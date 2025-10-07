# Vyvo Compute - Production Deployment Guide

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Node: 147.185.41.134                  │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  STORAGE LAYER                                          │
│  ├── MinIO (Port 9000/9090)                             │
│  │   └── Shared object storage for all nodes           │
│  │                                                       │
│  QUEUE LAYER                                            │
│  ├── Redis (Port 6379)                                  │
│  │   └── Job queue and state management                │
│  │                                                       │
│  CONTROL LAYER                                          │
│  ├── Orchestrator (Port 8081) [Rust]                   │
│  │   └── Job orchestration, model registry             │
│  │                                                       │
│  API LAYER                                              │
│  ├── Gateway (Port 8080 → External 20363) [Go]         │
│  │   └── Public API, routing, load balancing           │
│  │                                                       │
│  ADMIN LAYER                                            │
│  ├── Admin Dashboard (Port 8083) [Go]                  │
│  │   └── Monitoring, management, system control        │
│  │                                                       │
│  COMPUTE LAYER                                          │
│  ├── Worker Qwen-1 (GPU) [Python]                      │
│  │   └── AI inference, queue polling                   │
│  ├── Worker Qwen-2 (GPU) [Python]                      │
│  │   └── AI inference, queue polling                   │
│  └── ... (Scale horizontally)                          │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

## Port Forwarding Configuration

| Service | Internal Port | External Port | Description |
|---------|---------------|---------------|-------------|
| Gateway | 8080 | 20363 | Main API endpoint |
| Orchestrator | 8081 | - | Internal only |
| Admin | 8083 | TBD | Management UI |
| Redis | 6379 | - | Internal only |
| MinIO API | 9000 | 20418 | S3-compatible storage |
| MinIO Console | 9090 | - | Web UI (optional) |

## Prerequisites

### On the Deployment Machine (Local):
- Docker (for building images)
- Make (optional, for using Makefile)
- SSH access to node

### On the Compute Node (147.185.41.134):
- Docker with NVIDIA runtime
- NVIDIA GPU drivers (CUDA)
- Docker Compose
- Sufficient disk space (50GB+ recommended)

## Build Process

### Option 1: Build Locally (Recommended)

```bash
# Clone and navigate to backend
cd /Users/hakankozakli/Server/vyvo/compute/backend

# Build all services
./deploy/build-all.sh

# This creates:
# - vyvo-orchestrator:latest
# - vyvo-gateway:latest
# - vyvo-admin:latest
# - ghcr.io/hakankozakli/runner-qwen:qwen-image
```

### Option 2: Build on Node

```bash
# Package and upload
cd /Users/hakankozakli/Server/vyvo/compute/backend
tar czf vyvo-backend.tar.gz . --exclude=.git --exclude=.gocache --exclude=target

# Upload to node
scp -P 20034 vyvo-backend.tar.gz user@147.185.41.134:/tmp/

# SSH to node and build
ssh -p 20034 user@147.185.41.134
cd /tmp
tar xzf vyvo-backend.tar.gz
./deploy/build-all.sh
```

## Deployment

### Step 1: Save and Load Docker Images (if building locally)

```bash
# Save images
docker save vyvo-orchestrator:latest | gzip > orchestrator.tar.gz
docker save vyvo-gateway:latest | gzip > gateway.tar.gz
docker save vyvo-admin:latest | gzip > admin.tar.gz
docker save ghcr.io/hakankozakli/runner-qwen:qwen-image | gzip > runner-qwen.tar.gz

# Upload to node
scp -P 20034 *.tar.gz user@147.185.41.134:/tmp/

# Load on node
ssh -p 20034 user@147.185.41.134 << 'EOF'
cd /tmp
docker load < orchestrator.tar.gz
docker load < gateway.tar.gz
docker load < admin.tar.gz
docker load < runner-qwen.tar.gz
EOF
```

### Step 2: Deploy docker-compose

```bash
# Upload compose file
scp -P 20034 deploy/docker-compose-production.yml user@147.185.41.134:/tmp/

# Deploy on node
ssh -p 20034 user@147.185.41.134 << 'EOF'
cd /tmp
docker-compose -f docker-compose-production.yml down
docker-compose -f docker-compose-production.yml up -d
EOF
```

### Step 3: Verify Deployment

```bash
ssh -p 20034 user@147.185.41.134

# Check all services are running
docker ps

# Check logs
docker logs vyvo-orchestrator
docker logs vyvo-gateway
docker logs vyvo-admin
docker logs vyvo-worker-qwen-1

# Test health endpoints
curl http://localhost:8081/health  # Orchestrator
curl http://localhost:8080/health  # Gateway
curl http://localhost:8083/health  # Admin
```

## Testing the System

