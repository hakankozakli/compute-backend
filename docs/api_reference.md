# Vyvo Compute API Reference

This document describes the HTTP interfaces exposed by the Vyvo Compute Gateway. All endpoints mirror fal.ai semantics and can be accessed by switching the base domain to Vyvo.

- Queue base URL: `https://queue.<vyvo-domain>`
- Sync base URL: `https://<vyvo-domain>`
- WebSocket base URL: `wss://ws.<vyvo-domain>`
- Admin REST base URL: `https://rest.<vyvo-domain>`

The current implementation focuses on the queue and sync HTTP flows; websocket and admin endpoints are reserved for future milestones.

## Authentication

Requests must supply an API key header:

```
Authorization: Key <API_KEY>
```

WebSocket connections accept the same key via query parameter (`?key=...`) or initial message payload (future work).

## Queue API

Submit asynchronous jobs for later retrieval. Paths are constructed from the target `model_id` and optional subpaths (`fal-ai/fast-sdxl`, `wan/2.2/dev`, etc.).

### Submit Job

- **Endpoint:** `POST https://queue.<vyvo-domain>/{model_id}`
- **Headers:**
  - `Authorization: Key <API_KEY>`
  - `Content-Type: application/json`
  - `X-Fal-Store-IO: 0` (optional, disable payload storage)
- **Query Parameters:**
  - `fal_webhook=<https-url>` optional completion webhook target
- **Body:** Model-specific JSON payload
- **Response:** `201 Created`

```json
{
  "request_id": "561dcb47-16eb-4b83-8178-5bd424fcf561",
  "response_url": "https://queue.<vyvo-domain>/{model_id}/requests/561dcb47-16eb-4b83-8178-5bd424fcf561",
  "status_url": "https://queue.<vyvo-domain>/{model_id}/requests/561dcb47-16eb-4b83-8178-5bd424fcf561/status",
  "cancel_url": "https://queue.<vyvo-domain>/{model_id}/requests/561dcb47-16eb-4b83-8178-5bd424fcf561/cancel"
}
```

### Get Job Response

- **Endpoint:** `GET https://queue.<vyvo-domain>/{model_id}/requests/{request_id}`
- **Responses:**
  - `200 OK` with model output JSON when complete
  - `202 Accepted` with in-progress status payload when still running
  - `404 Not Found` if unknown request

### Job Status Polling

- **Endpoint:** `GET https://queue.<vyvo-domain>/{model_id}/requests/{request_id}/status`
- **Query Parameters:**
  - `logs=1` (optional) include log entries
- **Response Body:**

  - In queue:

    ```json
    {
      "status": "IN_QUEUE",
      "queue_position": 2,
      "response_url": "…/requests/{request_id}"
    }
    ```

  - In progress or completed:

    ```json
    {
      "status": "IN_PROGRESS",
      "response_url": "…/requests/{request_id}",
      "logs": ["job dispatched", "model started"]
    }
    ```

### Streaming Job Status (SSE)

- **Endpoint:** `GET https://queue.<vyvo-domain>/{model_id}/requests/{request_id}/status/stream`
- **Response:** `text/event-stream` where each `data:` line encodes the same JSON as the polling endpoint. Stream terminates on completion or cancellation. Gateway flushes events as soon as the orchestrator publishes updates.

### Cancel Job

- **Endpoint:** `PUT https://queue.<vyvo-domain>/{model_id}/requests/{request_id}/cancel`
- **Responses:**
  - `202 Accepted`: `{ "status": "CANCELLATION_REQUESTED" }`
  - `400 Bad Request`: `{ "status": "ALREADY_COMPLETED" }`
  - `404 Not Found` for unknown request ids

## Sync API

For low-latency models, call the sync path to block until completion.

- **Endpoint:** `POST https://<vyvo-domain>/sync/{model_id}`
- **Headers:**
  - `Authorization: Key <API_KEY>`
  - `Content-Type: application/json`
- **Body:** Model-specific JSON payload
- **Response:** `200 OK` with the model output JSON. Failures surface as `502` if upstream orchestrator errors or `4xx` for validation issues.

## Status Payload Schema

```
status          string enum: IN_QUEUE, IN_PROGRESS, COMPLETED, ERROR
queue_position  integer >= 0 (present only in IN_QUEUE)
response_url    string (absolute URL)
logs            array of strings (optional)
```

## Error Handling

- `401/403`: Authentication or authorization failures
- `404`: Unknown model, request id, or resource
- `422`: Input validation issues. Response matches fal.ai `detail[]` schema:

```json
{
  "detail": [
    {
      "type": "less_than_equal",
      "loc": ["body", "prompt_length"],
      "msg": "prompt must be <= 1000 characters",
      "ctx": { "limit_value": 1000 }
    }
  ]
}
```

- `429`: Rate limiting (future)
- `5xx`: Internal errors; not billed per fal.ai policy.

## Webhooks (Upcoming)

Jobs submitted with `fal_webhook` include callback metadata. Dispatcher signature headers:

- `X-Fal-Webhook-Request-Id`
- `X-Fal-Webhook-User-Id`
- `X-Fal-Webhook-Timestamp`
- `X-Fal-Webhook-Signature` (ED25519)

Payload mirrors either job success (`payload`) or failure (`error`). JWKS is published under `https://rest.<vyvo-domain>/.well-known/jwks.json`.

## WebSocket API (Planned)

`wss://ws.<vyvo-domain>/{model_id}` accepts JSON payloads over a persistent connection. Initial server message contains `{ type: "start", request_id, status }`, followed by streamed frames mirroring fal.ai semantics, then `{ type: "end", … }`.

## Admin REST (Planned)

`https://rest.<vyvo-domain>` will expose `PUT /requests/delete_io/{request_id}` and logging endpoints restricted to ADMIN scoped keys.
