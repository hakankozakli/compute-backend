from fastapi import FastAPI

app = FastAPI(title="Video to Audio Runner", version="0.1.0")


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke")
def invoke(payload: dict) -> dict:
    return {
        "status": "COMPLETED",
        "outputs": [{"type": "audio", "url": "https://example.com/mock.mp3"}],
        "payload": payload,
    }
