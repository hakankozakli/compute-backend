# runner-qwen

Vyvo's Qwen image runner packaged as a FastAPI microservice. It mirrors the HTTP contract used by the Vyvo compute gateway and supports three backends:

1. **Diffusers backend (default when `QWEN_DIFFUSERS_MODEL` is set)** – runs HuggingFace models such as `Qwen/Qwen-Image` directly on the GPU node.
2. **DashScope backend** – proxies Alibaba's hosted Qwen API when `DASHSCOPE_API_KEY` is configured.
3. **Stub backend** – returns deterministic placeholders for integration tests when no backend is configured.

## Environment Variables

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `QWEN_DIFFUSERS_MODEL` | — | HuggingFace model id (e.g., `Qwen/Qwen-Image`). Enables diffusers backend when set. |
| `HF_TOKEN` | — | HuggingFace access token for private/limited models. |
| `QWEN_DEVICE` | `cuda` | Device passed to diffusers (`cuda`, `cuda:0`, `cpu`). |
| `QWEN_TORCH_DTYPE` | `float16` | Torch dtype (`float16`, `bfloat16`, `float32`). |
| `QWEN_TRUST_REMOTE_CODE` | `1` | Set to `0` to disable `trust_remote_code`. Required for `Qwen/Qwen-Image`. |
| `QWEN_ENABLE_XFORMERS` | `1` | Toggle xFormers attention (requires xformers build). |
| `QWEN_ENABLE_TF32` | `1` | Allow TF32 matmul when CUDA supports it. |
| `DASHSCOPE_API_KEY` | — | DashScope key for hosted inference. |
| `QWEN_IMAGE_MODEL` | `wanx2.1` | DashScope model id. |
| `LOG_LEVEL` | `INFO` | Runner log level. |

The diffusers backend returns base64-encoded PNG data URLs, so no external storage is required. Replace with object store URLs if needed.

## Local Development

```bash
python -m venv .venv
source .venv/bin/activate
TORCH_INDEX=https://download.pytorch.org/whl/cu121 TORCH_VERSION=2.3.1 make install
export QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image
export HF_TOKEN=<your-hf-token-if-required>
uvicorn app.main:app --reload --port 9001
```

> Tip: To scaffold a new runner, start from the reusable template under `backend/runners/template`
> or run `./scripts/new-runner.sh <name> <model-id>`. It generates the FastAPI app, queue worker,
> Dockerfile, and Makefile so you can focus on model-specific code.

Smoke test:

```bash
curl -X POST http://localhost:9001/invoke \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"generate a futuristic skyline","image_count":1}'
```

Unset `QWEN_DIFFUSERS_MODEL` to fall back to DashScope (if configured) or the stub backend.

## Docker

```bash
echo "<PAT>" | docker login ghcr.io -u <github-user> --password-stdin
docker buildx build --platform linux/amd64 \
  -t ghcr.io/<org>/runner-qwen:latest --push .

docker run --rm --gpus all -p 9001:9001 \
  -e QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image \
  -e HF_TOKEN=<token> \
  ghcr.io/<org>/runner-qwen:latest
```

## Docker Compose snippet

```yaml
services:
  qwen-image:
    image: ghcr.io/<org>/runner-qwen:latest
    restart: unless-stopped
    runtime: nvidia
    env_file:
      - /etc/vyvo/qwen.env
    environment:
      NVIDIA_VISIBLE_DEVICES: all
      VYVO_MODEL_ID: qwen/image
    ports:
      - "9001:9001"
```

Example `/etc/vyvo/qwen.env`:

```
QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image
HF_TOKEN=<your-hf-token>
QWEN_TORCH_DTYPE=float16
QWEN_TRUST_REMOTE_CODE=1
```

## Kubernetes deployment sketch

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: runner-qwen
spec:
  replicas: 1
  selector:
    matchLabels:
      app: runner-qwen
  template:
    metadata:
      labels:
        app: runner-qwen
    spec:
      nodeSelector:
        gpu: nvidia-h100
      containers:
        - name: runner
          image: ghcr.io/<org>/runner-qwen:latest
          resources:
            limits:
              nvidia.com/gpu: 1
          env:
            - name: QWEN_DIFFUSERS_MODEL
              value: Qwen/Qwen-Image
            - name: HF_TOKEN
              valueFrom:
                secretKeyRef:
                  name: huggingface
                  key: token
            - name: NVIDIA_VISIBLE_DEVICES
              value: all
          ports:
            - containerPort: 9001
```

## CI/CD

The GitHub Actions workflow (`.github/workflows/runner-qwen-ci.yml`) installs CPU Torch, runs unit tests (stub backend), and pushes `ghcr.io/<owner>/runner-qwen:latest` on pushes to `main`. Enable “Read and write” permissions for workflow GITHUB_TOKEN.

## Notes

- Pre-download weights with `huggingface-cli download Qwen/Qwen-Image --local-dir /models/Qwen-Image` to avoid cold starts.
- Tune `QWEN_TORCH_DTYPE` and enable TF32/xFormers for H100 throughput.
- Replace base64 data URLs with signed CDN links if artifact size becomes an issue.
