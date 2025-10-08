# Phase 2 – Backend Foundations Plan

This document breaks down the concrete backend work needed to make the admin UI fully control
model builds, deployments, and workflows. Each subsection maps to services that already exist
so implementation can be incremental.

## 1. Runner Build Service

**Goal:** Accept runner build requests from the admin UI, build container images, push to registry,
update the model registry, and surface logs.

### API Surface (Go service, `cmd/builder`)
- `POST /api/builds`
  - Body: `{ "template": "diffusion", "runner_name": "ovi", "model_id": "chetwinlow1/Ovi", "version": "v0.1.0", "repo": "https://...", "weights_uri": "s3://...", "env": {...} }`
  - Response: `{ "build_id": "uuid", "status": "queued" }`
- `GET /api/builds/:id`
  - Returns status + timestamps + logs slice.
- `GET /api/builds/:id/logs`
  - Server-sent events / chunked plain text stream.

### Implementation Notes
- Use lightweight queue (Redis Stream) to enqueue build jobs.
- Build worker can shell out to BuildKit (`docker buildx bake`) or trigger GitHub Actions via API.
- On success, call Admin REST `/api/models` to create/patch version entry, mark `runner_image`.
- Persist build metadata in Postgres (reuse registry DB) – table `runner_builds`.

## 2. Control Plane Extensions

**Goal:** Manage node pools and model assignments entirely via API (and UI).

### Required Additions (`cmd/control-plane`, `pkg/controlplane`)
- Persist store in Postgres (tables `nodes`, `node_events`, `node_capabilities`).
- `POST /api/nodes/:id/assign` body `{ "model_id": "chetwinlow1/Ovi", "version_id": "...", "weights_uri": "..." }`.
  - Control plane writes `/etc/vyvo/<model>.env`, triggers systemd reload.
- `GET /api/nodes/:id/metrics` from agent heartbeat (expose GPU mem, utilization, running jobs).
- `POST /api/node-pools/scale` to request infra scaler (future stub calling Terraform/auto-scaler).

### Agents / Heartbeats
- Runner hosts run a lightweight agent (could extend queue worker) that POSTs health to control plane.
- Store last heartbeat and show status in UI.

## 3. Orchestrator & Queue Integration

**Goal:** Use the model registry to drive scheduling; ensure artifacts are uploaded to MinIO and links recorded.

### Work Items (Rust orchestrator + Go admin)
- Load model registry cache on startup; refresh via Admin REST `/api/models`.
- When dispatching job, select node based on registry metadata + control plane capacity.
- Receive runner completion callbacks (HTTP/gRPC) containing `artifacts[]`. Push to MinIO helper if not already uploaded.
- Update job record in Postgres (new `jobs` table, eventually replacing in-memory store).
- Stream job + artifact metadata back to clients (Gateway SSE).

### Queue Improvements
- Replace Redis list with Redis Streams (per model consumer groups) to allow multiple orchestrator/worker instances.
- Introduce priority / fairness by weighting streams or using sorted sets for scheduling hints.

## 4. Workflow Engine

**Goal:** Execute DAGs defined in registry and expose results.

### Steps
- Define `workflows` table (id, name, definition JSON, status).
- Extend workflow engine service to:
  - Load workflow definition.
  - Submit root job(s) via orchestrator.
  - Listen for completion events (Redis pub/sub or Postgres NOTIFY) and trigger dependent jobs.
  - Emit workflow status events accessible via Admin UI.

## 5. Artifact Storage Convention

**Goal:** Every runner uploads to MinIO and orchestrator persists artifact pointers.

### Actions
- Add helper lib (`pkg/storage` in Go, Python module for runners) that wraps MinIO upload (bucket per modality, TTL).
- Standardize artifact metadata structure: `{ type, url, size_bytes, mime_type, expires_at }`.
- Update runner template to use helper by default; enforce via CI check.

## 6. Observability & Metrics

- Export Prometheus metrics from orchestrator, control-plane, builder service (queue depth, job latency, node capacity).
- Publish OTLP traces to collector (already in docker-compose). Add dashboards (Grafana stack – future phase but plan now).

## 7. Security Considerations

- Gate new endpoints with API keys / JWT scopes (admin only).
- Store secrets (Docker registry creds, HF tokens) in Vault or env-managed service; control-plane retrieves short-lived creds per node.

---

With this blueprint approved, we can start implementing services and extending existing ones in Phase 2.
