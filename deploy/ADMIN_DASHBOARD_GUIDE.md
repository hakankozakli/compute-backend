# Admin Dashboard & Job Submission Guide

## 🎯 Current Status

✅ **FLUX Worker**: Running on GPU node, processing jobs successfully
✅ **Admin Dashboard**: Available at http://localhost:5173
✅ **Job Submission**: Working via helper script
⚠️ **Direct Dashboard Integration**: Pending (network configuration needed)

---

## Quick Job Submission

### Method 1: Helper Script (Recommended)

Use the provided script to submit jobs directly to the GPU node:

```bash
cd /Users/hakankozakli/Server/vyvo/compute/backend/deploy

# Submit a job
./submit-job-to-gpu-node.sh \
  --prompt "A beautiful landscape with mountains and a lake" \
  --steps 12 \
  --guidance 4 \
  --width 768 \
  --height 768
```

**Output:**
```
📝 Submitting job to GPU node...
   Job ID: ef7ba1ea-cc6a-4d8d-a577-1ca632f1d7ac
   Prompt: A beautiful landscape...

✅ Job queued successfully
📊 Job Status: completed
⚡ Processing Time: 0.52s
🖼️  Image URL: http://147.185.41.134:20418/generated-images/...
```

**Script Options:**
- `--prompt TEXT` - Image prompt (required)
- `--steps NUM` - Diffusion steps (default: 12)
- `--guidance NUM` - Guidance scale (default: 4)
- `--width NUM` - Image width (default: 768)
- `--height NUM` - Image height (default: 768)
- `--seed NUM` - Random seed (optional)

### Method 2: Manual Redis Submission

SSH into the GPU node and submit directly:

```bash
ssh -p 20034 user@147.185.41.134

JOB_ID=$(uuidgen | tr "[:upper:]" "[:lower:]")

sudo docker exec vyvo-redis redis-cli SET "job:$JOB_ID" \
  "{\"id\":\"$JOB_ID\",\"model\":\"black-forest-labs/FLUX.1-dev\",\"status\":\"pending\",\"params\":{\"prompt\":\"Your prompt here\",\"steps\":12,\"guidance_scale\":4,\"width\":768,\"height\":768},\"created_at\":$(date +%s)}"

sudo docker exec vyvo-redis redis-cli RPUSH "queue:black-forest-labs/FLUX.1-dev" "$JOB_ID"

# Check result
sleep 3
sudo docker exec vyvo-redis redis-cli GET "job:$JOB_ID" | jq '.'
```

---

## Admin Dashboard

### Accessing the Dashboard

**URL**: http://localhost:5173

**Available Pages:**
- `/app/admin/jobs` - Job history and submission
- `/app/admin/nodes` - Node management
- `/app/admin/models` - Model registry
- `/app/admin/runners` - Runner monitoring

### Jobs Page Features

The Jobs page (`/app/admin/jobs`) includes:

1. **Test Models Section** (top card)
   - Model dropdown selector
   - Prompt text area
   - Parameter controls (steps, guidance, width, height, seed)
   - "Run Test" button
   - Real-time status display

2. **Statistics Cards**
   - Total jobs
   - Queued jobs
   - Running jobs
   - Failed jobs

3. **Job History Table**
   - Job ID, Model, Status
   - Queue position
   - Worker/Node assignment
   - Created/Updated timestamps
   - View details button

### Current Limitation

The dashboard currently connects to the **local** Admin API and Control Plane:
- Admin API: `http://localhost:8083`
- Control Plane: `http://localhost:3002`

These are separate from the GPU node's Redis where the worker is running.

**What this means:**
- ✅ Dashboard loads and displays correctly
- ✅ Can view job interface
- ⚠️ Jobs submitted via dashboard won't reach the GPU worker (different Redis instance)
- ✅ Use the helper script instead to submit jobs to GPU node

---

## Integration Options

To connect the dashboard to the GPU node's worker:

### Option 1: Redis Bridge (Recommended for Testing)

Create a script that forwards jobs from local Redis to GPU node Redis:

```bash
# Watch local Redis queue and forward to GPU node
while true; do
  JOB_ID=$(redis-cli -h localhost -p 6379 BLPOP queue:black-forest-labs/FLUX.1-dev 1 | tail -1)
  if [ -n "$JOB_ID" ]; then
    JOB_DATA=$(redis-cli -h localhost -p 6379 GET "job:$JOB_ID")
    ssh -p 20034 user@147.185.41.134 \
      "sudo docker exec vyvo-redis redis-cli SET 'job:$JOB_ID' '$JOB_DATA' && \
       sudo docker exec vyvo-redis redis-cli RPUSH 'queue:black-forest-labs/FLUX.1-dev' '$JOB_ID'"
  fi
done
```

### Option 2: Control Plane Configuration

Update the Control Plane to submit jobs to the GPU node's Redis:

