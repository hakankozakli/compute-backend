"""
Worker mode for runner-qwen that polls Redis queue and processes jobs.
"""
import asyncio
import json
import os
import time
from typing import Any

import httpx
import redis.asyncio as redis
from loguru import logger

from app.main import InvokeRequest, build_backend, ModelSettings


class RunnerCallbackClient:
    def __init__(self, base_url: str | None, token: str | None = None) -> None:
        self.base_url = base_url.rstrip("/") if base_url else None
        self.token = token
        self._client: httpx.AsyncClient | None = None

    async def _ensure_client(self) -> httpx.AsyncClient:
        if self._client is None:
            timeout = httpx.Timeout(connect=5.0, read=30.0, write=10.0)
            self._client = httpx.AsyncClient(timeout=timeout)
        return self._client

    async def post_event(self, job_id: str, payload: dict[str, Any]) -> None:
        if not self.base_url:
            return
        client = await self._ensure_client()
        headers = {}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        url = f"{self.base_url}/{job_id}/events"
        try:
            response = await client.post(url, json=payload, headers=headers)
            response.raise_for_status()
        except httpx.HTTPError as exc:  # pragma: no cover - network failure
            logger.error("Runner callback post failed for job {}: {}", job_id, exc)

    async def close(self) -> None:
        if self._client:
            await self._client.aclose()
            self._client = None


