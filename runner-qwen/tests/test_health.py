import pytest
from httpx import AsyncClient

from app.main import app


@pytest.mark.asyncio
async def test_healthz() -> None:
    async with AsyncClient(app=app, base_url="http://test") as client:
        resp = await client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


@pytest.mark.asyncio
async def test_invoke() -> None:
    payload = {"prompt": "generate a futuristic city", "image_count": 2}
    async with AsyncClient(app=app, base_url="http://test") as client:
        resp = await client.post("/invoke", json=payload)
    assert resp.status_code == 200
    body = resp.json()
    assert body["model_id"] == "fal-ai/fast-sdxl"
    assert len(body["outputs"]) == 2
