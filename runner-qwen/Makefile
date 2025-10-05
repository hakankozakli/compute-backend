.PHONY: install test run docker-build

install:
	python -m venv .venv
	. .venv/bin/activate && pip install -e .[test]

test:
	. .venv/bin/activate && pytest

run:
	. .venv/bin/activate && uvicorn app.main:app --host 0.0.0.0 --port 9001 --reload

docker-build:
	docker build -t runner-qwen:local .
