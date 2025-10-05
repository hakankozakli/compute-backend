# runner-qwen

Vyvo's Qwen image runner packaged as a FastAPI microservice. It mirrors the HTTP contract used by the Vyvo compute gateway and is designed to run either directly via Docker Compose or as a container inside Kubernetes. The current implementation returns stubbed artifacts so the gateway/orchestrator integration can be exercised before wiring in real inference code.

## Features

- `/healthz` liveness probe
- `/invoke` endpoint accepting fal.ai-compatible payloads
- Structured logging via Loguru
- Ready for GPU integration (replace the stubbed response section with actual model inference)
- Container image built with `uvicorn` and Python 3.11

## Local Development

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e .[test]
pytest
uvicorn app.main:app --reload --port 9001
```

Smoke test with curl:

```bash
curl -X POST http://localhost:9001/invoke \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"generate a futuristic skyline","image_count":2}'
```

## Docker

```bash
docker build -t ghcr.io/<org>/runner-qwen:latest .
docker run --rm -p 9001:9001 ghcr.io/<org>/runner-qwen:latest
```

Verify:

```bash
curl http://localhost:9001/healthz
```

## Publishing to GHCR

```bash
echo "<PAT>" | docker login ghcr.io -u <github-user> --password-stdin
docker buildx build --platform linux/amd64 \
  -t ghcr.io/<org>/runner-qwen:latest --push .
```

The included GitHub Actions workflow (`.github/workflows/runner-qwen-ci.yml`) builds and pushes `ghcr.io/<owner>/runner-qwen:latest` on pushes to `main`. Ensure `Actions > General > Read and write permissions` is enabled for the GITHUB_TOKEN.

## Docker Compose snippet

```yaml
services:
  qwen-image:
    image: ghcr.io/<org>/runner-qwen:latest
    restart: unless-stopped
    runtime: nvidia
    environment:
      - NVIDIA_VISIBLE_DEVICES=all
      - VYVO_MODEL_ID=fal-ai/fast-sdxl
    ports:
      - "9001:9001"
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
      containers:
        - name: runner
          image: ghcr.io/<org>/runner-qwen:latest
          env:
            - name: NVIDIA_VISIBLE_DEVICES
              value: all
          ports:
            - containerPort: 9001
```

## Next Steps

- Integrate actual Qwen image inference code or Triton/TensorRT pipeline.
- Emit streaming/chunked responses as required by the Vyvo orchestrator.
- Wire observability (OTel traces, metrics).
- Add structured metrics/log forwarding for distributed tracing.
