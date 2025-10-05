# Vyvo Compute Platform – fal.ai-Compatible Architecture

## 1. Objectives
- Mirror fal.ai HTTP/WebSocket queue, sync, workflow, and webhook semantics without client changes.
- Deliver sub-300 ms TTFB for streaming workloads and synchronous responses dominated by model runtime.
- Maintain containerized isolation for gateway, orchestration, runners, and supporting systems.
- Provide fast adaptability for new model IDs, subpaths, and workflows.

## 2. High-Level Architecture
```
Clients ─▶ Edge (CDN/WAF) ─▶ API Gateway (queue|sync|ws)
                                   │
                                   ▼
                            Orchestrator Service ──▶ Redis Streams (state)
                                   │                      │
         ┌──────────────────────────┴────┐                ▼
         ▼                               ▼          Postgres (metadata)
   Runner Pool (GPU pods)         Workflow Engine ─▶ Object Store (artifacts)
         │                               │                │
         ▼                               ▼                ▼
   Telemetry Collectors ◀───────────────┴──────────── Webhook Dispatcher
```

- **API Gateway** handles routing, auth, SSE/WS streaming, and compatibility response shaping.
- **Orchestrator** owns job lifecycle, persistence, rate limits, and dispatching runners.
- **Runner Pool** executes model inference via gRPC (binary streaming) or HTTP chunked transfer.
- **Workflow Engine** coordinates multi-node DAGs and emits workflow events.
- **Storage** tier (Postgres + Redis + S3-compatible object store) retains metadata, payloads, signed URLs.
- **Observability Stack** (Tempo/OTel collector + Prometheus + Loki) ingests traces/metrics/logs with shared `request_id`.

## 3. Network Entry Points
- `queue.<vyvo-domain>`: Async submissions via HTTPS (HTTP/1.1 keep-alive + gzip).
- `<vyvo-domain>`: Low-latency synchronous path; shares code with queue handler but bypasses queue persistence.
- `ws.<vyvo-domain>`: WebSocket entrypoint built on the same routing table; uses HTTP/2 upgrade.
- `rest.<vyvo-domain>`: Admin and metadata APIs; rate-limited separately.

TLS termination occurs at an Envoy-based edge proxy colocated with the gateway deployment for H/2, ALPN, and zero-copy forwarding.

## 4. Component Responsibilities
### 4.1 Gateway Service
- Written in Go (Chi + `net/http` + Gorilla WS) for low-latency streaming and strong concurrency.
- Normalizes incoming requests and enriches them with `request_id`, `user_id`, and `trace_id` headers.
- Maintains compatibility table of fal model IDs → internal runner targets.
- Implements SSE and WS streaming, pushing incremental updates from orchestrator via Redis Pub/Sub.
- Applies rate limits (token bucket via Redis) and scope checks before admitting requests.

### 4.2 Orchestrator Service
- Written in Rust (`axum` + Tokio) for predictable latency and memory safety around high-QPS queues.
- Persists request metadata, state transitions, and log frames into Postgres.
- Uses Redis Streams for queueing; consumer groups per model to guarantee ordering.
- Dispatches jobs to runner pools via gRPC; supports cancellation and retries with exponential backoff.
- Generates structured status events consumed by gateway (HTTP status/SSE) and webhook dispatcher.

### 4.3 Runner Pools
- Python microservices packaged per model family (e.g., `runner-qwen-image`, `runner-wan-video`).
- Use FastAPI + Uvicorn with gRPC bindings via `grpcio` to communicate with orchestrator.
- Maintain warm GPU contexts (PyTorch/TensorRT) to hit TTFB targets; auto-scale via KEDA against queue depth.
- Emit progressive outputs (diffusion, frames) through streaming RPC back to orchestrator.

### 4.4 Workflow Engine
- Lightweight DAG executor (Go) subscribing to orchestrator events.
- Loads workflow specs (YAML) and orchestrates sequential/parallel execution.
- Emits workflow SSE/WS events matching fal types (`submit`, `completion`, `output`, `error`).

### 4.5 Storage Services
- Postgres: request metadata, logs, workflow definitions, API keys, rate limit counters.
- Redis: hot path caching (status snapshots), idempotency records, distributed locks.
- MinIO (S3-compatible): payload storage with signed URL issuance. TTL jobs prune objects beyond 30 days.

### 4.6 Webhook Dispatcher
- Event-driven worker triggered by orchestrator updates.
- Signs payloads using ED25519 keys from Hashicorp Vault; publishes JWKS to Admin REST.
- Handles retries with exponential backoff and DLQ (Redis) for manual inspection.

