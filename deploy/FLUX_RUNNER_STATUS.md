# FLUX Runner Deployment Status

**Date**: October 11, 2025
**Status**: ✅ FLUX Runner Deployed and Connected (Issue Identified)

---

## Summary

The FLUX runner has been successfully deployed on the GPU node and is now:
- ✅ Running with GPU access (NVIDIA H100 80GB)
- ✅ Connected to the correct Redis instance (147.185.41.134:6379)
- ✅ Polling the queue `queue:black-forest-labs/FLUX.1-dev`
- ⚠️ **Job Format Mismatch** - Worker expects Redis Hash format, but jobs are stored as JSON strings

---

## Current Configuration

### GPU Node
- **Host**: 147.185.41.134
- **SSH Port**: 20034
- **SSH User**: `user`
- **SSH Access**: `ssh -i <key> -p 20034 user@147.185.41.134`

### FLUX Runner Container
- **Container Name**: `vyvo-flux-runner`
- **Image**: `ghcr.io/hakankozakli/runner-qwen:flux`
- **Node ID**: `c98bc605-34aa-4a5f-be52-2c6156635ce9`
- **Model ID**: `black-forest-labs/FLUX.1-dev`
- **Worker ID**: `node1-worker-flux-1`
- **Redis URL**: `redis://147.185.41.134:6379` (control plane Redis)
- **Network**: `vyvo-network`
- **GPU**: NVIDIA H100 80GB (accessible via `runtime: nvidia`)

### Environment File
**Location**: `/etc/vyvo/flux.env`

```env
REDIS_URL=redis://147.185.41.134:6379
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
**Location**: `/opt/vyvo/docker-compose.yml`

The FLUX runner service is configured with:
- GPU runtime (`runtime: nvidia`)
- Worker command (`command: ["python", "run_worker.py"]`)
- Network bridge to control plane Redis (`vyvo-network`)
- Environment file mounting
- Health checks

---

## Issue Identified: Job Format Mismatch

### Problem

The worker code expects jobs to be stored in Redis as **hashes** (key-value pairs accessed via `HGETALL`), but the Gateway/Control Plane stores jobs as **JSON strings** (accessed via `GET`).

### Evidence

**Job Storage Format (Current)**:
```bash
$ redis-cli TYPE "job:8e64b980-b29b-4f86-8de5-1a59104f0813"
string

$ redis-cli GET "job:8e64b980-b29b-4f86-8de5-1a59104f0813"
{
  "id": "8e64b980-b29b-4f86-8de5-1a59104f0813",
  "model": "black-forest-labs/FLUX.1-dev",
  "status": "pending",
  "params": {...},
  ...
}
```

**Worker Code Expectation** (`/opt/venv/lib/python3.11/site-packages/app/worker.py:70-81`):
```python
# Get job data from Redis (stored as hash by admin API)
job_data = await self.redis_client.hgetall(job_key)  # ← Expects hash!
if not job_data:
    logger.error("Job {} not found in Redis", job_id)
    return
```

**Result**: When the worker tries to `HGETALL` a string key, it gets an empty result and logs "Job not found".

### Worker Behavior

The worker successfully:
1. Connects to Redis
2. Polls the queue
3. Would pick up jobs if they existed in the queue

But when it tries to process a job:
1. It pops the job ID from the queue
2. Tries to `HGETALL job:<id>`
3. Gets empty result (because it's a string, not a hash)
4. Logs error and skips the job

---

## Solutions

### Solution 1: Update Worker Code (Recommended)

Modify `/opt/venv/lib/python3.11/site-packages/app/worker.py` to handle both formats:

```python
async def process_job(self, job_id: str) -> None:
    """Process a single job"""
    if not self.redis_client or not self.backend:
        return

    job_key = f"job:{job_id}"

    try:
        # Try to get as hash first
        job_data = await self.redis_client.hgetall(job_key)

        # If empty, try as JSON string
        if not job_data:
            job_str = await self.redis_client.get(job_key)
            if job_str:
                job_obj = json.loads(job_str)
                # Convert JSON object to the expected format
                params = job_obj.get("params", {})
                job_data = {
                    "prompt": params.get("prompt", ""),
                    "negative_prompt": params.get("negative_prompt"),
                    "seed": params.get("seed"),
                    "steps": params.get("steps", 20),
                    "guidance": params.get("guidance_scale", 8.5),
                    "num_images": params.get("image_count", 1),
                    "width": params.get("width"),
                    "height": params.get("height"),
                    "status": job_obj.get("status", "pending"),
                }
            else:
                logger.error("Job {} not found in Redis", job_id)
                return

        # Rest of the processing logic...
