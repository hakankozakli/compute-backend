from __future__ import annotations

import asyncio
import base64
import io
import os
import time
import uuid
from dataclasses import dataclass
from typing import Any, Iterable, List

from fastapi import FastAPI, HTTPException
from loguru import logger
from minio import Minio
from minio.error import S3Error
from pydantic import BaseModel, Field

try:  # Optional dependency; only needed when DashScope backend is enabled
    from dashscope import ImageSynthesis
    from dashscope.api_entities import ImageSynthesisOutput
    from dashscope.client.base import DashScopeAPIResponse
    from dashscope.error import DashScopeAPIError
except Exception:  # pragma: no cover - dashscope is optional at runtime
    ImageSynthesis = None  # type: ignore
    DashScopeAPIResponse = Any  # type: ignore
    DashScopeAPIError = Exception  # type: ignore


class ModelSettings(BaseModel):
    model_id: str = Field(default="qwen/image", description="External model identifier")
    default_steps: int = Field(default=20, ge=1, le=150)
    default_guidance: float = Field(default=8.5, ge=0.0, le=30.0)
    default_size: str = Field(default=os.getenv("QWEN_IMAGE_SIZE", "1024*1024"))


class InvokeRequest(BaseModel):
    prompt: str = Field(..., min_length=1, max_length=2048)
    negative_prompt: str | None = Field(default=None, max_length=2048)
    seed: int | None = Field(default=None, ge=0, le=2**32 - 1)
    steps: int | None = Field(default=None, ge=1, le=150)
    guidance_scale: float | None = Field(default=None, ge=0.0, le=30.0)
    image_count: int = Field(default=1, ge=1, le=4)
    size: str | None = Field(default=None, pattern=r"^\d{3,4}\*\d{3,4}$")
    width: int | None = Field(default=None, ge=512, le=2048)
    height: int | None = Field(default=None, ge=512, le=2048)


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


@dataclass
class BackendResult:
    request_id: str
    outputs: List[ImageArtifact]
    inference_seconds: float


class MinIOStorage:
    def __init__(self) -> None:
        endpoint = os.getenv("MINIO_ENDPOINT", "localhost:9000")
        access_key = os.getenv("MINIO_ACCESS_KEY", "vyvo")
        secret_key = os.getenv("MINIO_SECRET_KEY", "vyvo-secure-password-change-me")
        secure = os.getenv("MINIO_SECURE", "false").lower() in ("true", "1", "yes")
        self.bucket = os.getenv("MINIO_BUCKET", "generated-images")

        self.client = Minio(
            endpoint,
            access_key=access_key,
            secret_key=secret_key,
            secure=secure,
        )

        # Ensure bucket exists
        try:
            if not self.client.bucket_exists(self.bucket):
                self.client.make_bucket(self.bucket)
                logger.info("Created MinIO bucket: {}", self.bucket)
        except S3Error as exc:
            logger.error("Failed to check/create bucket: {}", exc)

    def upload_image(self, image_data: bytes, filename: str) -> str:
        """Upload image to MinIO and return the URL"""
        try:
            self.client.put_object(
                self.bucket,
                filename,
                io.BytesIO(image_data),
                len(image_data),
                content_type="image/png",
            )
            # Return URL using MINIO_PUBLIC_URL or construct from MINIO_ENDPOINT
            public_url = os.getenv("MINIO_PUBLIC_URL")
            if public_url:
                # Remove trailing slash if present
                public_url = public_url.rstrip('/')
                return f"{public_url}/{self.bucket}/{filename}"
            else:
                # Fallback to constructing URL from endpoint
                endpoint = os.getenv("MINIO_ENDPOINT", "localhost:9000")
                secure = os.getenv("MINIO_SECURE", "false").lower() in ("true", "1", "yes")
                protocol = "https" if secure else "http"
                return f"{protocol}://{endpoint}/{self.bucket}/{filename}"
        except S3Error as exc:
            logger.error("Failed to upload image: {}", exc)
            raise HTTPException(status_code=500, detail="Failed to upload image to storage")