### 1. Test MinIO (Storage)
```bash
# Check bucket exists
curl http://147.185.41.134:20418/generated-images/

# Should return XML listing
```

### 2. Test Redis (Queue)
```bash
ssh -p 20034 user@147.185.41.134
docker exec vyvo-redis redis-cli ping
# Should return: PONG
```

### 3. Test Gateway (API)
```bash
# Submit inference job
curl -X POST http://147.185.41.134:20363/v1/inference \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen/image",
    "prompt": "A beautiful sunset",
    "steps": 30,
    "width": 1024,
    "height": 1024
  }'

# Should return job_id
```

### 4. Test Job Status
```bash
# Get job status (replace JOB_ID)
curl http://147.185.41.134:20363/v1/jobs/{JOB_ID}
```

### 5. Test Admin Dashboard
```bash
# List queues
curl http://147.185.41.134:8083/admin/queues

# List jobs
curl http://147.185.41.134:8083/admin/jobs

# System metrics
curl http://147.185.41.134:8083/admin/metrics
```

## Scaling

### Adding More Workers (Same Node)

Edit `docker-compose-production.yml`:

```yaml
  worker-qwen-3:
    image: ghcr.io/hakankozakli/runner-qwen:qwen-image
    container_name: vyvo-worker-qwen-3
    # ... same config as worker-qwen-1
    environment:
      - WORKER_ID=node1-worker-qwen-3
```

Then:
```bash
docker-compose -f docker-compose-production.yml up -d worker-qwen-3
```

### Adding New Compute Node

On new node, deploy only workers:

```yaml
# docker-compose-compute-only.yml
version: '3.8'

services:
  worker-qwen-1:
    image: ghcr.io/hakankozakli/runner-qwen:qwen-image
    runtime: nvidia
    environment:
      - WORKER_MODEL_ID=qwen/image
      - REDIS_URL=redis://147.185.41.134:6379      # Point to main node
      - MINIO_ENDPOINT=147.185.41.134:9000          # Point to main node
      - WORKER_ID=node2-worker-qwen-1               # Unique worker ID
      # ... rest of config
```

## Monitoring

### Check Service Health
```bash
# All services
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"

# Individual service logs
docker logs -f vyvo-worker-qwen-1
docker logs -f vyvo-orchestrator
```

### Check Queue Status
```bash
docker exec vyvo-redis redis-cli LLEN "queue:qwen/image"
docker exec vyvo-redis redis-cli KEYS "job:*"
```

### Check GPU Usage
```bash
nvidia-smi
watch -n 1 nvidia-smi
```

## Troubleshooting

### Worker Not Processing Jobs
```bash
# Check worker logs
docker logs vyvo-worker-qwen-1

# Check if connected to Redis
docker exec vyvo-redis redis-cli CLIENT LIST | grep worker

# Manually submit test job
docker exec vyvo-redis redis-cli RPUSH "queue:qwen/image" "test-job-123"
```

### Gateway Not Responding
```bash
# Check orchestrator connection
docker logs vyvo-gateway

# Test orchestrator directly
curl http://localhost:8081/health
```

### MinIO Issues
```bash
# Check MinIO status
curl http://localhost:9000/minio/health/live

# Check bucket permissions
docker exec vyvo-minio mc ls local/generated-images
```

## Backup and Recovery

### Backup Redis Data
```bash
docker exec vyvo-redis redis-cli SAVE
docker cp vyvo-redis:/data/dump.rdb ./redis-backup.rdb
```

### Backup MinIO Data
```bash
docker exec vyvo-minio mc mirror local/generated-images /backup/
```

## Security Notes

1. **Change default passwords** in docker-compose-production.yml:
   - MINIO_ROOT_PASSWORD
   - Redis password (add AUTH if needed)

2. **Use HTTPS** in production:
   - Add reverse proxy (nginx/Traefik)
   - Enable SSL certificates

3. **Restrict network access**:
   - Only expose necessary ports
   - Use firewall rules
   - Consider VPN for inter-node communication

## Environment Variables

Key environment variables that can be customized:

```bash
# MinIO
MINIO_ROOT_USER=vyvo
MINIO_ROOT_PASSWORD=change-me
MINIO_PUBLIC_URL=http://your-domain.com:20418

# Redis
REDIS_URL=redis://redis:6379

# Worker
WORKER_MODEL_ID=qwen/image
WORKER_ID=unique-worker-id
LOG_LEVEL=INFO

# GPU
QWEN_DEVICE=cuda
QWEN_TORCH_DTYPE=bfloat16
```

## Support

For issues or questions:
1. Check logs: `docker logs <container-name>`
2. Check status: `docker ps -a`
3. Review this documentation
4. Check GitHub issues

---

**Last Updated:** 2025-10-07
**Version:** 1.0.0
