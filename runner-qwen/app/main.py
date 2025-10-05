from __future__ import annotations

import asyncio
import os
import time
from typing import Any

from fastapi import FastAPI, HTTPException
from loguru import logger
from pydantic import BaseModel, Field


class ModelSettings(BaseModel):
    model_id: str = Field(default="fal-ai/fast-sdxl", description="External model identifier")
    default_steps: int = Field(default=20, ge=1, le=150)
    default_guidance: float = Field(default=8.5, ge=0.0, le=30.0)


class InvokeRequest(BaseModel):
    prompt: str = Field(..., min_length=1, max_length=2048)
    negative_prompt: str | None = Field(default=None, max_length=2048)
    seed: int | None = Field(default=None, ge=0, le=2**32 - 1)
    steps: int | None = Field(default=None, ge=1, le=150)
    guidance_scale: float | None = Field(default=None, ge=0.0, le=30.0)
    image_count: int = Field(default=1, ge=1, le=4)


class ImageArtifact(BaseModel):
    type: str = "image"
    url: str
    mime_type: str = "image/png"
    seed: int


class InvokeResponse(BaseModel):
    model_id: str
    request_id: str
    timings: dict[str, Any]
    outputs: list[ImageArtifact]


app = FastAPI(title="Vyvo Qwen Image Runner", version="0.1.0")
settings = ModelSettings()


@app.on_event("startup")
async def startup_event() -> None:
    logger.remove()
    logger.add(lambda msg: print(msg, end=""), level=os.getenv("LOG_LEVEL", "INFO"))
    logger.info("runner starting with model_id={}", settings.model_id)


@app.get("/healthz")
async def healthcheck() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke", response_model=InvokeResponse)
async def invoke(request: InvokeRequest) -> InvokeResponse:
    start = time.perf_counter()
    request_id = os.urandom(16).hex()
    logger.info("invoke received request_id={} prompt_length={} images={}", request_id, len(request.prompt), request.image_count)

    await asyncio.sleep(0.1)  # simulate warmup / scheduling delay

    try:
        artifacts = [
            ImageArtifact(
                url=f"https://cdn.vyvo.local/mock/{request_id}-{i}.png",
                seed=(request.seed or 0) + i,
            )
            for i in range(request.image_count)
        ]
    except Exception as exc:  # pragma: no cover - defensive logging
        logger.exception("artifact generation failed for request_id={}", request_id)
        raise HTTPException(status_code=500, detail="artifact_generation_error") from exc

    elapsed = time.perf_counter() - start
    response = InvokeResponse(
        model_id=settings.model_id,
        request_id=request_id,
        timings={
            "total": elapsed,
            "dispatch": 0.05,
            "inference": max(elapsed - 0.05, 0),
        },
        outputs=artifacts,
    )
    logger.info("invoke completed request_id={} elapsed={:.3f}s", request_id, elapsed)
    return response


if __name__ == "__main__":  # pragma: no cover
    import uvicorn

    uvicorn.run("app.main:app", host="0.0.0.0", port=int(os.getenv("PORT", "9001")), reload=True)
