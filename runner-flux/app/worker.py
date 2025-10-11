from __future__ import annotations

import asyncio
import json
import os
import time
from typing import Any, List

import httpx
import redis.asyncio as redis
from loguru import logger

from .backend import BackendResult, load_backend
from .types import InvokeRequest, RunnerEvent, WorkerConfig


def _load_config() -> WorkerConfig:
    redis_url = os.getenv("REDIS_URL")
    model_id = os.getenv("VYVO_MODEL_ID")
    if not redis_url or not model_id:
        raise RuntimeError("REDIS_URL and VYVO_MODEL_ID must be set for the worker")
    return WorkerConfig(
        redis_url=redis_url,
        model_id=model_id,
        node_id=os.getenv("VYVO_NODE_ID"),
        callback_url=os.getenv("RUNNER_CALLBACK_URL"),
        callback_token=os.getenv("RUNNER_CALLBACK_TOKEN"),
    )


class RunnerCallbackClient:
    def __init__(self, base_url: str | None, token: str | None) -> None:
        self.base_url = base_url.rstrip("/") if base_url else None
        self.token = token
        self._client: httpx.AsyncClient | None = None

    async def _client_instance(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(timeout=httpx.Timeout(connect=5.0, read=30.0, write=10.0))
        return self._client

    async def post_event(self, job_id: str, payload: RunnerEvent) -> None:
        if not self.base_url:
            return
        client = await self._client_instance()
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        url = f"{self.base_url}/{job_id}/events"
        try:
            await client.post(url, json=payload.dict(exclude_none=True), headers=headers)
        except Exception as exc:  # pragma: no cover - network
            logger.error("failed to post runner event", job_id=job_id, error=exc)

    async def close(self) -> None:
        if self._client:
            await self._client.aclose()
            self._client = None


class FluxQueueWorker:
    def __init__(self, cfg: WorkerConfig) -> None:
        self.cfg = cfg
        backend, model_id, backend_info = load_backend()
        self.backend = backend
        self.model_id = model_id
        self.backend_info = backend_info
        if backend_info.get("mode") == "diffusers":
            logger.info("runner using diffusers backend", model_id=self.model_id)
        else:
            logger.warning(
                "runner operating in stub mode",
                model_id=self.model_id,
                reason=self.backend_info.get("reason"),
            )
        self.redis: redis.Redis | None = None
        self.callback = RunnerCallbackClient(cfg.callback_url, cfg.callback_token)

    async def connect(self) -> None:
        self.redis = await redis.from_url(self.cfg.redis_url, encoding="utf-8", decode_responses=True)
        logger.info("worker connected to redis", url=self.cfg.redis_url)

    async def close(self) -> None:
        if self.redis:
            await self.redis.aclose()
        await self.callback.close()

    async def run(self) -> None:
        if not self.redis:
            raise RuntimeError("worker not connected")

        queue_keys: List[str] = []
        if self.cfg.node_id:
            queue_keys.append(f"queue:{self.cfg.node_id}:{self.model_id}")
        queue_keys.append(f"queue:{self.model_id}")

        logger.info("polling queues", queues=queue_keys)

        while True:
            try:
                result = await self.redis.blpop(queue_keys, timeout=5)
                if result is None:
                    continue
                queue_name, job_id = result
                logger.info("picked job", job_id=job_id, queue=queue_name)
                for other in queue_keys:
                    if other != queue_name:
                        await self.redis.lrem(other, 0, job_id)
                await self._process(job_id)
            except asyncio.CancelledError:  # pragma: no cover
                logger.info("worker cancelled")
                break
            except Exception as exc:
                logger.exception("worker loop error", error=exc)
                await asyncio.sleep(1)

    async def _process(self, job_id: str) -> None:
        if not self.redis:
            return
        job_key = f"job:{job_id}"
        payload = await self.redis.get(job_key)
        if not payload:
            logger.error("job payload missing", job_id=job_id)
            await self.callback.post_event(job_id, RunnerEvent(status="ERROR", message="payload missing"))
            return

        try:
            record = json.loads(payload)
        except json.JSONDecodeError:
            logger.error("job payload invalid json", job_id=job_id)
            await self.callback.post_event(job_id, RunnerEvent(status="ERROR", message="invalid payload"))
            return

        params = record.get("params", {})
        prompt = params.get("prompt") or record.get("prompt")
        if not prompt:
            await self.callback.post_event(job_id, RunnerEvent(status="ERROR", message="prompt missing"))
            return

        request = InvokeRequest(
            prompt=prompt,
            negative_prompt=params.get("negative_prompt"),
            steps=params.get("steps"),
            guidance_scale=params.get("guidance_scale"),
            width=params.get("width"),
            height=params.get("height"),
            seed=params.get("seed"),
            image_count=params.get("image_count", 1),
        )

        now = int(time.time())
        record["status"] = "processing"
        record["started_at"] = now
        record["worker_id"] = self.model_id
        if not record.get("node_id") and self.cfg.node_id:
            record["node_id"] = self.cfg.node_id
        record["backend_mode"] = self.backend_info.get("mode")
        record["backend_reason"] = self.backend_info.get("reason")
        await self.redis.set(job_key, json.dumps(record))

        logs = [f"worker accepted job {job_id}"]
        await self.callback.post_event(job_id, RunnerEvent(status="IN_PROGRESS", logs=logs[-1:]))

        if self.backend_info.get("mode") != "diffusers":
            warning_reason = self.backend_info.get("reason") or "diffusers backend unavailable"
            warning_message = f"WARNING: runner in stub mode — {warning_reason}"
            logs.append(warning_message)
            await self.callback.post_event(
                job_id,
                RunnerEvent(
                    status="LOG",
                    message="Runner operating in stub mode",
                    logs=[warning_message],
                ),
            )

        try:
            result: BackendResult = await self.backend.generate(request)
            artifacts = [artifact.dict() for artifact in result.outputs]
            logs.append(f"completed in {result.inference_seconds:.2f}s")

            record["status"] = "completed"
            record["completed_at"] = int(time.time())
            result_payload: dict[str, Any] = {
                "elapsed_seconds": result.inference_seconds,
                "worker_id": self.model_id,
                "artifacts": artifacts,
            }
            if artifacts:
                result_payload["image_url"] = artifacts[0].get("url")
            record["result"] = result_payload
            record["logs"] = logs
            record["backend_mode"] = self.backend_info.get("mode")
            record["backend_reason"] = self.backend_info.get("reason")
            await self.redis.set(job_key, json.dumps(record))

            await self.callback.post_event(
                job_id,
                RunnerEvent(
                    status="COMPLETED",
                    logs=logs,
                    result={
                        "elapsed_seconds": result.inference_seconds,
                        "worker_id": self.model_id,
                    },
                    artifacts=artifacts,
                ),
            )
        except HTTPException as exc:
            logs.append(f"runner error: {exc.detail}")
            await self.callback.post_event(
                job_id,
                RunnerEvent(status="ERROR", message=exc.detail, logs=logs),
            )
            record["status"] = "failed"
            record["error"] = exc.detail
            record["logs"] = logs
            record["backend_mode"] = self.backend_info.get("mode")
            record["backend_reason"] = self.backend_info.get("reason")
            await self.redis.set(job_key, json.dumps(record))
        except Exception as exc:  # pragma: no cover - runtime error
            logger.exception("job failed", job_id=job_id)
            logs.append(str(exc))
            await self.callback.post_event(
                job_id,
                RunnerEvent(status="ERROR", message=str(exc), logs=logs),
            )
            record["status"] = "failed"
            record["error"] = str(exc)
            record["logs"] = logs
            record["backend_mode"] = self.backend_info.get("mode")
            record["backend_reason"] = self.backend_info.get("reason")
            await self.redis.set(job_key, json.dumps(record))


async def main() -> None:
    cfg = _load_config()
    worker = FluxQueueWorker(cfg)
    await worker.connect()
    try:
        await worker.run()
    finally:
        await worker.close()


if __name__ == "__main__":  # pragma: no cover
    asyncio.run(main())
