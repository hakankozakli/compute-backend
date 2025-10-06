.PHONY: install test run docker-build docker-run

TORCH_VERSION ?= 2.3.1
TORCH_INDEX ?= https://download.pytorch.org/whl/cu121

install:
	python -m venv .venv
	. .venv/bin/activate && pip install --upgrade pip
	. .venv/bin/activate && pip install --extra-index-url $(TORCH_INDEX) torch==$(TORCH_VERSION) torchvision==0.18.1 torchaudio==2.3.1
	. .venv/bin/activate && pip install -e .[test]

test:
	. .venv/bin/activate && pytest

run:
	. .venv/bin/activate && uvicorn app.main:app --host 0.0.0.0 --port 9001 --reload

docker-build:
	docker buildx build --platform linux/amd64 -t runner-qwen:local .

docker-run:
	docker run --rm --gpus all -p 9001:9001 runner-qwen:local
