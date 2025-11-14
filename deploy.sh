#!/bin/bash

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
IMAGE_NAME="configmap-event-handler"
IMAGE_TAG="${IMAGE_TAG:-latest}"
NAMESPACE="eventhandler"
SERVICE_NAME="event-monitoring"
K8S_DIR="k8s"

# Functions
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_prerequisites() {
    print_info "Checking prerequisites..."
    
    # Check for podman or docker
    if command -v podman &> /dev/null; then
        CONTAINER_CMD="podman"
        print_info "Using Podman"
    elif command -v docker &> /dev/null; then
        CONTAINER_CMD="docker"
        print_info "Using Docker"
    else
        print_error "Neither Podman nor Docker is installed or not in PATH"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl is not installed or not in PATH"
        exit 1
    fi
    
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster. Check your kubeconfig"
        exit 1
    fi
    
    print_info "Prerequisites check passed ✓"
}

build_image() {
    print_info "Building container image: ${IMAGE_NAME}:${IMAGE_TAG}"
    print_info "Target platform: linux/amd64"
    
    # Use --platform to ensure amd64 build even on ARM machines
    if ${CONTAINER_CMD} build --platform linux/amd64 -t "${IMAGE_NAME}:${IMAGE_TAG}" .; then
        print_info "Container image built successfully ✓"
    else
        print_error "Failed to build container image"
        exit 1
    fi
}

load_image_to_kind() {
    if command -v kind &> /dev/null && kubectl config current-context | grep -q kind; then
        print_info "Detected kind cluster, loading image..."
        # kind load works with both docker and podman images
        if [ "$CONTAINER_CMD" = "podman" ]; then
            # Podman: save image and load into kind
            print_info "Using Podman, saving image to temporary file..."
            TEMP_IMAGE_FILE=$(mktemp)
            podman save "${IMAGE_NAME}:${IMAGE_TAG}" -o "${TEMP_IMAGE_FILE}"
            kind load image-archive "${TEMP_IMAGE_FILE}" || print_warn "Failed to load image to kind (may need manual load)"
            rm -f "${TEMP_IMAGE_FILE}"
        else
            kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" || print_warn "Failed to load image to kind (may need manual load)"
        fi
    fi
}

deploy_k8s() {
    print_info "Deploying Kubernetes manifests..."
    
    # Update image in deployment if using kustomize or apply directly
    if [ -f "${K8S_DIR}/kustomization.yaml" ]; then
        print_info "Using kustomize to deploy..."
        kubectl apply -k "${K8S_DIR}/"
    else
        print_info "Applying manifests directly..."
        kubectl apply -f "${K8S_DIR}/"
    fi
    
    print_info "Kubernetes manifests applied ✓"
}

wait_for_deployment() {
    print_info "Waiting for deployment to be ready..."
    
    if kubectl wait --for=condition=available --timeout=300s deployment/configmap-event-handler -n "${NAMESPACE}" 2>/dev/null; then
        print_info "Deployment is ready ✓"
    else
        print_error "Deployment failed to become ready"
        print_info "Checking pod status..."
        kubectl get pods -n "${NAMESPACE}" -l app=configmap-event-handler
        exit 1
    fi
}

check_health() {
    print_info "Checking service health..."
    
    # Port forward in background
    kubectl port-forward -n "${NAMESPACE}" svc/${SERVICE_NAME} 8080:80 &
    PF_PID=$!
    
    # Wait for port forward to be ready
    sleep 3
    
    # Check health endpoint
    if curl -s http://localhost:8080/health > /dev/null; then
        print_info "Health check passed ✓"
        HEALTH_RESPONSE=$(curl -s http://localhost:8080/health)
        echo "Response: ${HEALTH_RESPONSE}"
    else
        print_warn "Health check failed (service may still be starting)"
    fi
    
    # Kill port forward
    kill $PF_PID 2>/dev/null || true
}

show_status() {
    print_info "Deployment status:"
    echo ""
    kubectl get all -n "${NAMESPACE}" -l app=configmap-event-handler
    echo ""
    print_info "Service endpoint:"
    echo "  http://${SERVICE_NAME}.${NAMESPACE}.svc.cluster.local"
}

# Main execution
main() {
    print_info "Starting deployment process..."
    echo ""
    
    check_prerequisites
    echo ""
    
    build_image
    echo ""
    
    load_image_to_kind
    echo ""
    
    deploy_k8s
    echo ""
    
    wait_for_deployment
    echo ""
    
    check_health
    echo ""
    
    show_status
    echo ""
    
    print_info "Deployment completed successfully! ✓"
    print_info "Service is available at: http://${SERVICE_NAME}.${NAMESPACE}.svc.cluster.local"
}

# Handle script arguments
case "${1:-}" in
    --build-only)
        check_prerequisites
        build_image
        ;;
    --deploy-only)
        check_prerequisites
        deploy_k8s
        wait_for_deployment
        show_status
        ;;
    --help|-h)
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  (no args)     Full deployment (build + deploy)"
        echo "  --build-only  Build Docker image only"
        echo "  --deploy-only Deploy to Kubernetes only (assumes image exists)"
        echo "  --help, -h    Show this help message"
        echo ""
        echo "Environment variables:"
        echo "  IMAGE_TAG     Docker image tag (default: latest)"
        echo ""
        exit 0
        ;;
    *)
        main
        ;;
esac

