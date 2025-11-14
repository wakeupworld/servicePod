.PHONY: build docker-build deploy test clean

# Build the Go binary
build:
	go mod download
	go build -o configmap-event-handler .

# Detect container runtime (podman or docker)
CONTAINER_CMD := $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null || echo "docker")

# Build container image (explicitly for linux/amd64)
docker-build:
	$(CONTAINER_CMD) build --platform linux/amd64 -t configmap-event-handler:latest .

# Alias for podman-build (same as docker-build)
podman-build: docker-build

# Deploy to Kubernetes (using deployment script)
deploy:
	./deploy.sh

# Deploy using kubectl directly
deploy-kubectl:
	kubectl apply -k k8s/

# Delete from Kubernetes
undeploy:
	kubectl delete -k k8s/

# Test health endpoint (requires port-forward)
test:
	curl http://localhost:8080/health

# Clean build artifacts
clean:
	rm -f configmap-event-handler
	go clean

# Run locally (requires in-cluster config or modification)
run:
	go run main.go

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run || true

