from __future__ import annotations

import asyncio
import json
import os
import time
import uuid
from typing import Any

import httpx
import redis.asyncio as redis
from loguru import logger
from minio import Minio
from minio.error import S3Error

from .backend_factory import load_backend
from .types import Artifact, InvokeRequest

QUEUE_POLL_SECONDS = float(os.getenv("QUEUE_POLL_SECONDS", "1.0"))


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
            logger.debug("Runner callback disabled; skipping event for job {}", job_id)
            return
        client = await self._ensure_client()
        url = f"{self.base_url}/{job_id}/events"
        headers = {}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        try:
            response = await client.post(url, json=payload, headers=headers)
            response.raise_for_status()
        except httpx.HTTPError as exc:  # pragma: no cover - depends on network
            logger.error("Failed to post runner event for job {}: {}", job_id, exc)

    async def close(self) -> None:
        if self._client:
            await self._client.aclose()
            self._client = None


class MinIOUploader:
    def __init__(self) -> None:
        endpoint = os.getenv("MINIO_ENDPOINT")
        if not endpoint:
            raise ValueError("MINIO_ENDPOINT is required for artifact uploads")
        access_key = os.getenv("MINIO_ACCESS_KEY", "vyvo")
        secret_key = os.getenv("MINIO_SECRET_KEY", "vyvo-secret")
        secure = os.getenv("MINIO_SECURE", "false").lower() in {"1", "true", "yes"}
        self.bucket = os.getenv("MINIO_BUCKET", "vyvo-artifacts")
        self.prefix = os.getenv("MINIO_PREFIX", "runner")
        self.public_url = os.getenv("MINIO_PUBLIC_URL")

        self.client = Minio(
            endpoint,
            access_key=access_key,
            secret_key=secret_key,
            secure=secure,
        )

        try:
            if not self.client.bucket_exists(self.bucket):
                self.client.make_bucket(self.bucket)
                logger.info("Created MinIO bucket: {}", self.bucket)
        except S3Error as exc:  # pragma: no cover - external service
            logger.error("Failed to ensure MinIO bucket exists: {}", exc)
            raise

    def _object_name(self, job_id: str, suffix: str) -> str:
        ts = int(time.time())
        random_token = uuid.uuid4().hex[:8]
        return f"{self.prefix}/{job_id}/{ts}-{random_token}.{suffix}"

    async def upload_json(self, job_id: str, payload: dict[str, Any]) -> str:
        data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        return await self.upload_bytes(job_id, data, content_type="application/json", suffix="json")

    async def upload_bytes(
        self,
        job_id: str,
        data: bytes,
        *,
        content_type: str,
        suffix: str,
    ) -> str:
        object_name = self._object_name(job_id, suffix)

        import io

        await asyncio.to_thread(
            self.client.put_object,
            self.bucket,
            object_name,
            io.BytesIO(data),
            len(data),
            content_type=content_type,
        )

        if self.public_url:
            return f"{self.public_url.rstrip('/')}/{self.bucket}/{object_name}"

        endpoint = os.getenv("MINIO_ENDPOINT", "localhost:9000")
        secure = os.getenv("MINIO_SECURE", "false").lower() in {"1", "true", "yes"}
        protocol = "https" if secure else "http"
        return f"{protocol}://{endpoint}/{self.bucket}/{object_name}"