class ImageBackend:
    async def generate(self, request: InvokeRequest) -> BackendResult:  # pragma: no cover - protocol
        raise NotImplementedError


class StubBackend(ImageBackend):
    def __init__(self, storage: MinIOStorage | None = None):
        self.storage = storage

    async def generate(self, request: InvokeRequest) -> BackendResult:
        from PIL import Image, ImageDraw, ImageFont

        start = time.perf_counter()
        request_id = uuid.uuid4().hex

        # Simulate some processing time
        await asyncio.sleep(0.5)

        outputs = []
        for idx in range(request.image_count):
            # Generate a simple test image with PIL
            width, height = 512, 512

            # Create image with gradient background
            img = Image.new('RGB', (width, height), color=(73, 109, 137))
            d = ImageDraw.Draw(img)

            # Draw some shapes
            d.rectangle([50, 50, width-50, height-50], fill=(200, 120, 180), outline=(255, 255, 255), width=3)
            d.ellipse([150, 150, width-150, height-150], fill=(100, 200, 150), outline=(255, 255, 255), width=3)

            # Add text
            text = f"Stub Image\nRequest: {request_id[:8]}\nPrompt: {request.prompt[:30]}..."
            d.multiline_text((width//2, height//2), text, fill=(255, 255, 255), anchor="mm")

            # Convert to bytes
            img_buffer = io.BytesIO()
            img.save(img_buffer, format='PNG')
            img_data = img_buffer.getvalue()

            # Upload to MinIO if storage is available
            if self.storage:
                filename = f"stub/{request_id}-{idx}.png"
                image_url = self.storage.upload_image(img_data, filename)
            else:
                image_url = f"https://cdn.vyvo.local/mock/{request_id}-{idx}.png"

            outputs.append(
                ImageArtifact(
                    url=image_url,
                    seed=(request.seed or 0) + idx,
                )
            )

        elapsed = time.perf_counter() - start
        logger.warning(
            "Using stub backend for request_id={} (no real backend configured)",
            request_id,
        )
        return BackendResult(request_id=request_id, outputs=outputs, inference_seconds=elapsed)


class DashScopeBackend(ImageBackend):
    def __init__(self, api_key: str, model: str, settings: ModelSettings) -> None:
        if ImageSynthesis is None:
            raise RuntimeError("dashscope package is not available")
        os.environ.setdefault("DASHSCOPE_API_KEY", api_key)
        self.model = model
        self.settings = settings

    async def generate(self, request: InvokeRequest) -> BackendResult:
        prompt = request.prompt
        negative_prompt = request.negative_prompt or ""
        image_count = request.image_count
        size = request.size or self.settings.default_size
        guidance = request.guidance_scale or self.settings.default_guidance
        steps = request.steps or self.settings.default_steps

        def _call_dashscope() -> DashScopeAPIResponse:
            return ImageSynthesis.call(  # type: ignore[operator]
                model=self.model,
                prompt=prompt,
                negative_prompt=negative_prompt,
                n=image_count,
                size=size,
                guidance_scale=guidance,
                steps=steps,
            )

        start = time.perf_counter()
        try:
            response = await asyncio.to_thread(_call_dashscope)
        except DashScopeAPIError as exc:  # pragma: no cover - relies on external API
            logger.exception("DashScope API error: {}", exc)
            raise HTTPException(status_code=502, detail="dashscope_error") from exc

        elapsed = time.perf_counter() - start
        if response.status_code != 200:
            message = getattr(response, "message", "dashscope_error")
            logger.error("DashScope returned non-200: {}", message)
            raise HTTPException(status_code=502, detail=message)

        request_id = getattr(response, "request_id", uuid.uuid4().hex)
        outputs: list[ImageArtifact] = []
        raw_output = getattr(response, "output", [])
        logger.debug("DashScope raw output size={} request_id={}", len(raw_output), request_id)

        for idx, item in enumerate(raw_output):
            url = None
            if isinstance(item, ImageSynthesisOutput):  # pragma: no cover - depends on SDK
                url = getattr(item, "url", None)
            elif isinstance(item, dict):
                url = item.get("url") or item.get("image_url")
            else:
                url = getattr(item, "url", None)

            if not url:
                logger.warning("DashScope output {} missing url, skipping", idx)
                continue

            outputs.append(
                ImageArtifact(
                    url=url,
                    seed=(request.seed or 0) + idx,
                )
            )

        if not outputs:
            logger.error("DashScope response contained no usable outputs")
            raise HTTPException(status_code=502, detail="dashscope_no_output")

        return BackendResult(request_id=request_id, outputs=outputs, inference_seconds=elapsed)


class DiffusersBackend(ImageBackend):
    def __init__(self, settings: ModelSettings, storage: MinIOStorage | None = None) -> None:
        import torch
        from diffusers import DiffusionPipeline

        model_id = os.getenv("QWEN_DIFFUSERS_MODEL", "Qwen/Qwen-Image")
        torch_dtype_str = os.getenv("QWEN_TORCH_DTYPE", "bfloat16")
        device = os.getenv("QWEN_DEVICE", "cuda")
        hf_token = os.getenv("HF_TOKEN")
        trust_remote = os.getenv("QWEN_TRUST_REMOTE_CODE", "1") not in {"0", "false", "False"}
        enable_xformers = os.getenv("QWEN_ENABLE_XFORMERS", "1") not in {"0", "false", "False"}
        enable_tf32 = os.getenv("QWEN_ENABLE_TF32", "1") not in {"0", "false", "False"}

        logger.info(
            "Loading diffusers pipeline model={} dtype={} device={} trust_remote_code={}",
            model_id,
            torch_dtype_str,
            device,
            trust_remote,
        )

        torch_dtype = getattr(torch, torch_dtype_str, torch.bfloat16)

        # Load diffusion pipeline
        # FLUX.1-dev: ~24GB VRAM (recommended for 80GB GPU)
        # Qwen/Qwen-Image: ~72GB VRAM (too large for single 80GB GPU)
        self.model_id = model_id
        self.pipeline = DiffusionPipeline.from_pretrained(
            model_id,
            torch_dtype=torch_dtype,
        ).to(device)

        if enable_xformers:
            try:
                self.pipeline.enable_xformers_memory_efficient_attention()
                logger.info("Enabled xFormers memory efficient attention")
            except Exception as exc:  # pragma: no cover - depends on build
                logger.warning("Failed to enable xFormers: {}", exc)

        if enable_tf32 and torch.cuda.is_available():  # pragma: no cover - hardware dependent
            torch.backends.cuda.matmul.allow_tf32 = True
            torch.backends.cudnn.allow_tf32 = True
        self.device = device
        self.settings = settings
        self.torch = torch
        self.storage = storage

    async def generate(self, request: InvokeRequest) -> BackendResult:
        prompt = request.prompt
        negative_prompt = request.negative_prompt or None
        steps = request.steps or self.settings.default_steps
        guidance = request.guidance_scale or self.settings.default_guidance
        image_count = request.image_count
        generator = None
        if request.seed is not None:
            generator = self.torch.Generator(device=self.device).manual_seed(int(request.seed))

        def _run_pipeline() -> Iterable[Any]:
            # Different models use different parameter names for guidance
            pipeline_kwargs = {
                "prompt": prompt,
                "num_inference_steps": steps,
                "num_images_per_prompt": image_count,
                "generator": generator,
                "width": request.width or 1024,
                "height": request.height or 1024,
            }

            # Qwen-Image uses true_cfg_scale, others use guidance_scale
            if "Qwen" in self.model_id:
                pipeline_kwargs["true_cfg_scale"] = guidance
                pipeline_kwargs["negative_prompt"] = negative_prompt or " "
            else:
                pipeline_kwargs["guidance_scale"] = guidance
                if negative_prompt:
                    pipeline_kwargs["negative_prompt"] = negative_prompt

            result = self.pipeline(**pipeline_kwargs)
            return result.images

        start = time.perf_counter()
        images = await asyncio.to_thread(_run_pipeline)
        elapsed = time.perf_counter() - start
        request_id = uuid.uuid4().hex

        outputs: list[ImageArtifact] = []
        for idx, image in enumerate(images):
            buffer = io.BytesIO()
            image.save(buffer, format="PNG")
            image_data = buffer.getvalue()

            # Upload to MinIO if available, otherwise use base64
            if self.storage:
                filename = f"{request_id}-{idx}.png"
                url = await asyncio.to_thread(self.storage.upload_image, image_data, filename)
            else:
                data = base64.b64encode(image_data).decode("utf-8")
                url = f"data:image/png;base64,{data}"

            outputs.append(
                ImageArtifact(
                    url=url,
                    seed=(request.seed or 0) + idx,
                )
            )

        return BackendResult(request_id=request_id, outputs=outputs, inference_seconds=elapsed)


def build_backend(settings: ModelSettings) -> ImageBackend:
    # Initialize MinIO storage if configured
    storage: MinIOStorage | None = None
    if os.getenv("MINIO_ENDPOINT"):
        try:
            storage = MinIOStorage()
            logger.info("MinIO storage initialized successfully")
        except Exception as exc:
            logger.warning("Failed to initialize MinIO storage, will use base64: {}", exc)

    diffusers_model = os.getenv("QWEN_DIFFUSERS_MODEL")
    if diffusers_model:
        try:
            logger.info("Using Diffusers backend with model={}", diffusers_model)
            return DiffusersBackend(settings=settings, storage=storage)
        except Exception as exc:
            logger.exception("Failed to initialize Diffusers backend: {}", exc)

    api_key = os.getenv("DASHSCOPE_API_KEY")
    if api_key:
        model = os.getenv("QWEN_IMAGE_MODEL", "wanx2.1")
        logger.info("Using DashScope backend with model={}", model)
        try:
            return DashScopeBackend(api_key=api_key, model=model, settings=settings)
        except Exception as exc:  # pragma: no cover
            logger.exception("Failed to initialize DashScope backend: {}", exc)

    logger.info("Using stub backend (no real backend configured)")
    return StubBackend(storage=storage)


app = FastAPI(title="Vyvo Qwen Image Runner", version="0.4.0")
settings = ModelSettings()
backend: ImageBackend = build_backend(settings)


@app.on_event("startup")
async def startup_event() -> None:
    logger.remove()
    logger.add(lambda msg: print(msg, end=""), level=os.getenv("LOG_LEVEL", "INFO"))
    logger.info(
        "runner starting with model_id={} backend={}",
        settings.model_id,
        backend.__class__.__name__,
    )


@app.get("/healthz")
async def healthcheck() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke", response_model=InvokeResponse)
async def invoke(request: InvokeRequest) -> InvokeResponse:
    start = time.perf_counter()
    result = await backend.generate(request)
    total_elapsed = time.perf_counter() - start

    timings = {
        "dispatch": max(round(total_elapsed - result.inference_seconds, 4), 0.0),
        "inference": round(result.inference_seconds, 4),
        "total": round(total_elapsed, 4),
    }

    logger.info(
        "invoke completed request_id={} total={:.4f}s backend={}",
        result.request_id,
        total_elapsed,
        backend.__class__.__name__,
    )

    return InvokeResponse(
        model_id=settings.model_id,
        request_id=result.request_id,
        timings=timings,
        outputs=result.outputs,
    )


if __name__ == "__main__":  # pragma: no cover
    import uvicorn

    uvicorn.run("app.main:app", host="0.0.0.0", port=int(os.getenv("PORT", "9001")), reload=True)
