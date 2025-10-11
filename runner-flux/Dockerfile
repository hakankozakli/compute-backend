# syntax=docker/dockerfile:1.6
FROM pytorch/pytorch:2.5.1-cuda12.4-cudnn9-runtime as base

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/opt/app/.cache/huggingface

WORKDIR /opt/app

COPY backend/runner-flux/requirements.txt requirements.txt
RUN pip install --upgrade pip && \
    pip install --no-cache-dir -r requirements.txt

COPY backend/runner-flux/requirements-gpu.txt requirements-gpu.txt
RUN pip install --no-cache-dir -r requirements-gpu.txt

COPY backend/runner-flux/app app

CMD ["python", "-m", "app.worker"]
