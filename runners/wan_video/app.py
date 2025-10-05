from fastapi import FastAPI

app = FastAPI(title="WAN Video Runner", version="0.1.0")


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke")
def invoke(payload: dict) -> dict:
    return {
        "status": "COMPLETED",
        "outputs": [{"type": "video", "url": "https://example.com/mock.mp4"}],
        "payload": payload,
    }
