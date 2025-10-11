package controlplane

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	yaml "gopkg.in/yaml.v3"
)

type Provisioner struct {
	store         Repository
	scriptBody    []byte
	logger        Logger
	redisURL      string
	callbackURL   string
	callbackToken string
}

type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

var (
	defaultFluxRunnerImage = valueOrDefault(os.Getenv("FLUX_RUNNER_IMAGE"), "ghcr.io/vyvo/runner-flux:latest")
	defaultQwenRunnerImage = valueOrDefault(os.Getenv("QWEN_RUNNER_IMAGE"), "ghcr.io/hakankozakli/runner-qwen:latest")
	defaultMinioImage      = valueOrDefault(os.Getenv("RUNNER_MINIO_IMAGE"), "quay.io/minio/minio:latest")
)

type composeFile struct {
	Version  string                    `yaml:"version"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]map[string]any `yaml:"volumes,omitempty"`
}

type composeService struct {
	Image       string              `yaml:"image"`
	Runtime     string              `yaml:"runtime,omitempty"`
	Restart     string              `yaml:"restart,omitempty"`
	DependsOn   []string            `yaml:"depends_on,omitempty"`
	EnvFile     []string            `yaml:"env_file,omitempty"`
	Environment map[string]string   `yaml:"environment,omitempty"`
	Command     []string            `yaml:"command,omitempty"`
	Ports       []string            `yaml:"ports,omitempty"`
	Volumes     []string            `yaml:"volumes,omitempty"`
	Healthcheck *composeHealthcheck `yaml:"healthcheck,omitempty"`
	Deploy      *composeDeploy      `yaml:"deploy,omitempty"`
	ShmSize     string              `yaml:"shm_size,omitempty"`
}

type composeHealthcheck struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty"`
	Retries  int      `yaml:"retries,omitempty"`
}

type composeDeploy struct {
	Resources *composeResources `yaml:"resources,omitempty"`
}

type composeResources struct {
	Reservations *composeReservations `yaml:"reservations,omitempty"`
}

type composeReservations struct {
	Devices []composeDevice `yaml:"devices,omitempty"`
}

type composeDevice struct {
	Driver       string   `yaml:"driver,omitempty"`
	Count        int      `yaml:"count,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty"`
}

func NewProvisioner(store Repository, script []byte, logger Logger, redisURL, callbackURL, callbackToken string) *Provisioner {
	return &Provisioner{
		store:         store,
		scriptBody:    script,
		logger:        logger,
		redisURL:      redisURL,
		callbackURL:   strings.TrimSpace(callbackURL),
		callbackToken: strings.TrimSpace(callbackToken),
	}
}

func (p *Provisioner) Provision(ctx context.Context, nodeID string) {
	go p.provision(nodeID)
}

func (p *Provisioner) provision(nodeID string) {
	// Use a background context with a generous timeout for the entire provisioning
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	node, ok := p.store.GetNode(nodeID)
	if !ok {
		p.logger.Error("provision: node not found", "nodeID", nodeID)
		return
	}

	if _, err := p.store.UpdateStatus(nodeID, NodeStatusProvisioning, "Connecting to node"); err != nil {
		p.logger.Error("provision: update status", "error", err)
		return
	}

	addr := fmt.Sprintf("%s:%d", node.IPAddress, node.SSHPort)
	authMethods, err := p.buildAuthMethods(node)
	if err != nil {
		p.fail(nodeID, err)
		return
	}

	config := &ssh.ClientConfig{
		User:            node.SSHUsername,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		p.fail(nodeID, fmt.Errorf("ssh dial failed: %w", err))
		return
	}
	defer client.Close()

	if err := p.installRunner(ctx, client, node); err != nil {
		p.fail(nodeID, err)
		return
	}

	if _, err := p.store.UpdateStatus(nodeID, NodeStatusReady, "Runner online"); err != nil {
		p.logger.Error("provision: update ready", "error", err)
	}
}

