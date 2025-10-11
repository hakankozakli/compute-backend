# Control Plane Service

The control plane exposes a REST API for provisioning GPU runner hosts. It is served by `cmd/control-plane` on port `3001` by default.

## Running locally

```bash
cd backend
GOCACHE=$(pwd)/.gocache CGO_ENABLED=0 go build ./cmd/control-plane
CONTROL_PLANE_ADDR=:3001 \
CONTROL_PLANE_DATA=./data/control-plane/nodes.json \
CONTROL_PLANE_SETUP_SCRIPT=./scripts/dev/setup_runner_host.sh \
./control-plane
```

> **Note:** `go mod tidy` and Docker builds require Internet access to download Go modules. Run these commands from a connected environment if the sandbox blocks outbound network calls.

## API

`POST /api/nodes`
: Register a GPU node. Body:

```json
{
  "name": "gpu-node-01",
  "gpuType": "NVIDIA H100",
  "ipAddress": "10.0.0.42",
  "sshUsername": "root",
  "sshPassword": "•••••",
  "sshPort": 22,
  "models": ["qwen-image"],
  "hfToken": "hf_...",
  "torchDtype": "float16"
}
```

`GET /api/nodes`
: List nodes and their statuses.

`GET /api/nodes/{id}`
: Fetch node details plus recent provisioning events.

`POST /api/nodes/{id}/retry`
: Trigger a new provisioning run.

## Provisioning Flow

1. Uploads `scripts/dev/setup_runner_host.sh` to `/tmp/vyvo/setup_runner_host.sh` and executes it.
2. Writes `/etc/vyvo/qwen.env` with the selected model configuration (e.g., `QWEN_DIFFUSERS_MODEL=Qwen/Qwen-Image`).
3. Restarts `vyvo-runners` and verifies it reaches the `active` state.
4. Emits events and transitions the node status (`PENDING → PROVISIONING → READY/ERROR`).

Provisioning logs and status transitions are available through the `/api/nodes/{id}/events` endpoint.

## Recognised models

The control plane now renders a Docker Compose stack for each assigned model. When you edit a node from the admin panel and toggle the **Models** list, the backend regenerates the stack and redeploys the runner automatically.

- `black-forest-labs/FLUX.1-dev` – provisions the Flux diffusers runner (`ghcr.io/vyvo/runner-flux:latest`) with GPU reservations, `FLUX_ENABLE_DIFFUSERS=1`, and Hugging Face credentials pulled from the node record.
- `qwen-image` – runs the `Qwen/Qwen-Image` diffusers checkpoint alongside a MinIO sidecar for artifact storage.
- `qwen-image-dashscope` – configures DashScope as a fallback backend and shares the same runner container.

Set `RUNNER_CALLBACK_URL` (and optionally `RUNNER_CALLBACK_TOKEN`) in the control plane environment if your remote runners should stream status updates back to the orchestrator. The generated stack always includes `/etc/vyvo/runner.env` with `REDIS_URL`, `VYVO_NODE_ID`, and any secrets you captured when registering the node, so no manual edits are required on the host.

## Docker Compose

`backend/deploy/docker-compose.yml` now includes a `control_plane` service. Data is stored in `control_plane_data:/var/lib/vyvo` within the Compose project.

```
docker compose -f deploy/docker-compose.yml up control_plane
```

The service ships the latest `setup_runner_host.sh` alongside the binary and exposes port `3001`.
