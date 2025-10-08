IMAGE ?= ghcr.io/vyvo/runner-template:latest

.PHONY: install run lint docker-build

install:
	python -m pip install -r requirements.txt

run:
	uvicorn app.main:app --host 0.0.0.0 --port $${PORT:-9001}

docker-build:
	docker build -t $(IMAGE) .