class QueueWorker:
    def __init__(
        self,
        model_id: str,
        redis_url: str,
        worker_id: str,
        node_id: str | None,
        callback: RunnerCallbackClient,
    ):
        self.model_id = model_id
        self.redis_url = redis_url
        self.worker_id = worker_id
        self.node_id = node_id
        self.callback = callback
        self.redis_client: redis.Redis | None = None
        self.backend = None
        self.settings = ModelSettings()

    async def connect(self) -> None:
        """Connect to Redis and initialize backend"""
        self.redis_client = await redis.from_url(
            self.redis_url,
            encoding="utf-8",
            decode_responses=True,
        )
        logger.info("Connected to Redis at {}", self.redis_url)

        # Initialize backend
        self.backend = build_backend(self.settings)
        logger.info("Backend initialized: {}", self.backend.__class__.__name__)

    async def poll_and_process(self) -> None:
        """Poll Redis queue and process jobs"""
        if not self.redis_client:
            raise RuntimeError("Redis client not connected")

        queue_keys: list[str] = []
        if self.node_id:
            queue_keys.append(f"queue:{self.node_id}:{self.model_id}")
        queue_keys.append(f"queue:{self.model_id}")

        logger.info(
            "Worker {} polling queues {}",
            self.worker_id,
            ", ".join(queue_keys),
        )

        while True:
            try:
                # Blocking pop from queue (5 second timeout)
                result = await self.redis_client.blpop(queue_keys, timeout=5)

                if result is None:
                    # No job available, continue polling
                    continue

                _, job_id = result
                logger.info("Worker {} picked up job {}", self.worker_id, job_id)

                # Process the job
                await self.process_job(job_id)

            except asyncio.CancelledError:
                logger.info("Worker {} cancelled", self.worker_id)
                break
            except Exception as exc:
                logger.exception("Worker {} error: {}", self.worker_id, exc)
                await asyncio.sleep(1)  # Brief pause before retry

    async def process_job(self, job_id: str) -> None:
        if not self.redis_client or not self.backend:
            return

        record = await self._fetch_job_record(job_id)
        if record is None:
            message = "job payload missing"
            await self.callback.post_event(
                job_id,
                {
                    "status": "ERROR",
                    "message": message,
                    "logs": [message],
                },
            )
            return

        payload = record.get("payload") or {}
        logs = [f"worker {self.worker_id} accepted job"]
        await self.callback.post_event(
            job_id,
            {
                "status": "IN_PROGRESS",
                "logs": logs[-1:],
                "message": "job dequeued",
            },
        )

        try:
            request = self._build_invoke_request(payload)
            result = await self.backend.generate(request)
            artifacts = [artifact.model_dump() for artifact in result.outputs]

            logs.append(f"job completed in {result.inference_seconds:.2f}s")
            await self.callback.post_event(
                job_id,
                {
                    "status": "COMPLETED",
                    "logs": logs,
                    "artifacts": artifacts,
                    "result": {
                        "elapsed_seconds": result.inference_seconds,
                        "worker_id": self.worker_id,
                        "model_id": self.model_id,
                    },
                },
            )

            logger.info(
                "Job {} completed in {:.2f}s",
                job_id,
                result.inference_seconds,
            )
        except Exception as exc:
            message = f"runner error: {exc}"
            logs.append(message)
            await self.callback.post_event(
                job_id,
                {
                    "status": "ERROR",
                    "message": message,
                    "logs": logs,
                },
            )
            logger.exception("Failed to process job {}", job_id)

    async def close(self) -> None:
        """Close Redis connection"""
        if self.redis_client:
            await self.redis_client.aclose()
        logger.info("Redis connection closed")
        await self.callback.close()

    async def _fetch_job_record(self, job_id: str) -> dict[str, Any] | None:
        job_key = f"job:{job_id}"
        assert self.redis_client is not None

        raw = await self.redis_client.get(job_key)
        if raw:
            try:
                record = json.loads(raw)
                if "payload" not in record:
                    record["payload"] = {}
                return record
            except json.JSONDecodeError:
                logger.warning("Job %s payload is not valid JSON", job_id)

        legacy = await self.redis_client.hgetall(job_key)
        if legacy:
            converted = {
                k.decode() if isinstance(k, bytes) else k: v.decode() if isinstance(v, bytes) else v
                for k, v in legacy.items()
            }
            return {"payload": converted}
        return None

    def _build_invoke_request(self, payload: dict[str, Any]) -> InvokeRequest:
        prompt = str(payload.get("prompt", ""))
        request_params = payload.copy()
        negative_prompt = request_params.get("negative_prompt")
        seed = request_params.get("seed")
        steps = request_params.get("steps")
        guidance = request_params.get("guidance_scale") or request_params.get("guidance")
        image_count = request_params.get("image_count") or request_params.get("num_images")
        width = request_params.get("width")
        height = request_params.get("height")

        return InvokeRequest(
            prompt=prompt,
            negative_prompt=negative_prompt,
            seed=int(seed) if seed is not None else None,
            steps=int(steps) if steps is not None else None,
            guidance_scale=float(guidance) if guidance is not None else None,
            image_count=int(image_count) if image_count is not None else 1,
            size=request_params.get("size"),
            width=int(width) if width is not None else None,
            height=int(height) if height is not None else None,
        )


async def main() -> None:
    """Main worker entry point"""
    model_id = os.getenv("WORKER_MODEL_ID", "qwen/image")
    redis_url = os.getenv("REDIS_URL", "redis://redis:6379")
    worker_id = os.getenv("WORKER_ID", f"worker-{os.getpid()}")
    node_id = os.getenv("RUNNER_NODE_ID")
    callback_url = os.getenv("RUNNER_CALLBACK_URL")
    callback_token = os.getenv("RUNNER_CALLBACK_TOKEN")
    callback_client = RunnerCallbackClient(callback_url, token=callback_token)

    logger.remove()
    import sys
    logger.add(sys.stdout, level=os.getenv("LOG_LEVEL", "INFO"))

    logger.info(
        "Starting worker model_id={} worker_id={} redis={}",
        model_id,
        worker_id,
        redis_url,
    )

    worker = QueueWorker(
        model_id=model_id,
        redis_url=redis_url,
        worker_id=worker_id,
        node_id=node_id,
        callback=callback_client,
    )

    try:
        await worker.connect()
        await worker.poll_and_process()
    except KeyboardInterrupt:
        logger.info("Worker interrupted")
    finally:
        await worker.close()


# Run worker when module is executed directly OR via -m
if __name__ == "__main__":
    asyncio.run(main())
