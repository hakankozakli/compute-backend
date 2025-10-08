from __future__ import annotations

import time
from typing import Any

from fastapi import FastAPI, HTTPException
from loguru import logger

from .backend_factory import load_backend
from .types import HealthResponse, InvokeRequest, InvokeResponse

app = FastAPI(title="Vyvo Runner Template", version="0.1.0")

_backend, _model_id = load_backend()
_ready = True


@app.on_event("startup")
async def startup_event() -> None:
    logger.info("runner starting", model_id=_model_id)


@app.on_event("shutdown")
async def shutdown_event() -> None:
    logger.info("runner shutting down", model_id=_model_id)


@app.get("/healthz", response_model=HealthResponse)
async def healthz() -> HealthResponse:
    return HealthResponse(status="ok", ready=_ready, model_id=_model_id)


@app.post("/invoke", response_model=InvokeResponse)
async def invoke(request: InvokeRequest) -> InvokeResponse:
    start = time.perf_counter()

    try:
        outputs, elapsed = await _backend.generate(request)
    except HTTPException:
        raise
    except Exception as exc:  # pragma: no cover - surface backend failure
        logger.exception("inference failure")
        raise HTTPException(status_code=500, detail=str(exc)) from exc

    timings: dict[str, Any] = {
        "total_ms": round(elapsed * 1000, 2),
        "wall_clock_ms": round((time.perf_counter() - start) * 1000, 2),
    }

    return InvokeResponse(
        model_id=_model_id,
        request_id=request.prompt[:16],
        outputs=outputs,
        timings=timings,
    )
