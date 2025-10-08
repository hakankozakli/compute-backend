#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <runner-name> [model-id]" >&2
  exit 1
fi

RUNNER_NAME="$1"
MODEL_ID="${2:-$1/model}"
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_DIR="$ROOT_DIR/$RUNNER_NAME"
TEMPLATE_DIR="$ROOT_DIR/runners/template"

if [[ -d "$TARGET_DIR" ]]; then
  echo "error: $TARGET_DIR already exists" >&2
  exit 1
fi

cp -R "$TEMPLATE_DIR" "$TARGET_DIR"

PYTHONPATH="" RUNNER_NAME="$RUNNER_NAME" MODEL_ID="$MODEL_ID" TARGET_DIR="$TARGET_DIR" python <<'PY'
import os
import pathlib

runner_name = os.environ['RUNNER_NAME']
model_id = os.environ['MODEL_ID']
target = pathlib.Path(os.environ['TARGET_DIR'])

replacements = {
    'template/model': model_id,
    'runner-template': runner_name,
    'Vyvo Runner Template': f"{runner_name.replace('-', ' ').title()} Runner",
}

for path in target.rglob('*'):
    if path.is_file():
        data = path.read_text()
        for needle, repl in replacements.items():
            data = data.replace(needle, repl)
        path.write_text(data)
PY

cat <<MSG
Created $RUNNER_NAME runner scaffold at $TARGET_DIR
Next steps:
  1. Edit $TARGET_DIR/app/backend_factory.py to load your model backend.
  2. Update $TARGET_DIR/requirements.txt with model-specific dependencies.
  3. Build the container with "make docker-build IMAGE=ghcr.io/<org>/$RUNNER_NAME:latest".
  4. Register the model via Admin REST (/api/models) once the runner image is pushed.
MSG
