# Infrastructure & Operations Guide

## 1. Local Development
- `docker compose -f deploy/docker-compose.yml up --build` launches gateway, orchestrator, runners, Redis, Postgres, MinIO, and telemetry collector.
- Override gateway configuration via environment variables (`GATEWAY_LISTEN_ADDR`, `GATEWAY_QUEUE_BASE_URL`, `GATEWAY_ORCHESTRATOR_URL`).
- The orchestrator exposes health checks on `http://localhost:8081/healthz` and gateway on `http://localhost:8080/healthz`.

## 2. Kubernetes Manifests
Kubernetes specs live under `deploy/k8s/` and include:
- `namespace.yaml` – namespaced isolation for vyvo-compute workloads.
- `gateway-deployment.yaml` – queue gateway Deployment + Service with readiness probes.
- `orchestrator-deployment.yaml` – orchestrator Deployment + Service with horizontal pod autoscaler annotations.
- `runners.yaml` – sample Deployment for Qwen runner.
- `redis.yaml`, `postgres.yaml`, `minio.yaml` – stateful backing services (intended for dev/staging; swap with managed services in production).

Apply with:
```sh
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/redis.yaml
kubectl apply -f deploy/k8s/postgres.yaml
kubectl apply -f deploy/k8s/minio.yaml
kubectl apply -f deploy/k8s/orchestrator-deployment.yaml
kubectl apply -f deploy/k8s/gateway-deployment.yaml
kubectl apply -f deploy/k8s/runners.yaml
```

## 3. Configuration Management
- Gateway config is sourced from `configs/config.yaml` with env overrides; mount as ConfigMap in Kubernetes (see Deployment template).
- Orchestrator currently uses in-memory queue for rapid prototyping; replace with Redis/SQL drivers by extending `AppState` in `orchestrator/src/state.rs`.

## 4. Observability
- OpenTelemetry collector (`otel_collector`) runs in docker-compose; for Kubernetes, deploy the collector and point services to the OTLP endpoint via `OTEL_EXPORTER_OTLP_ENDPOINT` env var.
- Gateway emits structured logs with request IDs (Chi middleware); orchestrator emits tracing spans via `tracing`.

## 5. Deployment Pipeline
- `Makefile` targets produce static binaries (`make gateway`, `make orchestrator`).
- Container builds rely on Dockerfiles under `configs/docker/`; integrate with CI to build/push images per commit.
- Employ GitHub Actions/ArgoCD to apply manifests automatically after image pushes.

## 6. Secrets & Security
- API keys should be stored in Postgres (not yet implemented) and served via vault-managed secrets.
- Webhook signing keys belong in Vault or AWS KMS; ensure JWKS endpoint publishes the public key via Admin REST service when implemented.

## 7. Scaling Strategy
- Run separate gateway Deployments for queue (`queue.<domain>`) and sync (`<domain>`) traffic by setting different `GATEWAY_QUEUE_BASE_URL` values and ingress host rules.
- Configure HPA for orchestrator based on queue depth metrics exported via Prometheus (future work: integrate metrics endpoint).
