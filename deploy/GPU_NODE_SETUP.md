# GPU Node Runner Setup Guide

## Current Situation

**Problem**: The control plane is running and has marked the GPU node (GPU-1, ID: `c98bc605-34aa-4a5f-be52-2c6156635ce9`) as READY. Jobs are being queued in Redis, but no runner is consuming them, so they stay in "Pending" status indefinitely.

**Root Cause**: The FLUX runner container is not running on the GPU node to process jobs from the Redis queue.

**Queue Status**:
- 3 jobs pending in queue `queue:black-forest-labs/FLUX.1-dev`
- 23 total job entries in Redis
- All jobs have `status: "pending"` and are assigned to node `c98bc605-34aa-4a5f-be52-2c6156635ce9`

---

## Solution: Deploy the FLUX Runner on GPU Node

### Prerequisites

1. **SSH Access**: You need SSH access to the GPU node at `147.185.41.134`
2. **Docker**: Docker and docker-compose must be installed
3. **NVIDIA GPU**: GPU drivers and NVIDIA Container Toolkit must be installed
4. **Network**: The GPU node must be able to reach Redis at `147.185.41.134:6379`

---

## Setup Instructions

### Option 1: Automated Setup (Recommended)

1. **Copy the setup script to the GPU node**:
   ```bash
   scp backend/deploy/gpu-node-setup.sh root@147.185.41.134:/tmp/
   ```

2. **SSH into the GPU node**:
   ```bash
   ssh root@147.185.41.134
   ```

3. **Run the setup script**:
   ```bash
   chmod +x /tmp/gpu-node-setup.sh
   /tmp/gpu-node-setup.sh
   ```

4. **Verify the runner is working**:
   ```bash
   # Check service status
   systemctl status vyvo-runners

   # View logs
   journalctl -u vyvo-runners -f

   # Or view docker logs directly
   docker compose -f /opt/vyvo/docker-compose.yml logs -f flux_runner
   ```

---

### Option 2: Manual Setup

#### Step 1: Create Deployment Directory

```bash
ssh root@147.185.41.134

mkdir -p /opt/vyvo
cd /opt/vyvo
```

#### Step 2: Create docker-compose.yml

```bash
cat > /opt/vyvo/docker-compose.yml <<'EOF'
version: "3.9"

services:
  flux_runner:
    image: ghcr.io/hakankozakli/vyvo-flux-runner:latest
    container_name: vyvo-flux-runner
    environment:
      - REDIS_URL=redis://147.185.41.134:6379
      - VYVO_MODEL_ID=black-forest-labs/FLUX.1-dev
      - VYVO_NODE_ID=c98bc605-34aa-4a5f-be52-2c6156635ce9
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
```

#### Step 3: Create Network and Start Runner

```bash
# Create docker network
docker network create vyvo || true

# Start the runner
docker compose up -d

# Check logs
docker compose logs -f flux_runner
```

#### Step 4: Create Systemd Service (Optional but Recommended)

```bash
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

# Reload systemd
systemctl daemon-reload

# Enable and start service
systemctl enable vyvo-runners
systemctl start vyvo-runners

# Check status
systemctl status vyvo-runners
```

---

## Verification

### 1. Check Runner Logs

The runner should show:
- Connection to Redis
- Polling for jobs from the queue
- Processing jobs as they arrive

```bash
# Via systemd
journalctl -u vyvo-runners -f

# Or via docker
docker compose -f /opt/vyvo/docker-compose.yml logs -f flux_runner
```

**Expected log output**:
```
INFO: Connected to Redis at redis://147.185.41.134:6379
INFO: Worker starting for model: black-forest-labs/FLUX.1-dev
INFO: Polling queue: queue:c98bc605-34aa-4a5f-be52-2c6156635ce9:black-forest-labs/FLUX.1-dev
INFO: Found job: 8e64b980-b29b-4f86-8de5-1a59104f0813
INFO: Processing job...
```

### 2. Monitor Redis Queue

From your local machine:

```bash
# Check queue length (should decrease as jobs are processed)
redis-cli -h localhost -p 6379 LLEN "queue:black-forest-labs/FLUX.1-dev"

# List pending job IDs
redis-cli -h localhost -p 6379 LRANGE "queue:black-forest-labs/FLUX.1-dev" 0 -1

# Check a specific job status
redis-cli -h localhost -p 6379 GET "job:8e64b980-b29b-4f86-8de5-1a59104f0813" | jq '.status'
```

### 3. Submit a Test Job

