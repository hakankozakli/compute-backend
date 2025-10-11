#!/bin/bash
# GPU Node Setup Script
# This script sets up the FLUX runner on a GPU node

set -e

echo "=== Vyvo GPU Node Setup ==="
echo ""

# Configuration
REDIS_HOST="${REDIS_HOST:-147.185.41.134}"
REDIS_PORT="${REDIS_PORT:-6379}"
NODE_ID="${NODE_ID:-c98bc605-34aa-4a5f-be52-2c6156635ce9}"
MODEL_ID="${MODEL_ID:-black-forest-labs/FLUX.1-dev}"
DEPLOY_DIR="/opt/vyvo"

echo "Configuration:"
echo "  Redis: ${REDIS_HOST}:${REDIS_PORT}"
echo "  Node ID: ${NODE_ID}"
echo "  Model: ${MODEL_ID}"
echo "  Deploy Directory: ${DEPLOY_DIR}"
echo ""

# Step 1: Create deployment directory
echo "[1/6] Creating deployment directory..."
mkdir -p ${DEPLOY_DIR}
cd ${DEPLOY_DIR}

# Step 2: Create docker-compose.yml for runners
echo "[2/6] Creating docker-compose.yml..."
cat > docker-compose.yml <<'EOF'
version: "3.9"

services:
  flux_runner:
    image: ghcr.io/hakankozakli/vyvo-flux-runner:latest
    container_name: vyvo-flux-runner
    environment:
      - REDIS_URL=redis://REDIS_HOST:REDIS_PORT
      - VYVO_MODEL_ID=MODEL_ID
      - VYVO_NODE_ID=NODE_ID
      - FLUX_ENABLE_DIFFUSERS=0
      - RUST_LOG=info
    restart: unless-stopped
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
    healthcheck:
      test: ["CMD-SHELL", "python3 -c 'import sys; sys.exit(0)'"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s

networks:
  default:
    name: vyvo
EOF

# Replace placeholders
sed -i "s/REDIS_HOST/${REDIS_HOST}/g" docker-compose.yml
sed -i "s/REDIS_PORT/${REDIS_PORT}/g" docker-compose.yml
sed -i "s/NODE_ID/${NODE_ID}/g" docker-compose.yml
sed -i "s/MODEL_ID/${MODEL_ID}/g" docker-compose.yml

echo "  Created docker-compose.yml"

# Step 3: Create systemd service
echo "[3/6] Creating systemd service..."
cat > /etc/systemd/system/vyvo-runners.service <<'EOF'
[Unit]
Description=Vyvo GPU Runners
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/vyvo
ExecStartPre=/usr/bin/docker network create vyvo || true
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
ExecReload=/usr/bin/docker compose restart

[Install]
WantedBy=multi-user.target
EOF

echo "  Created vyvo-runners.service"

# Step 4: Reload systemd
echo "[4/6] Reloading systemd..."
systemctl daemon-reload

# Step 5: Enable and start service
echo "[5/6] Enabling and starting service..."
systemctl enable vyvo-runners
systemctl restart vyvo-runners

# Step 6: Check status
echo "[6/6] Checking service status..."
sleep 5
systemctl status vyvo-runners --no-pager

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Useful commands:"
echo "  Check status:  systemctl status vyvo-runners"
echo "  View logs:     journalctl -u vyvo-runners -f"
echo "  Restart:       systemctl restart vyvo-runners"
echo "  Stop:          systemctl stop vyvo-runners"
echo ""
echo "Docker commands:"
echo "  View logs:     docker compose -f ${DEPLOY_DIR}/docker-compose.yml logs -f flux_runner"
echo "  Restart:       docker compose -f ${DEPLOY_DIR}/docker-compose.yml restart flux_runner"
echo "  Shell:         docker compose -f ${DEPLOY_DIR}/docker-compose.yml exec flux_runner bash"
echo ""
