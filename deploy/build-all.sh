#!/bin/bash
# Build all Vyvo Compute services

set -e

echo "======================================"
echo "Building Vyvo Compute Services"
echo "======================================"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Navigate to backend root
cd "$(dirname "$0")/.."

echo -e "${BLUE}1. Building Orchestrator (Rust)...${NC}"
docker build --platform linux/amd64 -f configs/docker/Dockerfile.orchestrator -t vyvo-orchestrator:latest .
echo -e "${GREEN}✓ Orchestrator built${NC}"

echo -e "${BLUE}2. Building Gateway (Go)...${NC}"
docker build --platform linux/amd64 -f configs/docker/Dockerfile.gateway -t vyvo-gateway:latest .
echo -e "${GREEN}✓ Gateway built${NC}"

echo -e "${BLUE}3. Building Admin Dashboard (Go)...${NC}"
docker build --platform linux/amd64 -f configs/docker/Dockerfile.admin -t vyvo-admin:latest .
echo -e "${GREEN}✓ Admin built${NC}"

echo -e "${BLUE}4. Building Qwen Worker (Python)...${NC}"
cd runner-qwen
docker build --platform linux/amd64 -t ghcr.io/hakankozakli/runner-qwen:qwen-image .
echo -e "${GREEN}✓ Qwen Worker built${NC}"

echo ""
echo -e "${GREEN}======================================"
echo "All services built successfully!"
echo "======================================"${NC}
echo ""
echo "Images created:"
echo "  - vyvo-orchestrator:latest"
echo "  - vyvo-gateway:latest"
echo "  - vyvo-admin:latest"
echo "  - ghcr.io/hakankozakli/runner-qwen:qwen-image"
echo ""
echo "Next steps:"
echo "  1. Review deploy/docker-compose-production.yml"
echo "  2. Run: docker-compose -f deploy/docker-compose-production.yml up -d"
