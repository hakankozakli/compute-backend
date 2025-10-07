"""
Worker mode for runner-qwen that polls Redis queue and processes jobs.
"""
import asyncio
import json
import os
import time
from typing import Any

import redis.asyncio as redis
from loguru import logger

from app.main import InvokeRequest, build_backend, ModelSettings


class QueueWorker:
    def __init__(self, model_id: str, redis_url: str, worker_id: str):
        self.model_id = model_id
        self.redis_url = redis_url
        self.worker_id = worker_id
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

        queue_key = f"queue:{self.model_id}"
        logger.info("Worker {} polling queue {}", self.worker_id, queue_key)

        while True:
            try:
                # Blocking pop from queue (5 second timeout)
                result = await self.redis_client.blpop(queue_key, timeout=5)

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
        """Process a single job"""
        if not self.redis_client or not self.backend:
            return

        job_key = f"job:{job_id}"

        try:
            # Get job data from Redis
            job_json = await self.redis_client.get(job_key)
            if not job_json:
                logger.error("Job {} not found in Redis", job_id)
                return

            job_data = json.loads(job_json)
            logger.debug("Processing job {}: {}", job_id, job_data)

            # Update job status to processing
            job_data["status"] = "processing"
            job_data["worker_id"] = self.worker_id
            job_data["started_at"] = int(time.time())
            await self.redis_client.set(job_key, json.dumps(job_data))

            # TODO: Update orchestrator status via HTTP
            # For now, just process the job

            # Convert params to InvokeRequest
            params = job_data.get("params", {})
            request = InvokeRequest(
                prompt=params.get("prompt", ""),
                negative_prompt=params.get("negative_prompt"),
                seed=params.get("seed"),
                steps=params.get("steps"),
                guidance_scale=params.get("guidance_scale"),
                image_count=params.get("image_count", 1),
                size=params.get("size"),
                width=params.get("width"),
                height=params.get("height"),
            )

            # Generate
            result = await self.backend.generate(request)

            # Update job with result
            job_data["status"] = "completed"
            job_data["completed_at"] = int(time.time())
            job_data["result"] = {
                "request_id": result.request_id,
                "outputs": [
                    {
                        "type": output.type,
                        "url": output.url,
                        "mime_type": output.mime_type,
                        "seed": output.seed,
                    }
                    for output in result.outputs
                ],
                "inference_seconds": result.inference_seconds,
            }

            await self.redis_client.set(job_key, json.dumps(job_data))
            logger.info(
                "Job {} completed in {:.2f}s",
                job_id,
                result.inference_seconds,
            )

            # TODO: Notify orchestrator of completion via HTTP

        except Exception as exc:
            logger.exception("Failed to process job {}: {}", job_id, exc)

            # Mark job as failed
            try:
                job_json = await self.redis_client.get(job_key)
                if job_json:
                    job_data = json.loads(job_json)
                    job_data["status"] = "failed"
                    job_data["completed_at"] = int(time.time())
                    job_data["error"] = str(exc)
                    await self.redis_client.set(job_key, json.dumps(job_data))
            except Exception as update_exc:
                logger.error("Failed to update job status: {}", update_exc)

    async def close(self) -> None:
        """Close Redis connection"""
        if self.redis_client:
            await self.redis_client.aclose()
            logger.info("Redis connection closed")


async def main() -> None:
    """Main worker entry point"""
    model_id = os.getenv("WORKER_MODEL_ID", "qwen/image")
    redis_url = os.getenv("REDIS_URL", "redis://redis:6379")
    worker_id = os.getenv("WORKER_ID", f"worker-{os.getpid()}")

    logger.remove()
    logger.add(lambda msg: print(msg, end=""), level=os.getenv("LOG_LEVEL", "INFO"))

    logger.info(
        "Starting worker model_id={} worker_id={} redis={}",
        model_id,
        worker_id,
        redis_url,
    )

    worker = QueueWorker(model_id=model_id, redis_url=redis_url, worker_id=worker_id)

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
