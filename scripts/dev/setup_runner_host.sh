#!/usr/bin/env bash
set -euo pipefail

# setup_runner_host.sh
# Provision a Vyvo GPU runner host using containerd/docker-compose
# without kubelet. Keeps layout compatible with future K8s adoption.

# --------- configurable vars ---------
NVIDIA_DRIVER_VERSION="550"
CONTAINER_RUNTIME="docker"    # docker or containerd
COMPOSE_VERSION="v2.29.2"
REPO_ROOT="/opt/vyvo"
SERVICE_USER="vyvo"
ENV_FILE="/etc/vyvo/qwen.env"

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    echo "Must run as root" >&2
    exit 1
  fi
}

install_prereqs() {
  log "Installing base packages"
  apt-get update
  apt-get install -y build-essential curl wget gnupg lsb-release ca-certificates
}

add_service_user() {
  if id -u "$SERVICE_USER" >/dev/null 2>&1; then
    return
  fi
  log "Creating service user $SERVICE_USER"
  useradd --system --create-home --shell /usr/sbin/nologin "$SERVICE_USER"
}

install_nvidia_drivers() {
  if command -v nvidia-smi >/dev/null 2>&1; then
    log "NVIDIA drivers already present"
    return
  fi
  log "NVIDIA drivers missing; please install H100 drivers manually before continuing"
  exit 1
}

install_docker() {
  log "Installing Docker Engine"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --yes --batch --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo \
"deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  usermod -aG docker "$SERVICE_USER"
}

install_containerd() {
  log "Installing containerd"
  install_prereqs
  apt-get install -y containerd
  mkdir -p /etc/containerd
  containerd config default > /etc/containerd/config.toml
  sed -i "s/SystemdCgroup = false/SystemdCgroup = true/" /etc/containerd/config.toml
  systemctl enable --now containerd
}

install_compose() {
  if [[ "$CONTAINER_RUNTIME" == "docker" ]]; then
    log "Docker Compose plugin already installed via apt"
  else
    log "Skipping compose install (containerd mode)"
  fi
}

configure_runtime() {
  log "Configuring NVIDIA container toolkit"
  local distribution mapped repo_url tmpfile
  distribution=$(. /etc/os-release; echo "$ID$VERSION_ID")

  mkdir -p /usr/share/keyrings
  curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
    | gpg --yes --batch --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

  mapped=$distribution
  case "$distribution" in
    ubuntu24.04|ubuntu23.10|ubuntu23.04|ubuntu22.10) mapped=ubuntu22.04 ;;
    ubuntu20.10|ubuntu20.04) mapped=ubuntu20.04 ;;
  esac

  repo_url="https://nvidia.github.io/libnvidia-container/${mapped}/libnvidia-container.list"
  tmpfile=$(mktemp)
  if ! curl -fsSL "$repo_url" -o "$tmpfile"; then
    log "Failed to download repo metadata for $distribution (mapped to $mapped)."
    rm -f "$tmpfile"
    exit 1
  fi
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#' "$tmpfile" \
    | tee /etc/apt/sources.list.d/nvidia-container-toolkit.list >/dev/null
  rm -f "$tmpfile"

  apt-get update
  apt-get install -y nvidia-container-toolkit

  if [[ "$CONTAINER_RUNTIME" == "docker" ]]; then
    nvidia-ctk runtime configure --runtime=docker --set-as-default
    systemctl restart docker
  else
    nvidia-ctk runtime configure --runtime=containerd --set-as-default
    systemctl restart containerd
  fi
}

layout_repo() {
  log "Laying out repository in $REPO_ROOT"
  mkdir -p "$REPO_ROOT"
  chown "$SERVICE_USER" "$REPO_ROOT"

  mkdir -p "$(dirname "$ENV_FILE")"
  if [[ ! -f "$ENV_FILE" ]]; then
    cat <<'ENV' > "$ENV_FILE"
# Qwen image runner environment
# Uncomment and populate the values below.
# === Local diffusers backend (recommended for on-prem GPUs) ===
# QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image
# HF_TOKEN=hf_xxx  # only if the model requires auth
# QWEN_TORCH_DTYPE=float16
# QWEN_DEVICE=cuda
# QWEN_TRUST_REMOTE_CODE=1
# QWEN_ENABLE_XFORMERS=1
# === DashScope fallback ===
# DASHSCOPE_API_KEY=your_dashscope_key
# QWEN_IMAGE_MODEL=wanx2.1
# QWEN_IMAGE_SIZE=1024*1024
ENV
    chown "$SERVICE_USER":"$SERVICE_USER" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
  fi

  cat <<COMPOSE > "$REPO_ROOT/docker-compose.yml"
services:
  minio:
    image: minio/minio:latest
    restart: unless-stopped
    command: server /data --console-address ":9090"
    environment:
      MINIO_ROOT_USER: vyvo
      MINIO_ROOT_PASSWORD: vyvo-secure-password-change-me
    ports:
      - "9000:9000"
      - "9090:9090"
    volumes:
      - minio-data:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 30s
      timeout: 5s
      retries: 5

  qwen-image:
    image: ghcr.io/hakankozakli/runner-qwen:latest
    restart: unless-stopped
    runtime: nvidia
    depends_on:
      - minio
    env_file:
      - $ENV_FILE
    environment:
      NVIDIA_VISIBLE_DEVICES: all
      VYVO_MODEL_ID: qwen/image
      MINIO_ENDPOINT: minio:9000
      MINIO_ACCESS_KEY: vyvo
      MINIO_SECRET_KEY: vyvo-secure-password-change-me
      MINIO_BUCKET: generated-images
      MINIO_SECURE: "false"
    ports:
      - "9001:9001"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://127.0.0.1:9001/healthz"]
      interval: 30s
      timeout: 5s
      retries: 5

volumes:
  minio-data:
COMPOSE

  chown "$SERVICE_USER":"$SERVICE_USER" "$REPO_ROOT"/docker-compose.yml
}

install_services() {
  log "Installing systemd units"
  cat <<SYSTEMD > /etc/systemd/system/vyvo-runners.service
[Unit]
Description=Vyvo Runner Stack
After=network-online.target docker.service containerd.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$REPO_ROOT
Environment=COMPOSE_PROJECT_NAME=vyvo
Environment=DOCKER_HOST=unix:///var/run/docker.sock
User=$SERVICE_USER
Group=$SERVICE_USER
SupplementaryGroups=docker
ExecStart=/usr/bin/docker compose up --remove-orphans
ExecStop=/usr/bin/docker compose down
Restart=always

[Install]
WantedBy=multi-user.target
SYSTEMD
  systemctl daemon-reload
  systemctl enable vyvo-runners.service
  systemctl start vyvo-runners.service
}

main() {
  require_root
  install_prereqs
  add_service_user
  install_nvidia_drivers

  if [[ "$CONTAINER_RUNTIME" == "docker" ]]; then
    install_docker
  else
    install_containerd
  fi

  install_compose
  configure_runtime
  layout_repo
  install_services
  log "Runner host setup complete"
  log "Check service status with: systemctl status vyvo-runners"
}

main "$@"