### 4.7 Admin REST
- Go service sharing gateway Auth middleware but restricted to ADMIN-scoped keys.
- Exposes `delete_io`, log search, JWKS, and request metadata endpoints.

### 4.8 Observability Stack
- OpenTelemetry instrumentation across services; request_id propagated via headers.
- Tempo/Jaeger for tracing, Prometheus for metrics, Loki for structured logs.
- Gateway exposes per-request metrics in SSE streams (mirroring fal) when `logs=1`.

## 5. Data & Control Flows
1. **Queue submission**: Gateway validates → persist metadata → enqueue Redis Stream → respond 201 with canonical URLs.
2. **Status polling**: Gateway fetches state snapshot (Redis + Postgres fallback) → renders fal-compatible JSON; SSE uses pub/sub to push updates.
3. **Dispatch**: Orchestrator claims job → selects runner pod via weighted scheduler → issues gRPC start.
4. **Streaming**: Runner pushes partial outputs to orchestrator → orchestrator relays via pub/sub to gateway, websockets, SSE.
5. **Completion**: Orchestrator marks Postgres state, triggers webhook dispatcher, generates signed URLs.
6. **Cancellation**: Gateway marks job as cancel-requested; orchestrator attempts to signal runner; responds with proper fal status body.
7. **Workflow**: Workflow engine listens for root request → submits downstream jobs; collects completions and emits workflow stream events.

## 6. Compatibility Guarantees
- Canonical serializer module ensures response bodies, status codes, and error shapes match fal contracts.
- Model registry table: columns `{external_model_id, subpath, internal_service, schema_version, default_params}`.
- Request router uses registry to build `response_url/status_url` ensuring subpaths omitted per fal semantics.
- Error mapping layer: unify validation errors (Pydantic-style `detail` array) with unique codes; orchestrator enforces charge semantics.
- Idempotency support storing request hash + response pointer keyed by `Idempotency-Key`.

## 7. Latency & Performance Tactics
- Warm runner pools sized via predictive autoscaling models; `min_ready_count` per model.
- Preinitialized model weights pinned in GPU memory; avoid cold starts via start-up scripts executed during container readiness probes.
- HTTP keep-alive and connection pooling at the gateway; use `SO_REUSEPORT` to reduce context switches.
- Redis cluster with `M6g` class instances (ARM) to meet <2 ms ops; orchestrator caches status in memory for synchronous reads.
- Use gRPC streaming for binary media; fallback to chunked HTTP for SSE textual updates.
- Deploy CDN (CloudFront/Fastly) with signed URLs for media artifacts.

## 8. Security & Auth
- API keys hashed (bcrypt) in Postgres; scope field enumerates `DEFAULT` vs `ADMIN` privileges.
- WS authentication supports query token minted by gateway (JWT RS256, 60-second TTL).
- Webhook signatures produced by dispatcher; JWKS hosted at `https://rest.<domain>/.well-known/jwks.json` with 24-hour cache headers.
- Per-service RBAC via Kubernetes service accounts; runners access object storage through short-lived credentials.

## 9. Deployment & Containerization
- Each service packaged with multi-arch Dockerfiles; pinned base images for reproducibility.
- `docker-compose` for local dev (mock GPU via CPU fallback); Kubernetes (EKS/GKE) for prod with namespace isolation.
- CI pipeline (GitHub Actions) builds images, runs conformance tests, pushes to ECR/GCR.

## 10. Testing Strategy
- Conformance suite replicating fal interactions using `pytest + httpx + websockets`.
- Latency tests using `k6` scripts hitting queue/sync/WS endpoints.
- Workflow simulations verifying event order, SSE and WS parity.
- Chaos experiments (fault injection) to validate retries and failure semantics.

## 11. Implementation Milestones
- Mirror agents.md rollout plan with engineering tasks:
  1. Gateway skeleton + auth + request IDs.
  2. Queue endpoints + SSE + Qwen runner.
  3. Sync path + WS streaming + CDN integration.
  4. Webhooks + JWKS + delete IO.
  5. WAN 2.2 runner + large artifact streaming + storage TTL jobs.
  6. Workflow engine + two-node pipeline.
  7. Error catalog + conformance suite + rate limits + idempotency.
  8. Load/latency optimization + observability polish.

## 12. Open Questions
- Upload helper endpoints vs. relying solely on client-managed storage; decision deferred.
- Quota granularity (per-model vs. global) tied to upcoming billing integration.
- Client SDK parity to be considered post HTTP conformance.
