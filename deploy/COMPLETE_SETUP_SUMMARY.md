# Complete FLUX Runner Setup - Summary

**Date**: October 11, 2025
**Status**: ✅ **WORKING** - Jobs are being processed successfully!

---

## 🎉 Achievement

The FLUX runner is now **fully operational** and processing jobs:

- ✅ Worker deployed on GPU node with H100 80GB access
- ✅ Connected to Redis and polling for jobs
- ✅ Successfully processing jobs (currently in stub mode)
- ✅ Jobs completing in ~0.5 seconds (stub backend)
- ✅ Images being generated and stored in MinIO
- ✅ Full job lifecycle: pending → processing → completed

**Test Job Result**:
```json
{
  "id": "59122be2-c500-4215-99ff-9b82345ffb88",
  "status": "completed",
  "inference_seconds": 0.53,
  "image_url": "http://147.185.41.134:20418/generated-images/stub/6292d2b7d1884740bceef637436e87d7-0.png"
}
```

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│  GPU Node (147.185.41.134)                                   │
│  ┌────────────────┐  ┌──────────────┐  ┌────────────────┐  │
│  │  Redis         │  │  MinIO       │  │  FLUX Runner   │  │
│  │  :6379         │  │  :9000       │  │  (worker mode) │  │
│  │                │←─┤              │←─┤  + H100 GPU    │  │
│  │  Queue storage │  │  Image store │  │  + Stub backend│  │
│  └────────────────┘  └──────────────┘  └────────────────┘  │
└─────────────────────────────────────────────────────────────┘
           ↑                                      ↑
           │ Queue jobs                           │ Poll queue
           │                                      │
┌──────────┴───────────┐              ┌──────────┴─────────┐
│  Control Plane       │              │  Worker Process    │
│  (local/remote)      │              │  Polls every 5s    │
│  Submit jobs via API │              │  Processes in stub │
└──────────────────────┘              └────────────────────┘
```

---

## Configuration Details

### GPU Node Setup
- **Host**: 147.185.41.134:20034 (SSH)
- **User**: `user`
- **SSH**: `ssh -i <key> -p 20034 user@147.185.41.134`

### FLUX Runner Container
- **Name**: `vyvo-flux-runner`
- **Image**: `ghcr.io/hakankozakli/runner-qwen:flux`
- **Command**: `python run_worker.py`
- **Network**: `vyvo-network`
- **GPU**: NVIDIA H100 80GB (runtime: nvidia)

### Environment Configuration
**File**: `/etc/vyvo/flux.env`
```env
REDIS_URL=redis://redis:6379                    # Local Redis (not remote!)
VYVO_MODEL_ID=black-forest-labs/FLUX.1-dev
VYVO_NODE_ID=c98bc605-34aa-4a5f-be52-2c6156635ce9
WORKER_ID=node1-worker-flux-1
WORKER_MODEL_ID=black-forest-labs/FLUX.1-dev
MINIO_ENDPOINT=minio:9000
MINIO_ACCESS_KEY=vyvo
MINIO_SECRET_KEY=vyvo-secure-password-change-me
MINIO_BUCKET=generated-images
MINIO_SECURE=false
MINIO_PUBLIC_URL=http://147.185.41.134:20418
LOG_LEVEL=INFO
FLUX_ENABLE_DIFFUSERS=0
ENABLE_TF32=true
PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True,max_split_size_mb:512,roundup_power2_divisions:16
```

### Docker Compose
**File**: `/opt/vyvo/docker-compose.yml`

The FLUX runner service is configured to:
1. Use the patched worker image
2. Connect to local Redis (`redis://redis:6379`)
3. Access GPU via `runtime: nvidia`
4. Run in worker mode (`command: ["python", "run_worker.py"]`)

---

## Worker Patch Applied

The worker was patched to handle **both** job storage formats:

### Original Format (Hash)
```redis
HGETALL job:<id>
```

### New Format (JSON String) - **Currently Used**
```redis
GET job:<id>
{
  "id": "...",
  "model": "black-forest-labs/FLUX.1-dev",
  "status": "pending",
  "params": {...}
}
```

**Patch Location**: `/opt/venv/lib/python3.11/site-packages/app/worker.py`

The patch automatically detects the Redis key type and processes accordingly.

---

## How to Submit Jobs

### Option 1: Direct Redis Submission (Current Method)

