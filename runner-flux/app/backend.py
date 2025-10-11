from __future__ import annotations

import asyncio
import base64
import io
import os
import time
import uuid
from typing import Iterable, List

from fastapi import HTTPException
from loguru import logger

from .types import ImageArtifact, InvokeRequest


class BackendResult:
    def __init__(self, request_id: str, outputs: List[ImageArtifact], elapsed: float) -> None:
        self.request_id = request_id
        self.outputs = outputs
        self.inference_seconds = elapsed


class StubBackend:
    def __init__(self) -> None:
        import PIL.Image

        self._pil = PIL.Image
        logger.warning("Using stub backend for FLUX runner; outputs are placeholders")

    async def generate(self, request: InvokeRequest) -> BackendResult:
        img = self._pil.new("RGB", (request.width or 768, request.height or 768), color=(32, 64, 96))
        buffer = io.BytesIO()
        img.save(buffer, format="PNG")
        encoded = base64.b64encode(buffer.getvalue()).decode("utf-8")
        artifact = ImageArtifact(url=f"data:image/png;base64,{encoded}", seed=request.seed or 0)
        return BackendResult(request_id=uuid.uuid4().hex, outputs=[artifact], elapsed=0.05)


class DiffusersBackend:
    def __init__(self) -> None:
        try:
            import torch
            from diffusers import DiffusionPipeline
        except Exception as exc:  # pragma: no cover - import error surfaced at runtime
            raise RuntimeError(
                "Diffusers backend requested but dependencies are missing"
            ) from exc

        model_id = os.getenv("FLUX_MODEL_ID", "black-forest-labs/FLUX.1-dev")
        dtype_name = os.getenv("FLUX_TORCH_DTYPE", "bfloat16")
        device = os.getenv("FLUX_DEVICE", "cuda")
        token = os.getenv("HF_TOKEN")

        torch_dtype = getattr(torch, dtype_name, torch.bfloat16)

        logger.info("Loading diffusers pipeline", model=model_id, dtype=dtype_name, device=device)
        self.pipeline = DiffusionPipeline.from_pretrained(
            model_id,
            torch_dtype=torch_dtype,
            use_safetensors=True,
            token=token,
        )
        self.pipeline.to(device)

        enable_xformers = os.getenv("FLUX_ENABLE_XFORMERS", "1") not in {"0", "false", "False"}
        if enable_xformers:
            try:
                self.pipeline.enable_xformers_memory_efficient_attention()
            except Exception as exc:  # pragma: no cover - depends on build
                logger.warning("Unable to enable xFormers", error=exc)
        self.device = device
        self.torch = torch

    async def generate(self, request: InvokeRequest) -> BackendResult:
        generator = None
        if request.seed is not None:
            generator = self.torch.Generator(device=self.device).manual_seed(int(request.seed))

        def _run() -> Iterable:
            return self.pipeline(
                prompt=request.prompt,
                height=request.height or 768,
                width=request.width or 768,
                num_inference_steps=request.steps or 12,
                guidance_scale=request.guidance_scale or 4.0,
                negative_prompt=request.negative_prompt,
                generator=generator,
                num_images_per_prompt=request.image_count or 1,
            ).images

        start = time.perf_counter()
        images = await asyncio.to_thread(_run)
        elapsed = time.perf_counter() - start
        request_id = uuid.uuid4().hex

        outputs: List[ImageArtifact] = []
        for idx, image in enumerate(images):
            buffer = io.BytesIO()
            image.save(buffer, format="PNG")
            encoded = base64.b64encode(buffer.getvalue()).decode("utf-8")
            outputs.append(ImageArtifact(url=f"data:image/png;base64,{encoded}", seed=(request.seed or 0) + idx))

        return BackendResult(request_id=request_id, outputs=outputs, elapsed=elapsed)


def load_backend() -> tuple[object, str, dict[str, str | None]]:
    model_id = os.getenv("VYVO_MODEL_ID", "black-forest-labs/FLUX.1-dev")
    enable_diffusers = os.getenv("FLUX_ENABLE_DIFFUSERS", "1") not in {"0", "false", "False"}
    metadata: dict[str, str | None] = {"mode": "stub", "reason": None}
    if enable_diffusers:
        try:
            backend = DiffusersBackend()
            logger.info("Diffusers backend initialised", model=model_id)
            metadata["mode"] = "diffusers"
            return backend, model_id, metadata
        except Exception as exc:
            reason = f"Diffusers backend initialisation failed: {exc}"
            metadata["reason"] = reason
            logger.exception("Failed to initialise diffusers backend; falling back to stub", error=exc)
    else:
        metadata["reason"] = "Diffusers backend disabled via FLUX_ENABLE_DIFFUSERS=0"
        logger.warning("Diffusers backend disabled; using stub backend")

    backend = StubBackend()
    if metadata["reason"] is None:
        metadata["reason"] = "Stub backend selected"
    return backend, model_id, metadata
