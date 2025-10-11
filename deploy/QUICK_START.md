# FLUX Runner - Quick Start Guide

## 🎉 System Status: **OPERATIONAL**

The FLUX runner is deployed and processing jobs successfully!

---

## Quick Test

SSH into the GPU node and run:

```bash
ssh -p 20034 user@147.185.41.134

# Submit a test job
JOB_ID=$(uuidgen | tr "[:upper:]" "[:lower:]")

sudo docker exec vyvo-redis redis-cli SET "job:$JOB_ID" \
  "{\"id\":\"$JOB_ID\",\"model\":\"black-forest-labs/FLUX.1-dev\",\"status\":\"pending\",\"params\":{\"prompt\":\"A beautiful landscape\",\"steps\":12,\"guidance_scale\":4,\"width\":768,\"height\":768},\"created_at\":$(date +%s)}"

sudo docker exec vyvo-redis redis-cli RPUSH "queue:black-forest-labs/FLUX.1-dev" "$JOB_ID"

# Wait a few seconds, then check result
sleep 3
sudo docker exec vyvo-redis redis-cli GET "job:$JOB_ID" | jq '.'
```

Expected output:
```json
{
  "id": "...",
  "status": "completed",
  "image_url": "http://147.185.41.134:20418/generated-images/stub/...png",
  "inference_seconds": 0.5
}
```

---

## Management Commands

### View Worker Logs
```bash
sudo docker logs -f vyvo-flux-runner
```

### Check Queue Status
```bash
sudo docker exec vyvo-redis redis-cli LLEN "queue:black-forest-labs/FLUX.1-dev"
```

### Restart Worker
```bash
cd /opt/vyvo
sudo docker compose restart flux-runner
```

### Check GPU
```bash
nvidia-smi
```

---

## Current Configuration

- **Model**: black-forest-labs/FLUX.1-dev
- **Backend**: Stub (0.5s per image)
- **GPU**: NVIDIA H100 80GB
- **Queue**: Redis (local on GPU node)
- **Storage**: MinIO at http://147.185.41.134:20418

---

## Next Steps

### Enable Real FLUX Model

Edit `/etc/vyvo/flux.env` and set:
```env
FLUX_ENABLE_DIFFUSERS=1
```

Then restart:
```bash
cd /opt/vyvo
sudo docker compose restart flux-runner
```

This will load the actual FLUX model (requires ~30GB VRAM and takes 1-2 minutes to load).

### Integration with Admin Dashboard

The admin dashboard needs to connect to the GPU node's Redis to submit and monitor jobs.

See [COMPLETE_SETUP_SUMMARY.md](COMPLETE_SETUP_SUMMARY.md) for detailed integration steps.

---

## Files & Documentation

- **COMPLETE_SETUP_SUMMARY.md** - Full system overview and architecture
- **GPU_NODE_SETUP.md** - Detailed setup instructions
- **FLUX_RUNNER_STATUS.md** - Deployment status and troubleshooting
- **QUICK_START.md** (this file) - Quick reference

---

## Support

For issues:
1. Check worker logs: `sudo docker logs vyvo-flux-runner`
2. Verify GPU access: `nvidia-smi`
3. Check Redis queue: `sudo docker exec vyvo-redis redis-cli LLEN "queue:black-forest-labs/FLUX.1-dev"`

See [COMPLETE_SETUP_SUMMARY.md](COMPLETE_SETUP_SUMMARY.md#troubleshooting) for detailed troubleshooting.