```bash
ssh -i <key> -p 20034 user@147.185.41.134

# Submit a job
JOB_ID=$(uuidgen | tr "[:upper:]" "[:lower:]")
sudo docker exec vyvo-redis redis-cli SET "job:$JOB_ID" \
  '{"id":"'$JOB_ID'","model":"black-forest-labs/FLUX.1-dev","status":"pending","params":{"prompt":"Your prompt here","steps":12,"guidance_scale":4,"width":768,"height":768},"created_at":'$(date +%s)'}'

sudo docker exec vyvo-redis redis-cli RPUSH "queue:black-forest-labs/FLUX.1-dev" "$JOB_ID"

# Check status
sudo docker exec vyvo-redis redis-cli GET "job:$JOB_ID" | jq '.'
```

### Option 2: Via Control Plane API (Requires Network Access)

```bash
curl -X POST http://147.185.41.134:3002/api/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "model_id": "black-forest-labs/FLUX.1-dev",
    "prompt": "A beautiful landscape",
    "steps": 12,
    "guidance_scale": 4,
    "width": 768,
    "height": 768
  }'
```

**Note**: This requires the Control Plane to be configured to submit jobs to the GPU node's Redis.

### Option 3: Admin Dashboard (Recommended for Testing)

The admin dashboard at `/app/admin/jobs` has a built-in job submission interface with:
- Model selection dropdown
- Prompt text area
- Parameter controls (steps, guidance, width, height, seed)
- Real-time status monitoring
- Image preview when complete

**Dashboard URL**: http://localhost:5173/app/admin/jobs

---

## Job Lifecycle

1. **Submit** → Job created in Redis as JSON string
   ```json
   {"id":"...","status":"pending",...}
   ```

2. **Queue** → Job ID added to queue
   ```
   RPUSH queue:black-forest-labs/FLUX.1-dev <job-id>
   ```

3. **Poll** → Worker polls queue every 5 seconds
   ```
   BLPOP queue:black-forest-labs/FLUX.1-dev 5
   ```

4. **Process** → Worker picks up job
   - Updates status to `processing`
   - Generates image (stub mode = 0.5s, real FLUX = ~10-30s)
   - Uploads to MinIO

5. **Complete** → Job updated with results
   ```json
   {
     "status": "completed",
     "image_url": "http://147.185.41.134:20418/...",
     "inference_seconds": 0.53
   }
   ```

---

## Monitoring & Management

### Check Worker Status
```bash
ssh -i <key> -p 20034 user@147.185.41.134

# Check container
sudo docker ps | grep flux

# View logs
sudo docker logs -f vyvo-flux-runner

# Restart worker
cd /opt/vyvo && sudo docker compose restart flux-runner
```

### Check Queue Status
```bash
# Queue length
sudo docker exec vyvo-redis redis-cli LLEN "queue:black-forest-labs/FLUX.1-dev"

# List jobs in queue
sudo docker exec vyvo-redis redis-cli LRANGE "queue:black-forest-labs/FLUX.1-dev" 0 -1

# Check specific job
sudo docker exec vyvo-redis redis-cli GET "job:<job-id>" | jq '.'
```

### Check GPU Usage
```bash
ssh -i <key> -p 20034 user@147.185.41.134

# Host GPU status
nvidia-smi

# GPU status from container
sudo docker exec vyvo-flux-runner nvidia-smi
```

### View Generated Images
MinIO console: http://147.185.41.134:9090

Bucket: `generated-images`

---

## Current Limitations & Next Steps

### ✅ Working
- Worker deployment and GPU access
- Job queue and processing
- JSON string job format handling
- Stub backend (fast testing)
- MinIO image storage

### ⚠️ Limitations
1. **Stub Backend Only**: Currently using stub backend that generates placeholder images
   - Real FLUX model not yet loaded
   - Need to set `FLUX_ENABLE_DIFFUSERS=1` or configure real backend

2. **Network Isolation**: Control Plane and GPU node Redis are separate
   - Jobs must be submitted directly to GPU node's Redis
   - Or Control Plane needs network access to GPU node

3. **No Admin Dashboard Integration**: Dashboard can't currently reach GPU node
   - Need to update `VITE_CONTROL_PLANE_URL` to point to GPU node
   - Or set up Redis replication/proxy

### 🚀 Next Steps

#### 1. Enable Real FLUX Model (High Priority)

Update `/etc/vyvo/flux.env`:
```env
FLUX_ENABLE_DIFFUSERS=1
# Or configure specific backend
```

This will load the actual FLUX model and generate real images instead of stubs.