```

**How to Apply**:
```bash
ssh -i <key> -p 20034 user@147.185.41.134
sudo docker exec -it vyvo-flux-runner bash

# Edit the file
vi /opt/venv/lib/python3.11/site-packages/app/worker.py

# Or copy a patched version
cat > /tmp/worker_patch.py << 'EOF'
# ... patched code ...
EOF
sudo docker cp /tmp/worker_patch.py vyvo-flux-runner:/opt/venv/lib/python3.11/site-packages/app/worker.py

# Restart the worker
sudo docker compose -f /opt/vyvo/docker-compose.yml restart flux-runner
```

### Solution 2: Update Gateway to Use Hashes

Modify the Gateway/Control Plane job creation to store jobs as Redis hashes instead of JSON strings.

**Pros**: Matches worker expectation
**Cons**: Requires changes to Gateway code and redeployment

### Solution 3: Build New Runner Image

Create a new runner image with the fixed worker code and deploy it.

**Pros**: Clean, permanent solution
**Cons**: Requires image build and push to registry

---

## Next Steps

### Immediate (To Get Jobs Processing)

1. **Apply Worker Patch** (Solution 1 above)
   ```bash
   ssh -i <key> -p 20034 user@147.185.41.134
   # Apply patch as shown above
   ```

2. **Clear Old Jobs** (optional)
   ```bash
   redis-cli -h 147.185.41.134 -p 6379 DEL queue:black-forest-labs/FLUX.1-dev
   redis-cli -h 147.185.41.134 -p 6379 DEL queue:c98bc605-34aa-4a5f-be52-2c6156635ce9:black-forest-labs/FLUX.1-dev
   # Clear individual job keys if needed
   ```

3. **Submit Test Job**
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

4. **Monitor Worker Logs**
   ```bash
   ssh -i <key> -p 20034 user@147.185.41.134
   sudo docker logs -f vyvo-flux-runner
   ```

### Long-term

1. **Standardize Job Format**: Decide on hash vs JSON string and update all components
2. **Update Runner Image**: Build new image with fixed worker code
3. **Add Queue Monitoring**: Set up alerts for stuck jobs
4. **Load Testing**: Verify GPU performance with real FLUX models

---

## Verification Commands

### Check Worker Status
```bash
ssh -i <key> -p 20034 user@147.185.41.134 'sudo docker ps | grep flux'
```

### View Worker Logs
```bash
ssh -i <key> -p 20034 user@147.185.41.134 'sudo docker logs --tail 50 vyvo-flux-runner'
```

### Check Redis Queue
```bash
# Queue length
redis-cli -h 147.185.41.134 -p 6379 LLEN "queue:black-forest-labs/FLUX.1-dev"

# List jobs in queue
redis-cli -h 147.185.41.134 -p 6379 LRANGE "queue:black-forest-labs/FLUX.1-dev" 0 -1

# Check specific job
redis-cli -h 147.185.41.134 -p 6379 GET "job:<job-id>"
```

### Check GPU Availability
```bash
ssh -i <key> -p 20034 user@147.185.41.134 'sudo docker exec vyvo-flux-runner nvidia-smi'
```

### Restart Worker
```bash
ssh -i <key> -p 20034 user@147.185.41.134 'cd /opt/vyvo && sudo docker compose restart flux-runner'
```

---

## Deployment History

1. **Initial Investigation**: Discovered no FLUX runner was deployed
2. **Created Configuration**: Set up `/etc/vyvo/flux.env` with correct settings
3. **Added to Docker Compose**: Created FLUX runner service definition
4. **Network Configuration**: Connected to `vyvo-network` for Redis access
5. **Redis URL Fix**: Changed from local Redis (`redis:6379`) to control plane (`147.185.41.134:6379`)
6. **Identified Issue**: Found job format mismatch between Gateway and Worker

---

## Contact & Support

- **GPU Node Access**: SSH key required (see above)
- **Redis**: 147.185.41.134:6379 (control plane)
- **Gateway**: http://147.185.41.134:8080
- **Admin API**: http://147.185.41.134:8083

For issues, check:
1. Worker logs: `sudo docker logs vyvo-flux-runner`
2. Redis connectivity: `redis-cli -h 147.185.41.134 -p 6379 PING`
3. GPU status: `nvidia-smi` on GPU node
4. Queue status: Redis commands above

---

**Status**: Worker is deployed and polling, but job format mismatch prevents processing. Apply Solution 1 to enable job processing.
