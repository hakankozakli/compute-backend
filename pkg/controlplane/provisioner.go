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
)

type Provisioner struct {
	store      Repository
	scriptBody []byte
	logger     Logger
}

type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func NewProvisioner(store Repository, script []byte, logger Logger) *Provisioner {
	return &Provisioner{store: store, scriptBody: script, logger: logger}
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

	if err := p.writeEnvFile(client, node, sudo); err != nil {
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

func (p *Provisioner) writeEnvFile(client *ssh.Client, node *Node, sudo string) error {
	var lines []string
	lines = append(lines, "# Managed by Vyvo control plane", "# Updated: "+time.Now().UTC().Format(time.RFC3339))

	for _, model := range node.Models {
		switch model {
		case "qwen-image":
			lines = append(lines,
				"QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image",
				fmt.Sprintf("QWEN_TORCH_DTYPE=%s", valueOrDefault(node.TorchDType, "float16")),
				fmt.Sprintf("QWEN_DEVICE=%s", valueOrDefault(node.Device, "cuda")),
				fmt.Sprintf("QWEN_TRUST_REMOTE_CODE=%d", boolToInt(node.TrustRemoteCode || true)),
				fmt.Sprintf("QWEN_ENABLE_XFORMERS=%d", boolToInt(node.EnableXformers || true)),
				fmt.Sprintf("QWEN_ENABLE_TF32=%d", boolToInt(node.EnableTF32)),
			)
			if node.HFToken != "" {
				lines = append(lines, fmt.Sprintf("HF_TOKEN=%s", node.HFToken))
			}
		case "qwen-image-dashscope":
			lines = append(lines,
				"DASHSCOPE_API_KEY="+node.DashScopeAPIKey,
				"QWEN_IMAGE_MODEL="+valueOrDefault(os.Getenv("QWEN_IMAGE_MODEL_DEFAULT"), "wanx2.1"),
			)
		default:
			lines = append(lines, fmt.Sprintf("# TODO configure model %s", model))
		}
	}

	content := strings.Join(lines, "\n") + "\n"
	return p.pushFileWithSudo(client, "/etc/vyvo/qwen.env", []byte(content), 0o600, sudo)
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
