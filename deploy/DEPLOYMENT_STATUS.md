# Vyvo Compute - Deployment Status

**Date:** October 7, 2025
**Node:** 147.185.41.134 (via SSH port 20034)

## Current Deployment

### Services Running

```
✅ vyvo-minio           - MinIO Object Storage (ports 9000, 9090)
✅ vyvo-redis           - Redis Queue Manager (port 6379)
✅ vyvo-worker-qwen-1   - Qwen Image Worker #1 (GPU)
✅ vyvo-worker-qwen-2   - Qwen Image Worker #2 (GPU)
```

### Architecture

```
┌─────────────────────────────────────────┐
│         Node: 147.185.41.134             │
├─────────────────────────────────────────┤
│                                          │
│  Storage Layer:                          │
│    └── MinIO (9000/9090)                │
│        ├── Bucket: generated-images     │
│        └── Public URL: :9000            │
│                                          │
│  Queue Layer:                            │
│    └── Redis (6379)                     │
│        └── Queue: queue:qwen/image      │
│                                          │
│  Compute Layer:                          │
│    ├── Worker-1 (GPU 0)                 │
│    └── Worker-2 (GPU 1)                 │
│                                          │
└─────────────────────────────────────────┘
```

## What's Working

1. **Redis Queue System** ✅
   - Jobs can be submitted to `queue:qwen/image`
   - Workers poll the queue using BLPOP
   - Job state tracked in Redis keys `job:{job_id}`

2. **MinIO Storage** ✅
   - Bucket `generated-images` created
   - Public read access configured
   - Images accessible at `http://147.185.41.134:9000/generated-images/{filename}`

3. **Worker Infrastructure** ✅
   - Two workers running with NVIDIA GPU runtime
   - Workers connect to Redis and MinIO successfully
   - Queue polling system operational

## What's Pending

### 1. Qwen Model Loading ⚠️

**Issue:** Workers currently fall back to stub backend because the Qwen/Qwen2.5-VL-7B-Image model:
- Requires HuggingFace authentication (401 error)
- May be gated or have incorrect model ID
- Needs to be cached locally during Docker build

**Solution Options:**
1. Use a different publicly available Qwen model
2. Add HuggingFace token to environment
3. Pre-download model and bake into Docker image
4. Use model volume with pre-downloaded weights

### 2. API Services (Not Deployed) 🔴

**Missing Services:**
- **Orchestrator** (Rust) - Job orchestration and model registry
- **Gateway** (Go) - Public API endpoint
- **Admin Dashboard** (Go) - Management and monitoring UI

**Build Issues:**
- Orchestrator: Rust compilation errors (needs investigation)
- Gateway: Missing config.yaml file in Docker context
- Admin: Built successfully but not deployed

### 3. Integration Testing 🔴

Need to test full end-to-end workflow:
1. Submit job via API
2. Job queued in Redis
3. Worker picks up job
4. Model generates image
5. Image uploaded to MinIO
6. Job completed in Redis
7. API returns result URL

## Quick Reference

### SSH Access
```bash
ssh -p 20034 user@147.185.41.134
```

### View Logs
```bash
sudo docker logs vyvo-worker-qwen-1
sudo docker logs vyvo-redis
sudo docker logs vyvo-minio
```

### Check Queue Status
```bash
sudo docker exec vyvo-redis redis-cli LLEN "queue:qwen/image"
sudo docker exec vyvo-redis redis-cli KEYS "job:*"
```

### Restart Services
```bash
cd /tmp
sudo docker-compose -f docker-compose-simple.yml restart
```

### Stop All
```bash
sudo docker-compose -f docker-compose-simple.yml down
```

## Files Created

### Deployment Configuration
- [`docker-compose-simple.yml`](docker-compose-simple.yml) - Current deployment (storage + workers)
- [`docker-compose-production.yml`](docker-compose-production.yml) - Full stack (all services)
- [`DEPLOYMENT.md`](DEPLOYMENT.md) - Complete deployment guide
- [`build-all.sh`](build-all.sh) - Build script for all services

### Code Changes
- [`runner-qwen/app/worker.py`](../runner-qwen/app/worker.py) - Queue worker implementation
- [`runner-qwen/run_worker.py`](../runner-qwen/run_worker.py) - Worker entry point
- [`cmd/admin-rest/main.go`](../cmd/admin-rest/main.go) - Admin REST API with queue monitoring
- [`pkg/queue/redis.go`](../pkg/queue/redis.go) - Redis queue package

## Next Steps

### Priority 1: Fix Model Loading
1. Identify correct Qwen model ID or use alternative
2. Add HuggingFace token if needed
3. Test model loading in isolated container
4. Update docker-compose with working configuration

### Priority 2: Deploy API Layer
1. Fix Orchestrator build issues
2. Add missing config files to Gateway
3. Deploy Orchestrator + Gateway + Admin
4. Configure port forwarding for external access

### Priority 3: End-to-End Testing
1. Submit test job through API
2. Verify job processing
3. Confirm image generation
4. Test image access via MinIO URL

## Port Forwarding Table

| Service | Internal Port | External Port | Status |
|---------|---------------|---------------|--------|
| MinIO API | 9000 | 20418 | ⚠️ Needs forwarding |
| MinIO Console | 9090 | - | Not exposed |
| Redis | 6379 | - | Internal only |
| Gateway | 8080 | 20363 | ⚠️ Not deployed |
| Admin | 8083 | TBD | ⚠️ Not deployed |

## Resources

- Repository: https://github.com/hakankozakli/compute-backend
- Last Commit: `94f5143` - "Add production deployment config and queue-based architecture"
- Qwen Documentation: https://github.com/QwenLM/Qwen-Image

---

**Status Legend:**
- ✅ Working
- ⚠️ Partially working / Needs attention
- 🔴 Not implemented / Blocked
