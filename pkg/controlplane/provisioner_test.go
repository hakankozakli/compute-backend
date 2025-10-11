package controlplane

import (
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestComposeForNodeFlux(t *testing.T) {
	defaultFluxRunnerImage = "ghcr.io/vyvo/runner-flux:latest"
	defaultQwenRunnerImage = "ghcr.io/vyvo/runner-qwen:latest"
	defaultMinioImage = "quay.io/minio/minio:latest"

	p := &Provisioner{
		redisURL:    "redis://redis:6379",
		callbackURL: "http://orchestrator:8084/v1/internal/runners",
	}

	node := &Node{
		ID:         "node-1",
		Models:     []string{"black-forest-labs/FLUX.1-dev"},
		TorchDType: "bfloat16",
		Device:     "cuda",
		HFToken:    "hf_test",
	}

	composeBytes, err := p.composeForNode(node)
	if err != nil {
		t.Fatalf("composeForNode returned error: %v", err)
	}

	var cfg composeFile
	if err := yaml.Unmarshal(composeBytes, &cfg); err != nil {
		t.Fatalf("failed to decode compose yaml: %v", err)
	}

	svc, ok := cfg.Services["flux-runner"]
	if !ok {
		t.Fatalf("flux-runner service not present: %#v", cfg.Services)
	}
	if svc.Runtime != "nvidia" {
		t.Fatalf("expected nvidia runtime, got %q", svc.Runtime)
	}
	if svc.Environment["FLUX_ENABLE_DIFFUSERS"] != "1" {
		t.Fatalf("expected FLUX_ENABLE_DIFFUSERS=1, got %#v", svc.Environment)
	}
	if svc.Environment["FLUX_TORCH_DTYPE"] != "bfloat16" {
		t.Fatalf("unexpected FLUX_TORCH_DTYPE: %#v", svc.Environment)
	}
	if len(svc.EnvFile) == 0 || svc.EnvFile[0] != "/etc/vyvo/runner.env" {
		t.Fatalf("expected env_file to include runner env, got %#v", svc.EnvFile)
	}

	envContent := p.buildGlobalEnv(node)
	if !strings.Contains(envContent, "REDIS_URL=redis://redis:6379") {
		t.Fatalf("global env missing redis url: %s", envContent)
	}
	if !strings.Contains(envContent, "HF_TOKEN=hf_test") {
		t.Fatalf("global env missing hf token: %s", envContent)
	}
}

func TestComposeForNodeQwenAddsMinio(t *testing.T) {
	defaultFluxRunnerImage = "ghcr.io/vyvo/runner-flux:latest"
	defaultQwenRunnerImage = "ghcr.io/vyvo/runner-qwen:latest"
	defaultMinioImage = "quay.io/minio/minio:latest"

	p := &Provisioner{redisURL: "redis://redis:6379"}
	node := &Node{
		ID:      "node-2",
		Models:  []string{"qwen-image"},
		Device:  "cuda",
		HFToken: "hf_test",
	}

	composeBytes, err := p.composeForNode(node)
	if err != nil {
		t.Fatalf("composeForNode returned error: %v", err)
	}

	var cfg composeFile
	if err := yaml.Unmarshal(composeBytes, &cfg); err != nil {
		t.Fatalf("failed to decode compose yaml: %v", err)
	}

	if _, ok := cfg.Services["minio"]; !ok {
		t.Fatalf("expected minio service for qwen nodes: %#v", cfg.Services)
	}
	if svc, ok := cfg.Services["qwen-image"]; !ok {
		t.Fatalf("expected qwen-image service in compose")
	} else if len(svc.EnvFile) != 2 {
		t.Fatalf("expected qwen service to load runner and qwen env files, got %#v", svc.EnvFile)
	}
}
