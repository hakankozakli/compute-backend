from fastapi import FastAPI

app = FastAPI(title="Qwen Image Runner", version="0.1.0")


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke")
def invoke(payload: dict) -> dict:
    # TODO: integrate with actual Qwen image generation models.
    return {
        "status": "COMPLETED",
        "outputs": [{"type": "image", "url": "https://example.com/mock.png"}],
        "payload": payload,
    }
