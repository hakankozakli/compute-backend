# Implementation Plan & Repository Layout

## 1. Technology Stack Selection
- **Gateway / Admin REST**: Go 1.22, Chi router, Gorilla WebSocket, OpenTelemetry SDK, Redis client (go-redis).
- **Orchestrator**: Rust 1.78, Axum, tonic (gRPC), sqlx (Postgres), redis-rs, tracing crate.
- **Workflow Engine**: Go 1.22 leveraging shared libraries with gateway for auth and request ID propagation.
- **Runner Services**: Python 3.11 using FastAPI, gRPC via `grpcio`, PyTorch/TensorRT runtime, NVidia Triton optional for WAN 2.2.
- **Storage**: Postgres 15, Redis 7 cluster mode, MinIO (S3-compatible), optional Elastic for full-text log search.
- **Telemetry**: OpenTelemetry Collector, Prometheus, Tempo, Loki, Grafana dashboarding.
- **Infrastructure**: Docker Compose for local dev; Kubernetes (Helm charts) for prod; ArgoCD continuous delivery.

## 2. Repository Structure
```
backend/
├── cmd/
│   ├── gateway/
│   │   └── main.go
│   ├── orchestrator/
│   │   └── main.rs
│   ├── workflow-engine/
│   │   └── main.go
│   ├── webhook-dispatcher/
│   │   └── main.go
│   └── admin-rest/
│       └── main.go
├── pkg/
│   ├── auth/
│   ├── registry/
│   ├── falcompat/
│   ├── telemetry/
│   └── config/
├── runners/
│   ├── qwen_image/
│   ├── wan_video/
│   └── video_to_audio/
├── workflows/
│   └── specs/
├── configs/
│   ├── helm/
│   ├── docker/
│   └── k8s/
├── deploy/
│   ├── docker-compose.yml
│   └── k8s/
├── docs/
│   ├── system_design.md
│   └── implementation_plan.md
├── scripts/
│   ├── conformance/
│   ├── load_tests/
│   └── dev/
└── tests/
    ├── conformance/
    ├── latency/
    └── integration/
```

- `pkg/falcompat`: shared serializers, error mappers ensuring fal.ai parity.
- `pkg/registry`: data access layer for model registry (Postgres) with caching.
- `configs/helm`: templated charts for gateway, orchestrator, runners, observability.
- `deploy/docker-compose.yml`: local stack with mocks for GPU (CPU-only) and minimal dependencies.

## 3. Environment Configuration
- `.env` (local) storing API keys, Redis/Postgres DSNs; `config/` includes `config.yaml` per service.
- Use Viper (Go) / Figment (Rust) for hierarchical config: env vars override YAML.
- Secrets in prod managed via Kubernetes Secrets + Vault integration.

## 4. Development Workflow
1. Run `make bootstrap` to provision virtualenvs, Go modules, Rust toolchain (via `rustup`).
2. `make up` spins docker-compose stack with gateway, orchestrator, runners, Postgres, Redis, MinIO, telemetry stack.
3. Local testing via `make test-conformance` executing HTTP+WS parity suite.
4. CI pipeline stages: lint (golangci-lint, clippy, black), unit tests, build containers, run conformance, publish images.

## 5. Detailed Roadmap
- **Milestone A (Week 1)**
  - Scaffold gateway (Go) with auth middleware and model registry stub.
  - Implement queue submission → orchestrator stub returning mock responses.
  - Setup Postgres migrations (sqlc/dbmate) and Redis connection pools.
  - Deliver initial docker-compose with gateway, orchestrator, Postgres, Redis.
- **Milestone B (Week 2)**
  - Complete queue lifecycle (status, SSE, cancel, response) and integrate Qwen runner over gRPC.
  - Implement synchronous path sharing orchestrator dispatch (short-circuit path) and ensure streaming semantics.
  - Add webhook dispatcher with signing + JWKS endpoint.
- **Milestone C (Week 3)**
  - Integrate WAN 2.2 runner, large artifact streaming, storage TTL jobs.
  - Implement workflow engine and end-to-end two-node pipeline test.
  - Finish error catalog mapping, idempotency persistence, rate limiting.
- **Milestone D (Week 4)**
  - Harden observability, load testing, autoscaling policies, warm pool management.
  - Security review, finalize conformance suite, prepare canary rollout.

## 6. Latency Guardrails
- Service-level budgets: Gateway p99 < 20 ms, Orchestrator p99 < 30 ms, Runner cold start < 120 ms with prewarm.
- Instrument `TIME_TO_FIRST_BYTE` metric captured at gateway for SSE/WS.
- Continuous load tests triggered nightly with synthetic payloads across models.

## 7. Flexibility Enablers
- Registry-driven routing enabling config-only onboarding of new model IDs.
- Versioned schema definitions stored as JSON Schema; validation performed prior to queue submission.
- Plugin interface for runners (gRPC) with capability descriptors (`supports_streaming`, `artifact_types`).

## 8. Risk Mitigation
- Provide fallback runner stubs returning canned responses for development.
- Implement dead-letter queues for failed jobs and webhook deliveries.
- Use feature flags (config table) to toggle new models/workflows safely.

## 9. Next Actions
- Approve technology stack and repo layout.
- Begin scaffolding gateway/orchestrator code using the outlined structure.
- Define database schemas (request metadata, logs, registry, keys) via migration tool.

