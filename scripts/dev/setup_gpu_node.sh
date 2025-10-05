#!/usr/bin/env bash
set -euo pipefail

# setup_gpu_node.sh
# Automates configuration of an NVIDIA H100 server as a Kubernetes worker node.
# Run as root on the target machine after base OS install.

# ----------- configurable parameters -----------
KUBERNETES_VERSION="1.30.3-00"
CONTAINERD_VERSION="1.7.18"
KUBEADM_JOIN_COMMAND=""

# Optional: set to desired runtime class name
NVIDIA_RUNTIME_CLASS="nvidia"

# ----------- helper functions -----------
log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

require_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    echo "This script must be run as root" >&2
    exit 1
  fi
}

ensure_variable() {
  local name="$1" value="${!1:-}"
  if [[ -z "${value}" ]]; then
    echo "Environment variable ${name} must be set." >&2
    exit 1
  fi
}

install_prereqs() {
  log "Installing prerequisites"
  apt-get update
  apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release software-properties-common
}

install_containerd() {
  log "Installing containerd ${CONTAINERD_VERSION}"
  mkdir -p /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y containerd.io="${CONTAINERD_VERSION}-1"

  mkdir -p /etc/containerd
  containerd config default > /etc/containerd/config.toml
  sed -i "s/SystemdCgroup = false/SystemdCgroup = true/" /etc/containerd/config.toml
  systemctl enable --now containerd
}

configure_nvidia_container_toolkit() {
  log "Configuring NVIDIA container runtime"
  local distribution
  distribution=$(. /etc/os-release; echo "$ID$VERSION_ID")

  curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
    | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

  curl -fsSL "https://nvidia.github.io/libnvidia-container/${distribution}/libnvidia-container.list" \
    | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#' \
    | tee /etc/apt/sources.list.d/nvidia-container-toolkit.list >/dev/null

  apt-get update
  apt-get install -y nvidia-container-toolkit
  nvidia-ctk runtime configure --runtime=containerd --set-as-default
  systemctl restart containerd
}

install_kubernetes_binaries() {
  log "Installing Kubernetes components ${KUBERNETES_VERSION}"
  curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.30/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
  echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.30/deb/ /" > /etc/apt/sources.list.d/kubernetes.list
  apt-get update
  apt-get install -y kubelet="${KUBERNETES_VERSION}" kubeadm="${KUBERNETES_VERSION}" kubectl="${KUBERNETES_VERSION}"
  apt-mark hold kubelet kubeadm kubectl
  systemctl enable kubelet
}

configure_sysctl() {
  log "Applying kernel parameters for Kubernetes"
  cat <<'SYSCTL' >/etc/sysctl.d/99-kubernetes-cri.conf
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward = 1
SYSCTL
  modprobe br_netfilter || true
  sysctl --system
}

join_cluster() {
  ensure_variable KUBEADM_JOIN_COMMAND
  log "Joining Kubernetes cluster"
  eval "${KUBEADM_JOIN_COMMAND}"
}

label_gpu_node() {
  if [[ -z "${KUBEADM_JOIN_COMMAND}" ]]; then
    return
  fi
  if ! command -v kubectl >/dev/null 2>&1; then
    return
  fi
  local node_name
  node_name=$(hostname -s)
  log "Labeling node ${node_name} with gpu=true"
  kubectl label node "${node_name}" gpu=true --overwrite || true
  kubectl label node "${node_name}" accelerator=nvidia-h100 --overwrite || true
}

main() {
  require_root
  install_prereqs
  install_containerd
  configure_nvidia_container_toolkit
  install_kubernetes_binaries
  configure_sysctl
  join_cluster
  label_gpu_node
  log "Setup complete"
  log "Verify GPU availability with: kubectl describe node $(hostname -s) | grep -i allocatable -A5"
}

main "$@"
