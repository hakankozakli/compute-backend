# Runner Template

This directory provides a lightweight starting point for building a new Vyvo compute runner.
Use it when you need to serve a model behind the internal orchestrator queue and expose
FastAPI-compatible synchronous endpoints for health checks or manual invocation.

## Quick Start

1. Copy the template into a new folder under `backend/`:

   ```bash
   ./scripts/new-runner.sh my-new-runner chetwinlow1/Ovi
   ```

   The script clones this template, renames module imports, and prints the next steps.

2. Install dependencies and run locally:

   ```bash
   cd backend/my-new-runner
   python -m venv .venv
   source .venv/bin/activate
   pip install -r requirements.txt
   uvicorn app.main:app --reload --port 9001
   ```

3. Implement the `load_backend` function in `app/backend_factory.py`. This is where you spin up
   your model, load weights, and return an object with an async `generate` method.

4. Update `runner-config.yaml` with GPU requirements, environment variables, and weight locations.

5. Build and push the container with `make docker-build` (or let the automated build service handle it).

## Files

| File | Purpose |
| ---- | ------- |
| `app/main.py` | FastAPI application wiring health checks and `/invoke` to the backend. |
| `app/backend_factory.py` | Stubs for implementing your model-specific backend. |
| `app/types.py` | Shared Pydantic models used by the template. |
| `worker.py` | Redis queue worker that cooperates with the orchestrator. |
| `requirements.txt` | Minimal dependency set (FastAPI, loguru, redis, etc.). |
| `Dockerfile` | Multi-stage Docker build optimized for GPU runners. |
| `Makefile` | Helper targets for lint/test/build. |
| `runner-config.yaml` | Sample config used by the control plane to preload weights/env vars. |

## Conventions

- Export structured logs with a `request_id` so orchestrator traces stay linked.
- Keep `/invoke` synchronous but lightweight—queue worker handles heavy jobs.
- Use MinIO (S3-compatible) helper if you need to persist artifacts; otherwise stream results back.
- Add a `tests/` folder with smoke tests; see `backend/runner-qwen/tests` for examples.

Once your runner works locally, register it in the model registry and assign it to nodes via the control plane.
