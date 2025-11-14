# ConfigMap Event Handler Service

An event-driven Kubernetes service that receives events via HTTP POST requests and patches ConfigMaps accordingly. This is an internal cluster-only service that doesn't require authentication.

## Features

- ✅ Internal cluster-only service (ClusterIP)
- ✅ No authentication required (trusted internal service)
- ✅ RESTful API for patching ConfigMaps
- ✅ Health check endpoint
- ✅ Small container image (~20-50MB)
- ✅ RBAC-based permissions
- ✅ Native Kubernetes client (no kubectl binary needed)

## Architecture

```
Event Source (Internal Pod/Service)
    ↓ HTTP POST (Event)
ConfigMap Event Handler Service - ClusterIP
    ↓ Kubernetes API
ConfigMap (Patched based on event)
```

## API Endpoints

### Health Check
```bash
GET /health
```

Response:
```json
{
  "status": "healthy",
  "service": "eventhandler"
}
```

### Event Monitoring (Patch ConfigMap)
```bash
POST /api/v1/event-monitoring
```

Request Body:
```json
{
  "namespace": "default",
  "name": "my-configmap",
  "patch": {
    "data": {
      "key1": "value1",
      "key2": "value2"
    }
  },
  "patchType": "merge"
}
```

#### Updating Specific Fields in YAML Files

If a ConfigMap contains a YAML file as a string value, you can update specific fields within that YAML by providing a map object instead of a string. The service will parse the existing YAML, merge your updates, and serialize it back.

**Example:** Updating fields in a YAML configuration file

Assume your ConfigMap has:
```yaml
data:
  config.yaml: |
    app:
      name: myapp
      version: 1.0
    database:
      host: localhost
      port: 5432
```

To update specific fields, send:
```json
{
  "namespace": "default",
  "name": "my-configmap",
  "patch": {
    "data": {
      "config.yaml": {
        "app.version": "2.0",
        "database.host": "db.example.com"
      }
    }
  },
  "patchType": "merge"
}
```

This will update only the specified fields, preserving the rest of the YAML structure:
```yaml
data:
  config.yaml: |
    app:
      name: myapp
      version: 2.0
    database:
      host: db.example.com
      port: 5432
```

**Features:**
- **Dot notation support**: Use `"nested.field"` to update deeply nested fields
- **Preserves structure**: Only updates specified fields, keeps the rest intact
- **Idempotent**: Checks if updates are needed before patching
- **Automatic YAML formatting**: Reformats YAML with proper indentation

#### Automatic Deployment Restart

After successfully patching a ConfigMap, the service automatically restarts the `registration-agent` deployment in the same namespace. This ensures pods pick up the new ConfigMap values.

**Configuration:**
- Default deployment name: `registration-agent`
- Override via environment variable: `RESTART_DEPLOYMENT=<deployment-name>`
- The deployment must exist in the same namespace as the ConfigMap

**Note:** If the deployment restart fails, the ConfigMap patch is still considered successful (the restart error is logged as a warning).

**RBAC Requirements:**
The service account needs permissions to patch deployments:
```yaml
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "patch"]
```

Response (Success):
```json
{
  "success": true,
  "message": "ConfigMap patched successfully",
  "namespace": "default",
  "name": "my-configmap"
}
```

Response (Error):
```json
{
  "success": false,
  "error": "Error message"
}
```

### Patch Types
- `merge` (default) - JSON merge patch
- `strategic` - Strategic merge patch
- `json` - JSON patch (RFC 6902)

## Building

### Prerequisites
- Go 1.21+
- Podman or Docker (for building container image)
- kubectl (only needed for deployment, not for the running application)

**Note:** The application uses the native Go Kubernetes client library (`k8s.io/client-go`) and does NOT require kubectl to run. kubectl is only needed for deploying/managing the Kubernetes resources.

### Build Locally
```bash
go mod download
go build -o eventhandler .
```

### Build Container Image
```bash
# Using Podman (recommended)
podman build --platform linux/amd64 -t configmap-event-handler:latest .

# Or using Docker
docker build --platform linux/amd64 -t configmap-event-handler:latest .
```

## Deployment

### Option 1: Using Deployment Script (Recommended)
```bash
# Full deployment (build + deploy)
./deploy.sh

# Build Docker image only
./deploy.sh --build-only

# Deploy to Kubernetes only (assumes image exists)
./deploy.sh --deploy-only

# Show help
./deploy.sh --help
```

The deployment script will:
- Check prerequisites (podman/docker, kubectl, cluster access)
- Build the container image (auto-detects Podman or Docker)
- Deploy Kubernetes manifests
- Wait for deployment to be ready
- Perform health checks
- Show deployment status

