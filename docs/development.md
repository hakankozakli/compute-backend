# Development Quickstart

1. **Prerequisites**
   - Go 1.22+
   - Rust 1.78+
   - Python 3.11 with Poetry
   - Docker & Docker Compose

2. **Install dependencies**
   - Run `go mod tidy` to fetch Go modules (`chi`, `uuid`, `viper`, `otel`, custom orchestrator client).
   - Within `orchestrator/`, execute `cargo fetch` to download Rust crates (`axum`, `tokio`, `chrono`, `tokio-stream`).
   - Install runner dependencies via `poetry install` inside `runners/`.

3. **Local stack**
   - `docker compose -f deploy/docker-compose.yml up --build` launches gateway, orchestrator, runners, Redis, Postgres, MinIO, and the OTEL collector.
   - Queue submission: `curl -X POST http://localhost:8080/fal-ai/fast-sdxl -H 'Content-Type: application/json' -d '{"prompt":"test"}'`.
   - Status polling: `curl http://localhost:8080/fal-ai/fast-sdxl/requests/<id>/status`.
   - SSE: `curl http://localhost:8080/fal-ai/fast-sdxl/requests/<id>/status/stream`.

4. **Synchronous path**
   - Invoke synchronous models via `POST http://localhost:8080/sync/fal-ai/fast-sdxl` with identical body semantics; gateway relays to orchestrator's synchronous runner.

5. **Configuration**
   - Adjust `configs/config.yaml` for gateway defaults. Environment variables override using the `GATEWAY_*` prefix.
   - Orchestrator currently uses an in-memory queue for fast iteration; plug in Redis/Postgres by extending `orchestrator/src/state.rs`.

6. **Testing**
   - Run `go test ./pkg/...` once modules are fetched (example unit test in `pkg/auth`).
   - Execute `cargo test -p orchestrator` after implementing additional logic.

7. **Next steps**
   - Replace stubbed runner responses with actual gRPC/HTTP calls to GPU workers.
   - Implement webhook dispatcher and Admin REST service to complete compatibility matrix.
   - Expand conformance tests under `tests/conformance/` to mirror fal.ai cases.
