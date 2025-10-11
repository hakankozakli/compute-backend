# runner-flux

Vyvo runner for the `black-forest-labs/FLUX.1-dev` text-to-image model. The service exposes a FastAPI
HTTP endpoint and a Redis-backed queue worker so it can be controlled by the control-plane/orchestrator.

## Backends

The runner supports two modes:

1. **Diffusers backend (default)** – loads the HuggingFace `FLUX.1-dev` pipeline directly on the GPU. Enable
   by setting `FLUX_ENABLE_DIFFUSERS=1` and providing the appropriate CUDA/HF environment variables.
2. **Stub backend** – returns a deterministic placeholder PNG for environments without GPU/diffusers. Useful
   for smoke tests during development. Enabled when `FLUX_ENABLE_DIFFUSERS` is unset or `0`.

## Environment Variables

| Variable | Description |
| --- | --- |
| `REDIS_URL` | Redis connection string (e.g. `redis://redis:6379`). |
| `VYVO_MODEL_ID` | Model identifier to poll (e.g. `black-forest-labs/FLUX.1-dev`). |
| `VYVO_NODE_ID` | Optional node ID. When set, worker polls `queue:<node_id>:<model_id>` before the global queue. |
| `FLUX_ENABLE_DIFFUSERS` | Set to `1` to activate the diffusers backend. |
| `HF_TOKEN` | HuggingFace token if the model is gated. |
| `FLUX_DEVICE` | Torch device (default `cuda`). |
| `FLUX_TORCH_DTYPE` | Torch dtype (`bfloat16` default). |
| `RUNNER_CALLBACK_URL` | Optional admin/control-plane callback base URL for status events. |

## Local Development

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements-dev.txt
export REDIS_URL=redis://localhost:6379
export VYVO_MODEL_ID=black-forest-labs/FLUX.1-dev
export FLUX_ENABLE_DIFFUSERS=0  # stub mode by default
uvicorn app.main:app --reload --port 9010
python app/worker.py
```

To exercise the diffusers backend you must install PyTorch with CUDA and the diffusers package. The Dockerfile
performs these steps automatically when building for production.

## Queue Worker

`app/worker.py` polls Redis and executes jobs posted by the control-plane. The worker honors the
parameter schema described in `runner-manifest.yaml` (steps, guidance, width, height, seed, etc.).

## Docker Build

```bash
docker build -t ghcr.io/<org>/runner-flux:latest backend/runner-flux
```

Set `HF_TOKEN`, `FLUX_ENABLE_DIFFUSERS=1`, `VYVO_MODEL_ID`, and `REDIS_URL` in the container environment before running.

## Runner Manifest

`runner-manifest.yaml` describes the parameters surfaced in the admin dashboard. Import this YAML into
`model_versions.runner_manifest` so the UI can render a dynamic form for Flux jobs.

