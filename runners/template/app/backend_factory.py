from __future__ import annotations

import os
from typing import Protocol

from loguru import logger

from .types import Artifact, InvokeRequest


class ModelBackend(Protocol):
    async def generate(self, request: InvokeRequest) -> tuple[list[Artifact], float]:
        """Run inference and return artifacts with the elapsed runtime in seconds."""


class StubBackend:
    """Fallback backend that returns canned responses useful for smoke tests."""

    def __init__(self, model_id: str) -> None:
        self.model_id = model_id

    async def generate(self, request: InvokeRequest) -> tuple[list[Artifact], float]:
        artifact = Artifact(
            url=f"https://cdn.vyvo.local/mock/{self.model_id}/{request.prompt[:32].strip().replace(' ', '_')}.json",
            metadata={"prompt": request.prompt, "params": request.params},
        )
        return [artifact], 0.1


def load_backend() -> tuple[ModelBackend, str]:
    """Instantiate the model backend based on environment configuration.

    Returns a tuple of (backend, model_id).
    Replace the stub logic with your actual model initialisation.
    """

    model_id = os.getenv("VYVO_MODEL_ID", "template/model")
    backend_type = os.getenv("RUNNER_BACKEND", "stub").lower()

    if backend_type == "stub":
        logger.warning("RUNNER_BACKEND=stub: returning StubBackend for %s", model_id)
        return StubBackend(model_id), model_id

    raise RuntimeError(
        "No backend configured. Set RUNNER_BACKEND to a supported value and implement load_backend()."
    )
