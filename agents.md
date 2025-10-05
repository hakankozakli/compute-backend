# agents.md — Vyvo Compute API (fal.ai-Compatible)

**Goal**: Deliver a fully containerized, low-latency API surface that is **drop‑in compatible** with fal.ai’s Model Endpoints (queue, synchronous, websockets) plus workflows and webhooks. Clients should be able to swap the base URL to Vyvo without changing request bodies/headers and receive the same shapes, status codes, and streaming semantics.

---

## 0) Design Principles

* **Compatibility first**: Mirror fal.ai endpoint paths, headers, response shapes, error semantics, and streaming behavior.
* **Latency**: Sub‑TTFB targets: <150–300 ms to first byte for streaming models; <1s for queue submission; results dominated by model runtime.
* **Flexibility**: Uniform adapters to plug in image/video models (e.g., Qwen image, WAN 2.2) and composite workflows.
* **Isolation & repeatability**: All components run as containers; dev/prod parity.
* **Observability**: Request ID propagation, logs, metrics in status streams, structured errors.

---

## 1) Base Hostnames & Routing (must match fal semantics)

* **Queue** (recommended): `https://queue.<vyvo-domain>`
* **Synchronous** (low-latency): `https://<vyvo-domain>`
* **WebSockets** (real‑time): `wss://ws.<vyvo-domain>`
* **Admin/REST (payload ops, logs)**: `https://rest.<vyvo-domain>`

> Note: We keep the **path format** identical to fal.ai, including the `model_id` namespace segments (e.g., `fal-ai/fast-sdxl`). Internally we alias these to Vyvo model runners.

---

## 2) Authentication (mirror behavior)

* Header: `Authorization: Key <API_KEY>`
* WebSocket: authenticate via query param/JWT (short‑lived) or via initial message as per fal behavior; gateway mints/validates ephemeral session tokens.
* Admin endpoints (e.g., delete IO) require keys with **ADMIN** scope.

---

## 3) Endpoints: **Queue API** (primary flow)

**Purpose**: Submit async jobs, poll or stream status, fetch result, cancel. Paths must be **byte‑for‑byte compatible**.

### 3.1 Submit to Queue

* **POST** `https://queue.<vyvo-domain>/{model_id}`
* **POST** `https://queue.<vyvo-domain>/{model_id}/{subpath}` (optional subpath)
* Headers: `Authorization: Key …`, `Content-Type: application/json`
* Optional Header: `X-Fal-Store-IO: 0` (disable payload storage)
* **Body**: Model‑specific JSON (unchanged from fal models)
* **Query (optional)**: `fal_webhook=<url>` to receive completion callbacks
* **201 Response**:

  ```json
  {
    "request_id": "<uuid>",
    "response_url": "https://queue.<vyvo-domain>/{model_id}/requests/<uuid>",
    "status_url": "https://queue.<vyvo-domain>/{model_id}/requests/<uuid>/status",
    "cancel_url": "https://queue.<vyvo-domain>/{model_id}/requests/<uuid>/cancel"
  }
  ```

### 3.2 Request Status

* **GET** `…/requests/{request_id}/status`
* Query:

  * `logs=1` to include logs array
* **200 Response** (**exact shapes**):

  * `IN_QUEUE`: `{ status, queue_position, response_url }`
  * `IN_PROGRESS`: `{ status, response_url, logs? }`
  * `COMPLETED`: `{ status, response_url, logs? }`

### 3.3 Streamed Status (SSE)

* **GET** `…/requests/{request_id}/status/stream`
* Response: `text/event-stream` with repeated JSON events mirroring the non-stream status, until `COMPLETED`.

### 3.4 Get Response

* **GET** `…/requests/{request_id}`
* **200 Response**: Model‑specific JSON payload (images/videos URLs, timings, seed, nsfw flags, etc.).

### 3.5 Cancel

* **PUT** `…/requests/{request_id}/cancel`
* **202** `{ "status": "CANCELLATION_REQUESTED" }` if still `IN_QUEUE`
* **400** `{ "status": "ALREADY_COMPLETED" }` if too late

---

