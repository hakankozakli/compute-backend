.PHONY: run worker fmt lint

VENV?=.venv
PYTHON?=python3

$(VENV)/bin/activate:
	$(PYTHON) -m venv $(VENV)
	$(VENV)/bin/pip install --upgrade pip
	$(VENV)/bin/pip install -r requirements-dev.txt

run: $(VENV)/bin/activate
	FLUX_ENABLE_DIFFUSERS?=0 $(VENV)/bin/uvicorn app.main:app --reload --host 0.0.0.0 --port 9010

worker: $(VENV)/bin/activate
	FLUX_ENABLE_DIFFUSERS?=0 $(VENV)/bin/python app/worker.py

fmt: $(VENV)/bin/activate
	$(VENV)/bin/ruff format app

lint: $(VENV)/bin/activate
	$(VENV)/bin/ruff check app