func (p *Provisioner) buildAuthMethods(node *Node) ([]ssh.AuthMethod, error) {
	authMethods := make([]ssh.AuthMethod, 0, 2)
	if key := strings.TrimSpace(node.SSHPrivateKey); key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("parse ssh private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password := strings.TrimSpace(node.SSHPassword); password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if len(authMethods) > 0 {
		return authMethods, nil
	}

	signer, err := defaultPrivateKeySigner()
	if err != nil {
		return nil, fmt.Errorf("no authentication method provided: %w", err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

func (p *Provisioner) installRunner(ctx context.Context, client *ssh.Client, node *Node) error {
	// Check if we're running as root
	whoami, err := runCommand(ctx, client, "whoami")
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}
	isRoot := strings.TrimSpace(whoami) == "root"
	sudo := ""
	if !isRoot {
		sudo = "sudo "
	}

	if err := p.pushFile(client, "/tmp/vyvo/setup_runner_host.sh", p.scriptBody, 0o755); err != nil {
		return fmt.Errorf("upload setup script: %w", err)
	}

	p.store.AppendEvent(node.ID, NodeStatusProvisioning, "Writing model configuration")

	if err := p.writeRunnerFiles(client, node, sudo); err != nil {
		return err
	}

	steps := []struct {
		message string
		command string
	}{
		{"Running setup script", sudo + "bash /tmp/vyvo/setup_runner_host.sh"},
		{"Reloading runner service", sudo + "systemctl daemon-reload"},
		{"Restarting runner", sudo + "systemctl restart vyvo-runners"},
	}

	for _, step := range steps {
		p.store.AppendEvent(node.ID, NodeStatusProvisioning, step.message)
		if _, err := runCommand(ctx, client, step.command); err != nil {
			return fmt.Errorf("%s: %w", step.message, err)
		}
	}

	time.Sleep(3 * time.Second)
	status, err := runCommand(ctx, client, sudo+"systemctl is-active vyvo-runners")
	if err != nil {
		return fmt.Errorf("runner status check failed: %w", err)
	}
	if !strings.Contains(status, "active") {
		logs, _ := runCommand(ctx, client, sudo+"journalctl -u vyvo-runners --no-pager -n 50")
		return fmt.Errorf("runner inactive: %s", logs)
	}
	return nil
}

func (p *Provisioner) writeRunnerFiles(client *ssh.Client, node *Node, sudo string) error {
	globalEnv := p.buildGlobalEnv(node)
	if err := p.pushFileWithSudo(client, "/etc/vyvo/runner.env", []byte(globalEnv), 0o600, sudo); err != nil {
		return err
	}

	if hasModel(node.Models, "qwen-image") || hasModel(node.Models, "qwen-image-dashscope") {
		qwenEnv := p.buildQwenEnv(node)
		if err := p.pushFileWithSudo(client, "/etc/vyvo/qwen.env", []byte(qwenEnv), 0o600, sudo); err != nil {
			return err
		}
	}

	composeBytes, err := p.composeForNode(node)
	if err != nil {
		return err
	}
	return p.pushFileWithSudo(client, "/opt/vyvo/docker-compose.yml", composeBytes, 0o640, sudo)
}

func (p *Provisioner) buildGlobalEnv(node *Node) string {
	lines := []string{
		"# Managed by Vyvo control plane",
		"# Updated: " + time.Now().UTC().Format(time.RFC3339),
		fmt.Sprintf("REDIS_URL=%s", p.redisURL),
		fmt.Sprintf("VYVO_NODE_ID=%s", node.ID),
	}
	if v := strings.TrimSpace(node.HFToken); v != "" {
		lines = append(lines, fmt.Sprintf("HF_TOKEN=%s", v))
	}
	if v := strings.TrimSpace(node.DashScopeAPIKey); v != "" {
		lines = append(lines, fmt.Sprintf("DASHSCOPE_API_KEY=%s", v))
	}
	if p.callbackURL != "" {
		lines = append(lines, fmt.Sprintf("RUNNER_CALLBACK_URL=%s", p.callbackURL))
	}
	if p.callbackToken != "" {
		lines = append(lines, fmt.Sprintf("RUNNER_CALLBACK_TOKEN=%s", p.callbackToken))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (p *Provisioner) buildQwenEnv(node *Node) string {
	lines := []string{
		"# Qwen runner configuration",
		"# Updated: " + time.Now().UTC().Format(time.RFC3339),
		"QWEN_DIFFUSERS_MODEL=" + valueOrDefault(os.Getenv("QWEN_DIFFUSERS_MODEL_DEFAULT"), "Qwen/Qwen-Image"),
		"QWEN_TORCH_DTYPE=" + valueOrDefault(node.TorchDType, "float16"),
		"QWEN_DEVICE=" + valueOrDefault(node.Device, "cuda"),
		fmt.Sprintf("QWEN_TRUST_REMOTE_CODE=%d", boolToInt(node.TrustRemoteCode || true)),
		fmt.Sprintf("QWEN_ENABLE_XFORMERS=%d", boolToInt(node.EnableXformers || true)),
		fmt.Sprintf("QWEN_ENABLE_TF32=%d", boolToInt(node.EnableTF32)),
	}
	if hasModel(node.Models, "qwen-image-dashscope") {
		lines = append(lines,
			"QWEN_IMAGE_MODEL="+valueOrDefault(os.Getenv("QWEN_IMAGE_MODEL_DEFAULT"), "wanx2.1"),
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

func (p *Provisioner) composeForNode(node *Node) ([]byte, error) {
	services := make(map[string]composeService)
	volumes := make(map[string]map[string]any)

	if hasModel(node.Models, "qwen-image") || hasModel(node.Models, "qwen-image-dashscope") {
		volumes["minio-data"] = map[string]any{}
		services["minio"] = composeService{
			Image:   defaultMinioImage,
			Restart: "unless-stopped",
			Command: []string{"server", "/data", "--console-address", ":9090"},
			Environment: map[string]string{
				"MINIO_ROOT_USER":     "vyvo",
				"MINIO_ROOT_PASSWORD": "vyvo-secure-password-change-me",
			},
			Ports:   []string{"9000:9000", "9090:9090"},
			Volumes: []string{"minio-data:/data"},
			Healthcheck: &composeHealthcheck{
				Test:     []string{"CMD", "curl", "-f", "http://localhost:9000/minio/health/live"},
				Interval: "30s",
				Timeout:  "5s",
				Retries:  5,
			},
		}

		qwenEnv := map[string]string{
			"NVIDIA_VISIBLE_DEVICES": "all",
			"VYVO_MODEL_ID":          "qwen/image",
			"MINIO_ENDPOINT":         "minio:9000",
			"MINIO_ACCESS_KEY":       "vyvo",
			"MINIO_SECRET_KEY":       "vyvo-secure-password-change-me",
			"MINIO_BUCKET":           "generated-images",
			"MINIO_SECURE":           "false",
		}

		services["qwen-image"] = composeService{
			Image:       defaultQwenRunnerImage,
			Runtime:     "nvidia",
			Restart:     "unless-stopped",
			DependsOn:   []string{"minio"},
			EnvFile:     []string{"/etc/vyvo/runner.env", "/etc/vyvo/qwen.env"},
			Environment: qwenEnv,
			Ports:       []string{"9001:9001"},
			Healthcheck: &composeHealthcheck{
				Test:     []string{"CMD", "curl", "-f", "http://127.0.0.1:9001/healthz"},
				Interval: "30s",
				Timeout:  "5s",
				Retries:  5,
			},
			Deploy: &composeDeploy{
				Resources: &composeResources{
					Reservations: &composeReservations{
						Devices: []composeDevice{
							{Driver: "nvidia", Count: 1, Capabilities: []string{"gpu"}},
						},
					},
				},
			},
		}
	}

	if hasModel(node.Models, "black-forest-labs/FLUX.1-dev") {
		fluxEnv := map[string]string{
			"NVIDIA_VISIBLE_DEVICES": "all",
			"VYVO_MODEL_ID":          "black-forest-labs/FLUX.1-dev",
			"FLUX_ENABLE_DIFFUSERS":  "1",
			"FLUX_TORCH_DTYPE":       valueOrDefault(node.TorchDType, "bfloat16"),
			"FLUX_DEVICE":            valueOrDefault(node.Device, "cuda"),
		}

		services["flux-runner"] = composeService{
			Image:       defaultFluxRunnerImage,
			Runtime:     "nvidia",
			Restart:     "unless-stopped",
			EnvFile:     []string{"/etc/vyvo/runner.env"},
			Environment: fluxEnv,
			Deploy: &composeDeploy{
				Resources: &composeResources{
					Reservations: &composeReservations{
						Devices: []composeDevice{
							{Driver: "nvidia", Count: 1, Capabilities: []string{"gpu"}},
						},
					},
				},
			},
			ShmSize: "2g",
		}
	}

	compose := composeFile{
		Version:  "3.9",
		Services: services,
	}
	if len(volumes) > 0 {
		compose.Volumes = volumes
	}

	data, err := yaml.Marshal(compose)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (p *Provisioner) pushFileWithSudo(client *ssh.Client, remotePath string, data []byte, perm os.FileMode, sudo string) error {
	if sudo == "" {
		return p.pushFile(client, remotePath, data, perm)
	}

	// For sudo, we need to write to a temp location first, then move with sudo
	tmpPath := "/tmp/vyvo_" + filepath.Base(remotePath)

	// Write to temp location
	if err := p.pushFile(client, tmpPath, data, perm); err != nil {
		return err
	}

	// Create directory if needed and move file with sudo
	dir := dirName(remotePath)
	commands := []string{
		fmt.Sprintf("%smkdir -p %s", sudo, dir),
		fmt.Sprintf("%smv %s %s", sudo, tmpPath, remotePath),
		fmt.Sprintf("%schmod %o %s", sudo, perm, remotePath),
		fmt.Sprintf("%schown vyvo:vyvo %s", sudo, remotePath),
	}

	for _, cmd := range commands {
		if _, err := runCommand(context.Background(), client, cmd); err != nil {
			return fmt.Errorf("failed to execute %q: %w", cmd, err)
		}
	}

	return nil
}

func (p *Provisioner) pushFile(client *ssh.Client, remotePath string, data []byte, perm os.FileMode) error {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sftpClient.Close()

	dir := dirName(remotePath)
	if err := sftpClient.MkdirAll(dir); err != nil {
		return err
	}

	file, err := sftpClient.Create(remotePath)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(perm); err != nil {
		return err
	}
	return nil
}

func (p *Provisioner) fail(nodeID string, err error) {
	p.logger.Error("provision failed", "nodeID", nodeID, "error", err)
	if _, updateErr := p.store.UpdateStatus(nodeID, NodeStatusError, err.Error()); updateErr != nil {
		p.logger.Error("record failure", "error", updateErr)
	}
}

func runCommand(ctx context.Context, client *ssh.Client, command string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(command)
	}()

	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}

	return strings.TrimSpace(stdout.String()), nil
}

func hasModel(models []string, target string) bool {
	for _, candidate := range models {
		if strings.EqualFold(candidate, target) {
			return true
		}
	}
	return false
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func dirName(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx == -1 {
		return "."
	}
	return path[:idx]
}

func defaultPrivateKeySigner() (ssh.Signer, error) {
	if path := strings.TrimSpace(os.Getenv("CONTROL_PLANE_DEFAULT_SSH_KEY")); path != "" {
		data, err := os.ReadFile(expandHome(path))
		if err != nil {
			return nil, err
		}
		return ssh.ParsePrivateKey(data)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		candidate := filepath.Join(home, ".ssh", name)
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		signer, parseErr := ssh.ParsePrivateKey(data)
		if parseErr != nil {
			continue
		}
		return signer, nil
	}
	return nil, fmt.Errorf("no default private key found")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