### Option 2: Using kubectl
```bash
# Apply all manifests
kubectl apply -f k8s/

# Or apply individually
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/serviceaccount.yaml
kubectl apply -f k8s/role.yaml
kubectl apply -f k8s/rolebinding.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

### Option 3: Using kustomize
```bash
kubectl apply -k k8s/
```

### Update Image
If you've built a new image, update the deployment:
```bash
# Edit deployment.yaml to change image tag
kubectl apply -f k8s/deployment.yaml

# Or use kubectl set image
kubectl set image deployment/configmap-event-handler \
  configmap-event-handler=configmap-event-handler:latest \
  -n eventhandler
```

## Usage

### From Another Pod in the Cluster

#### Using curl
```bash
curl -X POST http://event-monitoring.eventhandler.svc.cluster.local/api/v1/event-monitoring \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "default",
    "name": "my-configmap",
    "patch": {
      "data": {
        "key1": "value1"
      }
    },
    "patchType": "merge"
  }'
```

#### Using Python
```python
import requests

response = requests.post(
    "http://event-monitoring.eventhandler.svc.cluster.local/api/v1/event-monitoring",
    json={
        "namespace": "default",
        "name": "my-configmap",
        "patch": {"data": {"key": "value"}},
        "patchType": "merge"
    }
)
print(response.json())
```

#### Using Go
```go
import (
    "bytes"
    "encoding/json"
    "net/http"
)

reqBody := map[string]interface{}{
    "namespace": "default",
    "name": "my-configmap",
    "patch": map[string]interface{}{
        "data": map[string]string{
            "key": "value",
        },
    },
    "patchType": "merge",
}

jsonData, _ := json.Marshal(reqBody)
resp, err := http.Post(
    "http://event-monitoring.eventhandler.svc.cluster.local/api/v1/event-monitoring",
    "application/json",
    bytes.NewBuffer(jsonData),
)
```

### Service DNS
- Full DNS: `http://event-monitoring.eventhandler.svc.cluster.local`
- Short form (same namespace): `http://event-monitoring`

## Testing Locally

### Prerequisites for Local Testing
- kubectl configured with cluster access
- Access to a Kubernetes cluster

### Run Locally (Development)
```bash
# The app requires in-cluster config, so for local testing you'd need to:
# 1. Port-forward to the cluster
# 2. Or modify the code to use local kubeconfig

# For now, build and deploy to cluster for testing
```

### Test in Cluster
```bash
# Deploy the service
kubectl apply -k k8s/

# Wait for pod to be ready
kubectl wait --for=condition=ready pod -l app=eventhandler -n eventhandler

# Port-forward to test locally
kubectl port-forward -n eventhandler svc/event-monitoring 8080:80

# Test health endpoint
curl http://localhost:8080/health

# Test patch endpoint
curl -X POST http://localhost:8080/api/v1/event-monitoring \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "default",
    "name": "test-configmap",
    "patch": {"data": {"test": "value"}}
  }'
```

## Permissions

The service uses a ServiceAccount with a Role that grants:
- `get` on ConfigMaps
- `patch` on ConfigMaps

The Role is namespace-scoped. To patch ConfigMaps in other namespaces, you would need to:
1. Create a ClusterRole instead of Role
2. Create a ClusterRoleBinding instead of RoleBinding
3. Update the RoleBinding to reference the ClusterRole

## Troubleshooting

### Check Pod Status
```bash
kubectl get pods -n eventhandler
kubectl describe pod -n eventhandler -l app=eventhandler
kubectl logs -n eventhandler -l app=eventhandler
```

### Check Service
```bash
kubectl get svc -n eventhandler
kubectl describe svc event-monitoring -n eventhandler
```

### Check RBAC
```bash
kubectl get role,rolebinding -n eventhandler
kubectl describe role eventhandler -n eventhandler
```

### Test Service from Another Pod
```bash
# Run a test pod
kubectl run -it --rm test-pod --image=curlimages/curl --restart=Never -- sh

# From inside the pod
curl http://event-monitoring.eventhandler.svc.cluster.local/health
```

## Project Structure

```
.
├── main.go              # Main application code
├── go.mod               # Go dependencies
├── Dockerfile           # Container build file (works with Podman and Docker)
├── k8s/                 # Kubernetes manifests
│   ├── namespace.yaml
│   ├── serviceaccount.yaml
│   ├── role.yaml
│   ├── rolebinding.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   └── kustomization.yaml
├── DESIGN.md            # Design document
└── README.md            # This file
```

## License

MIT

