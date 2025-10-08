#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 5 ]]; then
  echo "usage: $0 <builder-url> <template> <runner-name> <model-id> <version> [repo-url] [runner-image] [weights-uri]" >&2
  exit 1
fi

BUILDER_URL="$1"
TEMPLATE="$2"
RUNNER_NAME="$3"
MODEL_ID="$4"
VERSION="$5"
REPOSITORY=${6:-"https://github.com/your-org/${RUNNER_NAME}"}
RUNNER_IMAGE=${7:-"ghcr.io/your-org/${RUNNER_NAME}:${VERSION}"}
WEIGHTS_URI=${8:-""}

payload=$(cat <<JSON
{
  "template": "${TEMPLATE}",
  "runner_name": "${RUNNER_NAME}",
  "model_id": "${MODEL_ID}",
  "version": "${VERSION}",
  "repository": "${REPOSITORY}",
  "runner_image": "${RUNNER_IMAGE}",
  "weights_uri": "${WEIGHTS_URI}",
  "env": {}
}
JSON
)

curl -fsSL -X POST "${BUILDER_URL%/}/api/builds" \
  -H 'Content-Type: application/json' \
  -d "${payload}"