class QueueWorker:
    def __init__(
        self,
        model_id: str,
        redis_url: str,
        worker_id: str,
        node_id: str | None,
        callback: RunnerCallbackClient,
        uploader: MinIOUploader | None,
    ) -> None:
        self.model_id = model_id
        self.redis_url = redis_url
        self.worker_id = worker_id
        self.node_id = node_id
        self.redis_client: redis.Redis | None = None
        self.backend, self.backend_model_id = load_backend()
        self.callback = callback
        self.uploader = uploader

    async def connect(self) -> None:
        self.redis_client = await redis.from_url(
            self.redis_url,
            encoding="utf-8",
            decode_responses=True,
        )
        logger.info("Worker connected to Redis at {}", self.redis_url)

    async def poll_and_process(self) -> None:
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
                result = await self.redis_client.blpop(queue_keys, timeout=5)
                if result is None:
                    await asyncio.sleep(QUEUE_POLL_SECONDS)
                    continue

                _, job_id = result
                await self.process_job(job_id)
            except asyncio.CancelledError:
                logger.info("Worker {} cancelled", self.worker_id)
                break
            except Exception as exc:  # pragma: no cover - keep worker alive
                logger.exception("Worker {} error: {}", self.worker_id, exc)
                await asyncio.sleep(1)

    async def fetch_job_payload(self, job_id: str) -> dict[str, Any] | None:
        if not self.redis_client:
            return None
        job_key = f"job:{job_id}"
        payload = await self.redis_client.get(job_key)
        if not payload:
            logger.warning("Job {} missing payload in Redis", job_id)
            return None
        try:
            return json.loads(payload)
        except json.JSONDecodeError:
            logger.error("Job {} payload is not valid JSON", job_id)
            return None

    async def process_job(self, job_id: str) -> None:
        if not self.redis_client:
            return

        logs: list[str] = []
        payload = await self.fetch_job_payload(job_id)
        if not payload:
            await self.callback.post_event(
                job_id,
                {
                    "status": "ERROR",
                    "message": "job payload missing",
                    "logs": ["job payload missing"],
                },
            )
            return

        payload_body = payload.get("payload") or {}
        request = InvokeRequest(
            prompt=str(payload_body.get("prompt", "")),
            params=payload_body if isinstance(payload_body, dict) else {},
        )

        logs.append(f"worker {self.worker_id} accepted job")
        await self.callback.post_event(
            job_id,
            {
                "status": "IN_PROGRESS",
                "logs": logs[-1:],
                "message": f"worker {self.worker_id} started processing",
            },
        )

        try:
            artifacts, elapsed = await self.backend.generate(request)
            prepared = await self._prepare_artifacts(job_id, artifacts)
            logs.append(f"job completed in {elapsed:.2f}s")
            await self.callback.post_event(
                job_id,
                {
                    "status": "COMPLETED",
                    "logs": logs,
                    "artifacts": prepared,
                    "result": {
                        "elapsed_seconds": elapsed,
                        "worker_id": self.worker_id,
                        "model_id": self.backend_model_id,
                    },
                },
            )
            logger.info("Job {} completed", job_id)
        except Exception as exc:  # pragma: no cover - inference failure
            message = f"worker failure: {exc}"
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

    async def _prepare_artifacts(self, job_id: str, artifacts: list[Artifact]) -> list[dict[str, Any]]:
        prepared: list[dict[str, Any]] = []
        for idx, artifact in enumerate(artifacts):
            artifact_dict = artifact.model_dump()
            url = artifact_dict.get("url")

            if not url and self.uploader:
                upload_payload = {
                    "job_id": job_id,
                    "index": idx,
                    "metadata": artifact_dict.get("metadata", {}),
                }
                try:
                    url = await self.uploader.upload_json(job_id, upload_payload)
                    artifact_dict["url"] = url
                except Exception as exc:  # pragma: no cover - storage failure
                    logger.error("Failed to upload artifact {} for job {}: {}", idx, job_id, exc)
                    continue

            if not url:
                logger.warning("Artifact {} for job {} missing URL", idx, job_id)
                continue

            prepared.append(artifact_dict)
        return prepared

    async def close(self) -> None:
        if self.redis_client:
            await self.redis_client.aclose()
        await self.callback.close()


async def main() -> None:
    model_id = os.getenv("WORKER_MODEL_ID", "template/model")
    redis_url = os.getenv("REDIS_URL", "redis://redis:6379")
    worker_id = os.getenv("WORKER_ID", f"worker-{os.getpid()}")
    node_id = os.getenv("RUNNER_NODE_ID")

    callback_url = os.getenv("RUNNER_CALLBACK_URL")
    callback_token = os.getenv("RUNNER_CALLBACK_TOKEN")
    callback_client = RunnerCallbackClient(callback_url, token=callback_token)

    uploader: MinIOUploader | None = None
    if os.getenv("MINIO_ENDPOINT"):
        try:
            uploader = MinIOUploader()
            logger.info("MinIO uploader initialised")
        except Exception as exc:  # pragma: no cover - external dependency
            logger.warning("MinIO unavailable, falling back to inline artifact URLs: {}", exc)
            uploader = None

    worker = QueueWorker(
        model_id=model_id,
        redis_url=redis_url,
        worker_id=worker_id,
        node_id=node_id,
        callback=callback_client,
        uploader=uploader,
    )

    try:
        await worker.connect()
        await worker.poll_and_process()
    except KeyboardInterrupt:
        logger.info("Worker interrupted")
    finally:
        await worker.close()


if __name__ == "__main__":
    asyncio.run(main())
