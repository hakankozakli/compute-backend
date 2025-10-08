# Vyvo Compute Platform – Phase 1 Audit

_Last updated: 2025-10-08_

## 1. Services & Responsibilities

### Gateway (Go – `cmd/gateway`)
- Terminates queue/sync APIs compatible with fal.ai semantics.
- Talks to orchestrator over HTTP (`pkg/orchestrator` client).
- Exposes health check on `/healthz`.
- Reads config from `configs/config.yaml` / env (`pkg/config`).

### Orchestrator (Rust – `orchestrator/src`)
- HTTP API for internal job lifecycle (`/v1/internal/jobs`, `/stream`, `/sync`).
- In-memory job map + Redis for queue fan-out (`state.rs`).
- SSE streaming for status updates.

### Control Plane (Go – `cmd/control-plane`)
- Stores node metadata in JSON (`data/control-plane/nodes.json`).
- Provisioner pushes scripts/ENV to GPU hosts over SSH using `pkg/controlplane`.
- Exposes REST endpoints for nodes (`/api/nodes`, `/events`, `/retry`).

### Admin REST (Go – `cmd/admin-rest`)
- Queue/job monitoring, Redis management.
- Now integrates with Postgres-backed model registry (when `ADMIN_REST_DATABASE_URL` set).

### Builder Service (Go – `cmd/builder`)
- Accepts runner build requests, streams build logs, and persists metadata to Postgres when configured.
- Optional callback to model registry after successful builds.
- Accessed directly by the admin UI and CLI helper (`scripts/build-runner.sh`).

### Workflow Engine (Go – `cmd/workflow-engine`)
- Skeleton service with health endpoint. DAG execution not implemented yet.

### Runners (Python)
- Example runner `runner-qwen` (FastAPI + Redis worker). Template scaffold under `runners/template`.
- Queue worker (`app/worker.py`) polls Redis lists per model.

### Frontend (React – `web`)
- Admin portal: nodes, models (legacy), model registry (new), workflows (placeholders).
- Uses TanStack Router + React Query; expects API base URL `VITE_API_BASE_URL`.

## 2. Data & Control Flows (Current)

1. Gateway accepts client requests -> forwards to orchestrator.
2. Orchestrator creates job record (UUID) and enqueues to Redis list `queue:<model>`.
3. Runner worker pops job, runs inference/stub, updates Redis job hash. TODO: notify orchestrator for completion.
4. Admin REST reads Redis keys to show queue/job info.
5. Control plane provisions hosts via SSH (uploads env files, restarts systemd service).
6. Artifact storage: MinIO container is available; runners optionally upload artifacts there.

## 3. Storage & Infrastructure

- Redis (queue + job state) – single instance, docker-compose / k8s manifest.
- Postgres – used by admin registry; not yet by control plane or orchestrator.
- MinIO – S3-compatible object store for artifacts.
- Docker infra: compose files under `backend/deploy/`, Kubernetes manifests in `deploy/k8s`.

## 4. Observability

- Gateway uses Chi middleware logging.
- Orchestrator has tracing (Tokio + tracing).
- Docker compose includes OTEL collector; metrics wiring minimal.
- No centralized dashboard yet.

## 5. Identified Gaps for “Fully Controllable Admin UI”

| Area | Current State | Gap |
| ---- | ------------- | --- |
| Model registry | Admin REST + React page listing models | Need builder integration, version promotion automation, validations |
| Runner image builds | Manual (`Makefile` per runner) | No build service / UI trigger; no log streaming |
| Node provisioning | Control plane CLI/REST to register nodes | UI lacks full controls; store is JSON not Postgres; no capacity metrics |
| Queue management | Basic list/purge endpoints | Lacks scheduling policies, autoscaling hooks, metrics |
| Workflow engine | Stub service | Needs DAG storage, executor logic, UI builder |
| Artifacts | MinIO accessible but not enforced | Need standardized upload helper + links in orchestrator responses |
| Auth & RBAC | Basic portal auth store | Need fine-grained permissions for build/deploy actions |

This audit forms the baseline for Phase 2 implementation.