#### 2. Integrate Admin Dashboard

**Option A**: Update dashboard to connect to GPU node Redis
```env
VITE_CONTROL_PLANE_URL=http://147.185.41.134:3002
```

**Option B**: Set up Redis bridge/replication
- Forward local Redis jobs to GPU node Redis
- Or use Redis Cluster

#### 3. Production Setup

1. **Systemd Service** (Already documented in `gpu-node-setup.sh`)
   ```bash
   systemctl enable vyvo-runners
   systemctl start vyvo-runners
   ```

2. **Monitoring**
   - Set up Prometheus metrics
   - Configure alerts for failed jobs
   - Monitor GPU utilization

3. **Load Balancing** (if multiple GPU nodes)
   - Distribute jobs across nodes
   - Implement node health checks

4. **Persistence**
   - Ensure Redis persistence (RDB/AOF)
   - Backup MinIO bucket
   - Log aggregation

---

## Troubleshooting

### Worker Not Picking Up Jobs

```bash
# Check worker logs
sudo docker logs vyvo-flux-runner | grep -E "ERROR|poll|picked"

# Verify Redis connection
sudo docker exec vyvo-flux-runner ping -c 1 redis

# Check queue
sudo docker exec vyvo-redis redis-cli LLEN "queue:black-forest-labs/FLUX.1-dev"
```

### Jobs Stuck in Pending

```bash
# Check if job is in queue
sudo docker exec vyvo-redis redis-cli LRANGE "queue:black-forest-labs/FLUX.1-dev" 0 -1

# Check job data format
sudo docker exec vyvo-redis redis-cli TYPE "job:<job-id>"
sudo docker exec vyvo-redis redis-cli GET "job:<job-id>"
```

### GPU Not Accessible

```bash
# Check GPU from host
nvidia-smi

# Check GPU from container
sudo docker exec vyvo-flux-runner nvidia-smi

# Verify runtime
docker inspect vyvo-flux-runner | grep -i runtime
```

### Worker Patch Lost After Restart

The patch is applied to the running container's filesystem, which is lost on recreate.

**Permanent Solution**: Build new image with patched worker.py

**Quick Fix**: Reapply patch after restart
```bash
sudo docker cp /tmp/worker_patch.py vyvo-flux-runner:/opt/venv/lib/python3.11/site-packages/app/worker.py
sudo docker compose -f /opt/vyvo/docker-compose.yml restart flux-runner
```

---

## Files Created

1. **GPU_NODE_SETUP.md** - Complete setup guide with all commands
2. **FLUX_RUNNER_STATUS.md** - Deployment status and issue analysis
3. **gpu-node-setup.sh** - Automated setup script
4. **worker_patch.py** - Patched worker supporting both job formats
5. **COMPLETE_SETUP_SUMMARY.md** (this file) - Final summary

---

## Quick Reference Commands

```bash
# SSH to GPU node
ssh -i <key> -p 20034 user@147.185.41.134

# View worker logs
sudo docker logs -f vyvo-flux-runner

# Submit test job
JOB_ID=$(uuidgen | tr "[:upper:]" "[:lower:]")
sudo docker exec vyvo-redis redis-cli SET "job:$JOB_ID" '{"id":"'$JOB_ID'","model":"black-forest-labs/FLUX.1-dev","status":"pending","params":{"prompt":"Test image","steps":12,"guidance_scale":4,"width":768,"height":768},"created_at":'$(date +%s)'}'
sudo docker exec vyvo-redis redis-cli RPUSH "queue:black-forest-labs/FLUX.1-dev" "$JOB_ID"

# Check job status
sudo docker exec vyvo-redis redis-cli GET "job:$JOB_ID" | jq '.status,.image_url'

# Restart worker
cd /opt/vyvo && sudo docker compose restart flux-runner

# Check GPU
nvidia-smi
sudo docker exec vyvo-flux-runner nvidia-smi
```

---

## Success Metrics

✅ **Job Processing**: Working (0.53s per job in stub mode)
✅ **GPU Access**: Available (H100 80GB detected)
✅ **Image Generation**: Working (stub images generated)
✅ **Queue System**: Working (Redis queue operational)
✅ **Storage**: Working (MinIO accessible)
⏳ **Real Model**: Pending (need to enable FLUX backend)
⏳ **Dashboard Integration**: Pending (network access needed)

---

**Status**: The infrastructure is complete and working. The next step is enabling the real FLUX model backend to generate actual images instead of stubs.
