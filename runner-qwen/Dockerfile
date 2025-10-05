# syntax=docker/dockerfile:1.6
FROM python:3.11-slim AS builder

ENV VIRTUAL_ENV=/opt/venv
RUN python -m venv $VIRTUAL_ENV
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

WORKDIR /app
COPY pyproject.toml README.md ./
RUN pip install --upgrade pip && pip install build
COPY app ./app
RUN python -m build --wheel --outdir /dist

FROM python:3.11-slim
ENV VIRTUAL_ENV=/opt/venv
RUN python -m venv $VIRTUAL_ENV
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

WORKDIR /app
COPY --from=builder /dist/*.whl ./
RUN pip install --no-cache-dir *.whl && rm *.whl

ENV PORT=9001
EXPOSE 9001
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "9001"]