1. Edit Control Plane configuration
2. Set `REDIS_URL=redis://147.185.41.134:6379`
3. Ensure network access from local machine to GPU node port 6379
4. Restart Control Plane

### Option 3: VPN/Tunnel (Production)

Set up a VPN or SSH tunnel to access the GPU node's Redis:

```bash
# SSH tunnel
ssh -L 6379:localhost:6379 -p 20034 user@147.185.41.134 -N &

# Then configure Control Plane to use localhost:6379
```

---

## Testing the Complete Flow

### 1. Start the Dashboard
```bash
cd /Users/hakankozakli/Server/vyvo/compute/web
pnpm dev --host --port 5173
```

### 2. Open in Browser
Navigate to: http://localhost:5173/app/admin/jobs

### 3. Submit a Test Job
Use the helper script:
```bash
cd /Users/hakankozakli/Server/vyvo/compute/backend/deploy
./submit-job-to-gpu-node.sh --prompt "A test image" --steps 12
```

### 4. Check Worker Logs
```bash
ssh -p 20034 user@147.185.41.134 'sudo docker logs -f vyvo-flux-runner'
```

### 5. View Generated Image
The script will output the image URL:
```
http://147.185.41.134:20418/generated-images/stub/...png
```

---

## Monitoring & Management

### Check Worker Status
```bash
ssh -p 20034 user@147.185.41.134

# Container status
sudo docker ps | grep flux

# Recent logs
sudo docker logs --tail 50 vyvo-flux-runner

# Follow logs
sudo docker logs -f vyvo-flux-runner
```

### Check Queue
```bash
# Queue length
sudo docker exec vyvo-redis redis-cli LLEN "queue:black-forest-labs/FLUX.1-dev"

# List pending jobs
sudo docker exec vyvo-redis redis-cli LRANGE "queue:black-forest-labs/FLUX.1-dev" 0 -1

# Check specific job
sudo docker exec vyvo-redis redis-cli GET "job:<job-id>" | jq '.'
```

### Restart Worker
```bash
ssh -p 20034 user@147.185.41.134
cd /opt/vyvo
sudo docker compose restart flux-runner
```

---

## Environment Configuration

### Dashboard (.env.local)
```env
VITE_ADMIN_API_BASE_URL=http://localhost:8083
VITE_CONTROL_PLANE_URL=http://localhost:3002
```

### Worker (/etc/vyvo/flux.env)
```env
REDIS_URL=redis://redis:6379
VYVO_MODEL_ID=black-forest-labs/FLUX.1-dev
VYVO_NODE_ID=c98bc605-34aa-4a5f-be52-2c6156635ce9
WORKER_MODEL_ID=black-forest-labs/FLUX.1-dev
# ... (other settings)
```

---

## Common Tasks

### Submit Multiple Jobs
```bash
for prompt in "A sunset" "A mountain" "A forest"; do
  ./submit-job-to-gpu-node.sh --prompt "$prompt"
done
```

### Change Model Parameters
```bash
./submit-job-to-gpu-node.sh \
  --prompt "High quality photorealistic image" \
  --steps 20 \
  --guidance 7.5 \
  --width 1024 \
  --height 1024
```

### Use Custom Seed
```bash
./submit-job-to-gpu-node.sh \
  --prompt "Consistent image" \
  --seed 42
```

---

## Troubleshooting

### Dashboard Shows "Connection Refused"
- Dashboard is trying to connect to remote API
- Solution: Ensure `.env.local` points to `localhost:8083`

### Jobs Don't Process
- Check worker logs: `sudo docker logs vyvo-flux-runner`
- Verify queue: `sudo docker exec vyvo-redis redis-cli LLEN "queue:..."`
- Check GPU: `nvidia-smi`

### Script Fails with SSH Error
- Verify SSH key exists: `ls /tmp/vyvo_gpu_key`
- Test connection: `ssh -i /tmp/vyvo_gpu_key -p 20034 user@147.185.41.134`

---

## Next Steps

1. **Enable Real FLUX Model**
   ```bash
   # Edit /etc/vyvo/flux.env on GPU node
   FLUX_ENABLE_DIFFUSERS=1

   # Restart worker
   cd /opt/vyvo && sudo docker compose restart flux-runner
   ```

2. **Set up Redis Bridge** (if needed)
   - Create forwarding script
   - Connect local dashboard to GPU worker

3. **Production Deployment**
   - Set up systemd service
   - Configure monitoring
   - Enable automatic restarts

---

## Files Reference

- `submit-job-to-gpu-node.sh` - Helper script for job submission
- `COMPLETE_SETUP_SUMMARY.md` - Full system documentation
- `QUICK_START.md` - Quick reference guide
- `GPU_NODE_SETUP.md` - Detailed setup instructions

---

**Dashboard**: http://localhost:5173/app/admin/jobs
**Helper Script**: `./submit-job-to-gpu-node.sh --help`
**GPU Node**: ssh -p 20034 user@147.185.41.134
