from __future__ import annotations

import time

from fastapi import FastAPI, HTTPException
from loguru import logger

from .backend import BackendResult, load_backend
from .types import HealthResponse, ImageArtifact, InvokeRequest, InvokeResponse

app = FastAPI(title="Vyvo Flux Runner", version="0.1.0")

_backend, _model_id, _backend_info = load_backend()
_ready = True


@app.on_event("startup")
async def startup_event() -> None:
    logger.info("flux runner starting", model_id=_model_id)


@app.on_event("shutdown")
async def shutdown_event() -> None:
    logger.info("flux runner shutting down", model_id=_model_id)


@app.get("/healthz", response_model=HealthResponse)
async def healthz() -> HealthResponse:
    return HealthResponse(status="ok", ready=_ready, model_id=_model_id)


@app.post("/invoke", response_model=InvokeResponse)
async def invoke(request: InvokeRequest) -> InvokeResponse:
    start = time.perf_counter()

    try:
        result: BackendResult = await _backend.generate(request)
    except HTTPException:
        raise
    except Exception as exc:
        logger.exception("flux inference failure")
        raise HTTPException(status_code=500, detail=str(exc)) from exc

    total_ms = round((time.perf_counter() - start) * 1000, 2)
    timings = {
        "inference_ms": round(result.inference_seconds * 1000, 2),
        "total_ms": total_ms,
    }

    return InvokeResponse(
        model_id=_model_id,
        request_id=result.request_id,
        outputs=result.outputs,
        timings=timings,
    )