## 4) Endpoints: **Synchronous API** (low tail latency; blocking)

**Use when** the request is predictably quick and you want minimal latency.

* **POST** `https://<vyvo-domain>/{model_id}`
* **POST** `https://<vyvo-domain>/{model_id}/{subpath}`
* Same headers/body as Queue.
* **200 Response**: Direct model output JSON (e.g., `{ images: […], timings: { inference: … }, … }`).
* Tradeoffs: caller maintains the connection; no resume; failures are charged identically to fal behavior; use sparingly for heavy models.

---

## 5) Endpoints: **HTTP over WebSockets** (real‑time)

* **Connect**: `wss://ws.<vyvo-domain>/{model_id}`
* **Protocol** (message order):

  1. **Client →** JSON payload (same as HTTP body)
  2. **Server →** `start` metadata (JSON): `{ type: "start", request_id, status, headers }`
  3. **Server →** response stream: binary chunks **or** JSON/SSE text chunks depending on `Content-Type`
  4. **Server →** completion: `{ type: "end", request_id, status, time_to_first_byte_seconds }`
* Supports back‑to‑back prompts on one socket; responses guaranteed in order.

---

## 6) Endpoints: **Workflows** (pipelines)

* **Concept**: Chain multiple models (e.g., WAN 2.2 → video‑to‑audio) with streaming of **step events**.
* **Call** (mirror fal): use `queue`/`sync`/`ws` base with `workflows/{namespace}/{workflow_id}` as the `model_id`.
* **Streamed events (SSE/WS)**:

  * `submit`: `{ type: "submit", node_id, app_id, request_id }`
  * `completion`: `{ type: "completion", node_id, output: <model_output> }`
  * `output`: `{ type: "output", output: <final_output> }`
  * `error`: `{ type: "error", node_id, message, error: { status, body } }`
* **Response (final)**: same JSON as final node’s output.

---

## 7) Webhooks (callbacks)

* **How**: supply `fal_webhook=<https-url>` when submitting queue requests.
* **Delivery**: POST JSON with `{ request_id, status, payload? | error? }` (payload mirrored from the response). Retries with exponential backoff; include headers:

  * `X-Fal-Webhook-Request-Id`
  * `X-Fal-Webhook-User-Id`
  * `X-Fal-Webhook-Timestamp`
  * `X-Fal-Webhook-Signature` (ED25519; verify against public JWKS)
* **Security**: Publish `/.well-known/jwks.json` under `rest.<vyvo-domain>`; instruct integrators to cache for ≤24h.

---

## 8) Payload Storage & Privacy

* Default: store input/output payloads for 30 days; expose deletion API.
* **Disable storage per request**: `X-Fal-Store-IO: 0` header.
* **Delete IO** (admin scope):

  * **PUT** `https://rest.<vyvo-domain>/requests/delete_io/{request_id}`
  * Response: `{ CDN_DELETE_RESULTS: [ { link, exception } ] }`

---

## 9) Errors & Status Codes (mirror)

* **422** validation errors with Pydantic-like `detail` array and type codes (e.g., `image_too_large`, `file_download_error`, `multiple_of`, `less_than_equal`, etc.).
* **401/403** auth/access; **404** unknown request/model; **429** rate limit; **5xx** server.
* **Charging semantics**: client-visible FAQ alignment (do not charge 5xx; charge 422 invalid input).

---

## 10) Rate Limits & Headers

* Soft limits per key/route; respond with standard headers:

  * `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`.
* Idempotency (optional but safe): honor `Idempotency-Key` for queue submissions.

---

## 11) Model Registry & Aliasing

* Keep a registry mapping external **model_ids** (e.g., `fal-ai/veo3`, `fal-ai/fast-sdxl`, `qwen/<image-model>`, `wan/2.2`) to internal runners.
* Support **subpaths** (e.g., `/dev`, `/v1.6/pro/text-to-video`) exactly; status and response URLs must omit subpath just like fal.
* Per‑model **schema**: accept the same JSON inputs and output shapes (images/videos array, `timings`, `seed`, `has_nsfw_concepts`, etc.).

---

## 12) Performance Plan (to meet latency goals)

