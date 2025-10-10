from __future__ import annotations

from typing import Any, List, Optional

from pydantic import BaseModel, Field


class InvokeRequest(BaseModel):
    prompt: str = Field(..., min_length=1, max_length=4096)
    params: dict[str, Any] = Field(default_factory=dict)


class Artifact(BaseModel):
    type: str = Field(default="artifact")
    url: str | None = None
    mime_type: str = Field(default="application/json")
    metadata: dict[str, Any] = Field(default_factory=dict)


class InvokeResponse(BaseModel):
    model_id: str
    request_id: str
    outputs: List[Artifact]
    timings: dict[str, Any]


class HealthResponse(BaseModel):
    status: str = "ok"
    model_id: str
    ready: bool
    message: Optional[str] = None