```bash
curl -X POST http://147.185.41.134:8080/v1/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "model": "black-forest-labs/FLUX.1-dev",
    "params": {
      "prompt": "A beautiful sunset over mountains",
      "steps": 12,
      "guidance_scale": 4,
      "width": 768,
      "height": 768
    }
  }'
```

---

## Clearing Old Jobs (Optional)

If you want to clear the old stub jobs before starting:

```bash
# Clear the main queue
redis-cli -h localhost -p 6379 DEL "queue:black-forest-labs/FLUX.1-dev"

# Clear the node-specific queue
redis-cli -h localhost -p 6379 DEL "queue:c98bc605-34aa-4a5f-be52-2c6156635ce9:black-forest-labs/FLUX.1-dev"

# Clear all job entries
redis-cli -h localhost -p 6379 --scan --pattern "job:*" | xargs redis-cli -h localhost -p 6379 DEL
```

---

## Troubleshooting

### Runner won't start

1. **Check Docker is running**:
   ```bash
   systemctl status docker
   ```

2. **Check NVIDIA runtime**:
   ```bash
   docker run --rm --gpus all nvidia/cuda:11.8.0-base-ubuntu22.04 nvidia-smi
   ```

3. **Check network connectivity to Redis**:
   ```bash
   telnet 147.185.41.134 6379
   # Or
   nc -zv 147.185.41.134 6379
   ```

### Runner starts but doesn't process jobs

1. **Check Redis connection in logs**:
   ```bash
   docker compose -f /opt/vyvo/docker-compose.yml logs flux_runner | grep -i redis
   ```

2. **Verify the correct queue name**:
   ```bash
   # The runner should poll this queue:
   redis-cli -h 147.185.41.134 -p 6379 LRANGE "queue:c98bc605-34aa-4a5f-be52-2c6156635ce9:black-forest-labs/FLUX.1-dev" 0 -1
   ```

3. **Check environment variables**:
   ```bash
   docker compose -f /opt/vyvo/docker-compose.yml exec flux_runner env | grep VYVO
   ```

### Jobs fail during processing

1. **Check GPU availability**:
   ```bash
   docker compose -f /opt/vyvo/docker-compose.yml exec flux_runner nvidia-smi
   ```

2. **Check model loading**:
   ```bash
   docker compose -f /opt/vyvo/docker-compose.yml logs flux_runner | grep -i "loading model"
   ```

3. **Increase log verbosity**:
   ```bash
   # Edit docker-compose.yml and change:
   - RUST_LOG=debug
   # Then restart:
   docker compose -f /opt/vyvo/docker-compose.yml restart flux_runner
   ```

---

## Next Steps After Setup

1. **Monitor the first job completion**:
   - Watch the logs to see a job go from pending → processing → completed
   - Verify the output is stored correctly

2. **Check the Admin Dashboard**:
   - Visit http://147.185.41.134:8083/api/jobs
   - Verify jobs are moving from "pending" to "completed"

3. **Run smoke tests**:
   - Submit multiple jobs
   - Verify they queue and process sequentially
   - Check for any errors or performance issues

4. **Set up monitoring** (optional):
   - Configure alerts for runner failures
   - Set up metrics collection
   - Monitor GPU utilization

---

## Quick Reference

### Useful Commands

```bash
# Service management
systemctl status vyvo-runners
systemctl restart vyvo-runners
systemctl stop vyvo-runners
journalctl -u vyvo-runners -f

# Docker management
docker compose -f /opt/vyvo/docker-compose.yml ps
docker compose -f /opt/vyvo/docker-compose.yml logs -f flux_runner
docker compose -f /opt/vyvo/docker-compose.yml restart flux_runner
docker compose -f /opt/vyvo/docker-compose.yml down
docker compose -f /opt/vyvo/docker-compose.yml up -d

# Debug
docker compose -f /opt/vyvo/docker-compose.yml exec flux_runner bash
docker compose -f /opt/vyvo/docker-compose.yml exec flux_runner nvidia-smi
```

### Configuration

- **Redis URL**: `redis://147.185.41.134:6379`
- **Node ID**: `c98bc605-34aa-4a5f-be52-2c6156635ce9`
- **Model ID**: `black-forest-labs/FLUX.1-dev`
- **Queue Name**: `queue:c98bc605-34aa-4a5f-be52-2c6156635ce9:black-forest-labs/FLUX.1-dev`
- **Fallback Queue**: `queue:black-forest-labs/FLUX.1-dev`

---

## Contact

If you encounter issues not covered in this guide, check:
1. Runner logs for error messages
2. Redis connectivity
3. GPU availability
4. Model loading issues

The control plane is working correctly - once the runner starts, jobs should process immediately.
