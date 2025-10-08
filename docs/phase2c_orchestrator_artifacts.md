# Phase 2C – Orchestrator & Artifact Integration Plan

_Last updated: 2025-10-09_

## 1. Objectives
- Orchestrator dispatches jobs using model metadata stored in Postgres (registry).
- Jobs persist in Postgres so state survives orchestrator restarts.
- Runner services upload artifacts to MinIO and report completion back to orchestrator.
- Gateway/Admin endpoints return artifact links and job history matching fal.ai semantics.

## 2. Data Flow Overview
1. Admin queues job → Gateway → Orchestrator.
2. Orchestrator stores job record in Postgres (`jobs` table) and enqueues work.
3. Runner pulls job, executes inference, uploads artifact(s) to MinIO, and posts completion event.
4. Orchestrator updates job record (status/artifacts) and pushes status via SSE/Webhook.
5. Gateway/Admin UI surfaces job status and artifact URLs.

## 3. Schema Additions (Postgres)
### `jobs`
- `id UUID PRIMARY KEY`
- `model_id TEXT`
- `version_id TEXT`
- `node_id TEXT`
- `status TEXT`
- `queue_position INT`
- `payload JSONB`
- `artifacts JSONB`
- `created_at TIMESTAMPTZ`
- `updated_at TIMESTAMPTZ`

### `job_events`
- `id BIGSERIAL`
- `job_id UUID REFERENCES jobs(id) ON DELETE CASCADE`
- `status TEXT`
- `message TEXT`
- `created_at TIMESTAMPTZ`

## 4. Orchestrator Updates
- Introduce Postgres connection (config `ORCHESTRATOR_DATABASE_URL`).
- On job submission: insert row in `jobs`, schedule in Redis Streams.
- Model dispatch logic:
  - Query model registry (Admin REST) on startup; cache in memory with TTL.
  - When dispatching, pick node assignment from control-plane API.
- SSE stream pulls updates from job table/events.

## 5. Runner Callback API (new service endpoint)
- `POST /v1/internal/runners/:job_id/events`
  - Body: `{ status, logs?, artifacts? }`.
  - Runner uploads artifacts to MinIO using signed URL helper or shared credentials.
  - Orchestrator stores artifact metadata (`[{ type, url, size_bytes, mime_type }]`).

## 6. Gateway/Admin Adjustments
- Gateway `GET /requests/:id` returns artifacts array when job complete.
- Admin REST exposes job history (`/api/jobs`) to inspect job logs/artifacts.

## 7. Runner Template Changes
- Update template (`runners/template`) to include helper for MinIO upload and callback to orchestrator.
- Provide env vars for orchestrator callback URL and credentials (`RUNNER_CALLBACK_URL`, token).

## 8. Security Considerations
- Issue short-lived tokens for runner callbacks to prevent unauthorized updates.
- Ensure MinIO credentials scoped per assignment/model.

## 9. Implementation Steps
1. Orchestrator: add Postgres store + registry cache.
2. Define job tables/migrations (`backend/data/migrations/xxxx_job_store.sql`).
3. Build runner callback endpoint + update SSE stream.
4. Update gateway/admin to use persisted job store.
5. Enhance runner template + builder pipeline to supply necessary env.
6. Update docs/admin UI to show artifact metadata.

## 10. Testing
- Local integration test: queue job, stub runner posts callback, verify SSE + admin job log show artifacts.
- Simulate failure path (runner posts `status=ERROR`) to confirm job store updates correctly.