* **Warm pools** per model with min‑ready pods/containers sized to expected QPS; burst auto‑scale.
* **Pinned GPU pools** for heavy video (WAN 2.2) vs. lighter image models to avoid contention.
* **Streaming by default** for any model producing progressive bytes; send `start` ASAP with HTTP/1.1 chunked or WS framing.
* **Edge cache/CDN** for outputs (signed, time‑bound URLs) to speed delivery; avoid egress hot‑spots.
* **Binary fast path**: WS binary frames for media; SSE for JSON-only streams.

---

## 13) Containerized Services (no code, just roles)

* **API Gateway** (queue/sync/ws routers):

  * Terminates TLS; enforces auth; issues WS session tokens; routes based on `{model_id}`; injects `request_id`.
* **Orchestrator**:

  * Enqueues jobs, tracks status, dispatches to model runners; cancellation; retries; metrics; logs fan‑out.
* **Model Runners** (per model family):

  * Qwen Image, WAN 2.2 video, video→audio, etc.; gRPC/HTTP contract; streamers.
* **Workflow Engine**:

  * DAG execution, fan‑out/fan‑in, emits `submit/completion/output/error` events in order.
* **Storage**:

  * Payload DB (metadata), Object storage/CDN for media; signed URL issuer.
* **Webhook Dispatcher**:

  * Delivery, retry policy, ED25519 signing.
* **Admin REST**:

  * `delete_io`, logs search, JWKS publication.
* **Observability**:

  * Central log store; traces; SLO exports; per‑request `metrics` surfaced in status streams.

---

## 14) Queues & Concurrency (compat + elasticity)

* Job states: `RECEIVED → IN_QUEUE → DISPATCHED → IN_PROGRESS → COMPLETED | ERROR | CANCELED`.
* **Fair‑share**: per‑key quotas; per‑model max concurrency; preemption policy for low‑priority keys if needed.
* **Cancellation**: allowed while `IN_QUEUE`; best‑effort during startup; return compatible bodies.

---

## 15) Internationalization & Multitenancy (control plane)

* Multitenant orgs/teams reflected in API keys and quotas; org‑scoped keys map to usage and limits.
* Locale does not affect API; only dashboard/public site.

---

## 16) Backward/Forward Compatibility

* Ship a **conformance test suite** that exercises:

  * Queue submit/status/stream/cancel/response
  * Sync POST results
  * WS start/stream/end contract
  * Workflows stream event ordering
  * Webhook signature verification
  * Error catalog coverage (422 variants)
* Maintain aliasing table so new upstream model paths can be mapped without client changes.

---

## 17) Rollout Plan (2‑week target, functional slice)

1. **Day 1–2**: Gateway skeleton (auth, routing), request IDs, minimal orchestrator, in‑memory queue.
2. **Day 3–4**: Queue endpoints (submit/status/response/cancel), SSE status stream; containerized Qwen Image runner.
3. **Day 5–6**: Sync endpoints; WS start/stream/end; basic media CDN integration.
4. **Day 7**: Webhooks + JWKS signing; delete_io admin endpoint.
5. **Day 8–9**: Add WAN 2.2 video runner; large‑artifact streaming; storage TTL.
6. **Day 10**: Workflows (two‑node: WAN 2.2 → video‑to‑audio); stream events.
7. **Day 11–12**: Error catalog + conformance tests; rate limits; idempotency.
8. **Day 13–14**: Load tests, prewarm policies, observability, polish.

---

## 18) Acceptance Criteria

* All documented paths, headers, query params, and responses match fal.ai contracts.
* Smoke tests pass for: Qwen image, WAN 2.2 video, video→audio workflow.
* Webhooks verified against JWKS; delete_io works; payload storage off when `X-Fal-Store-IO: 0`.
* TTFB and tail-latency targets met in canary.

---

## 19) Open Questions / Decisions for Later

* Whether to expose file upload helper endpoints (presigned URLs) or rely on client‑side uploads only.
* Fine‑grained per‑model quotas vs. global; burst credits behavior.
* Additional compatibility surfaces (clients SDK methods) — optional, **not required** for HTTP parity.
