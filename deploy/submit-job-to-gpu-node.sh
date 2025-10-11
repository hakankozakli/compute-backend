#!/bin/bash
# Submit Job to GPU Node Redis
# This script creates a job directly in the GPU node's Redis queue

set -e

# Configuration
GPU_HOST="147.185.41.134"
GPU_PORT="20034"
GPU_USER="user"
SSH_KEY="/tmp/vyvo_gpu_key"

# Default parameters
MODEL_ID="black-forest-labs/FLUX.1-dev"
PROMPT=""
STEPS=12
GUIDANCE=4
WIDTH=768
HEIGHT=768
SEED=""

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --prompt)
      PROMPT="$2"
      shift 2
      ;;
    --steps)
      STEPS="$2"
      shift 2
      ;;
    --guidance)
      GUIDANCE="$2"
      shift 2
      ;;
    --width)
      WIDTH="$2"
      shift 2
      ;;
    --height)
      HEIGHT="$2"
      shift 2
      ;;
    --seed)
      SEED="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 --prompt \"Your prompt here\" [options]"
      echo ""
      echo "Options:"
      echo "  --prompt TEXT     Prompt for image generation (required)"
      echo "  --steps NUM       Number of diffusion steps (default: 12)"
      echo "  --guidance NUM    Guidance scale (default: 4)"
      echo "  --width NUM       Image width (default: 768)"
      echo "  --height NUM      Image height (default: 768)"
      echo "  --seed NUM        Random seed (optional)"
      echo ""
      echo "Example:"
      echo "  $0 --prompt \"A beautiful sunset\" --steps 20"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      echo "Use --help for usage information"
      exit 1
      ;;
  esac
done

# Validate prompt
if [ -z "$PROMPT" ]; then
  echo "Error: --prompt is required"
  echo "Use --help for usage information"
  exit 1
fi

# Generate job ID
JOB_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')

echo "📝 Submitting job to GPU node..."
echo "   Job ID: $JOB_ID"
echo "   Model: $MODEL_ID"
echo "   Prompt: $PROMPT"
echo "   Parameters: ${STEPS} steps, guidance ${GUIDANCE}, ${WIDTH}x${HEIGHT}"

# Create job JSON
JOB_JSON=$(cat <<EOF
{
  "id": "$JOB_ID",
  "model": "$MODEL_ID",
  "status": "pending",
  "params": {
    "prompt": "$PROMPT",
    "steps": $STEPS,
    "guidance_scale": $GUIDANCE,
    "width": $WIDTH,
    "height": $HEIGHT
    $([ -n "$SEED" ] && echo ",\"seed\": $SEED")
  },
  "created_at": $(date +%s)
}
EOF
)

# Submit to GPU node Redis
ssh -i "$SSH_KEY" -p "$GPU_PORT" "$GPU_USER@$GPU_HOST" bash <<REMOTE_SCRIPT
# Store job
sudo docker exec vyvo-redis redis-cli SET "job:$JOB_ID" '$JOB_JSON' > /dev/null

# Queue job
sudo docker exec vyvo-redis redis-cli RPUSH "queue:$MODEL_ID" "$JOB_ID" > /dev/null

echo "✅ Job queued successfully"
echo ""
echo "Waiting for processing..."
sleep 3

# Check status
JOB_DATA=\$(sudo docker exec vyvo-redis redis-cli GET "job:$JOB_ID")

STATUS=\$(echo "\$JOB_DATA" | jq -r '.status')
IMAGE_URL=\$(echo "\$JOB_DATA" | jq -r '.image_url // "pending"')
INFERENCE_TIME=\$(echo "\$JOB_DATA" | jq -r '.inference_seconds // "N/A"')

echo "📊 Job Status: \$STATUS"
echo "⚡ Processing Time: \${INFERENCE_TIME}s"
echo "🖼️  Image URL: \$IMAGE_URL"
echo ""
echo "Full details:"
echo "\$JOB_DATA" | jq '.'
REMOTE_SCRIPT

echo ""
echo "✨ Job submitted! Check the image at the URL above."
