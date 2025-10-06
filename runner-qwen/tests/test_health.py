import os

import pytest
from httpx import AsyncClient

os.environ.pop("DASHSCOPE_API_KEY", None)  # ensure stub backend for tests

from app.main import app  # noqa: E402


@pytest.mark.asyncio
async def test_healthz() -> None:
    async with AsyncClient(app=app, base_url="http://test") as client:
        resp = await client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


@pytest.mark.asyncio
async def test_invoke_stub_backend() -> None:
    payload = {"prompt": "generate a futuristic city", "image_count": 2}
    async with AsyncClient(app=app, base_url="http://test") as client:
        resp = await client.post("/invoke", json=payload)
    assert resp.status_code == 200
    body = resp.json()
    assert body["model_id"] == "qwen/image"
    assert len(body["outputs"]) == 2
    assert all("https://cdn.vyvo.local/mock" in item["url"] for item in body["outputs"])
