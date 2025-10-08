from __future__ import annotations

import asyncio
import json
import os
from typing import Any

import redis.asyncio as redis
from loguru import logger

from .backend_factory import load_backend
from .types import InvokeRequest

QUEUE_POLL_SECONDS = float(os.getenv("QUEUE_POLL_SECONDS", "1.0"))


async def main() -> None:
    backend, model_id = load_backend()

    redis_url = os.getenv("REDIS_URL", "redis://redis:6379")
    queue_key = f"queue:{model_id}"

    client = redis.from_url(redis_url, encoding="utf-8", decode_responses=True)
    logger.info("worker connected", redis=redis_url, queue=queue_key)

    try:
        while True:
            job = await client.lpop(queue_key)
            if not job:
                await asyncio.sleep(QUEUE_POLL_SECONDS)
                continue

            payload: dict[str, Any] = json.loads(job)
            logger.info("processing job", job_id=payload.get("id"))

            request = InvokeRequest(prompt=payload.get("prompt", ""), params=payload.get("params", {}))

            try:
                outputs, elapsed = await backend.generate(request)
                logger.info("job completed", job_id=payload.get("id"), elapsed=elapsed)
                # TODO: notify orchestrator completion endpoint
            except Exception as exc:  # pragma: no cover - log error, keep worker alive
                logger.exception("job failed", job_id=payload.get("id"), error=str(exc))
    finally:
        await client.aclose()


if __name__ == "__main__":
    asyncio.run(main())
