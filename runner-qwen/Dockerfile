# syntax=docker/dockerfile:1.6
FROM python:3.11-slim AS builder

ENV VIRTUAL_ENV=/opt/venv
RUN python -m venv $VIRTUAL_ENV
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

# Install git for pip installing from GitHub
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY pyproject.toml README.md ./
RUN pip install --upgrade pip && pip install build
COPY app ./app
RUN python -m build --wheel --outdir /dist

FROM python:3.11-slim
ARG TORCH_INDEX=https://download.pytorch.org/whl/cu121
ARG TORCH_VERSION=2.6.0
ENV VIRTUAL_ENV=/opt/venv

RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        curl \
        libglib2.0-0 \
        libsm6 \
        libxext6 \
        libxrender1 \
    && rm -rf /var/lib/apt/lists/*

RUN python -m venv $VIRTUAL_ENV
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

WORKDIR /app
COPY --from=builder /dist/*.whl ./
COPY run_worker.py ./

RUN pip install --no-cache-dir --extra-index-url ${TORCH_INDEX} \
        torch==${TORCH_VERSION} torchvision==0.21.0 torchaudio==2.6.0

# Skip xformers for now - version 0.0.26.post1 downgrades torch to 2.3.0
# RUN pip install --no-cache-dir --extra-index-url ${TORCH_INDEX} xformers==0.0.26.post1 || echo "xformers install failed, continuing without it"

RUN pip install --no-cache-dir *.whl && rm *.whl

ENV PORT=9001
EXPOSE 9001
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "9001"]
