from __future__ import annotations

from typing import List, Optional

from pydantic import BaseModel, Field


class HealthResponse(BaseModel):
    status: str
    ready: bool
    model_id: str


class InvokeRequest(BaseModel):
    prompt: str = Field(..., description="Primary text prompt")
    negative_prompt: Optional[str] = Field(None, description="Negative prompt text")
    steps: Optional[int] = Field(12, ge=1, le=64)
    guidance_scale: Optional[float] = Field(4.0, ge=0.0, le=20.0)
    width: Optional[int] = Field(768, ge=64, le=4096)
    height: Optional[int] = Field(768, ge=64, le=4096)
    seed: Optional[int] = Field(None, ge=0)
    image_count: Optional[int] = Field(1, ge=1, le=4)


class ImageArtifact(BaseModel):
    url: str
    seed: int


class InvokeResponse(BaseModel):
    model_id: str
    request_id: str
    outputs: List[ImageArtifact]
    timings: dict


class RunnerEvent(BaseModel):
    status: str
    message: Optional[str] = None
    logs: Optional[List[str]] = None
    result: Optional[dict] = None
    artifacts: Optional[List[dict]] = None


class WorkerConfig(BaseModel):
    redis_url: str
    model_id: str
    node_id: Optional[str] = None
    callback_url: Optional[str] = None
    callback_token: Optional[str] = None

