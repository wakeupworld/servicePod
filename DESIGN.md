# ConfigMap Event Handler Service - Design Document

## Overview
An event-driven Kubernetes service that receives events via HTTP POST requests and patches ConfigMaps accordingly. The service acts as an event handler that processes incoming events and executes ConfigMap patches based on those events.

**Access Model:** Internal cluster-only service, accessible within the cluster without authentication (trusted internal service).

**Event-Driven:** This service is designed to receive events and react by patching ConfigMaps, making it suitable for event-driven architectures.

## Architecture

### High-Level Design
```
Event Source (Internal Pod/Service)
    ↓ HTTP POST (Event - no auth required)
ConfigMap Event Handler Service - ClusterIP
    ↓ Kubernetes API
Kubernetes API Server
    ↓
ConfigMap (Patched based on event)
```

### Network Access
- **Service Type:** ClusterIP (internal only)
- **Access:** Only accessible from within the cluster
- **Authentication:** None required (trusted internal service)
- **DNS:** Accessible via service name: `http://eventhandler.<namespace>.svc.cluster.local`

### Components

#### 1. **API Server**
- **Technology Options:**
  - Python (Flask/FastAPI) - Simple, good for quick development
  - Go (Gin/Echo) - Lightweight, good performance
  - Node.js (Express) - JavaScript ecosystem
  - Rust (Actix/Axum) - High performance, memory safe

- **Responsibilities:**
  - Listen for HTTP POST requests (internal cluster only)
  - Validate request payload
  - Patch ConfigMaps using native Kubernetes client library
  - Return appropriate HTTP responses

#### 2. **Kubernetes Client Integration**
- **Implementation:** Uses native Kubernetes client library (no kubectl binary needed)
  - **Go:** `k8s.io/client-go` (official Kubernetes client)
  - **Connection:** Uses ServiceAccount token when running in-cluster
  - **Benefits:**
    - No kubectl binary required in container
    - Direct API communication
    - Better error handling
    - Type-safe operations
    - Smaller container image

#### 3. **Service Account & RBAC**
- ServiceAccount with appropriate permissions
- Role/RoleBinding or ClusterRole/ClusterRoleBinding
- Permissions needed:
  - `configmaps` resource: `get`, `patch` (and optionally `update`)

## API Design

### Endpoint Specification

#### POST `/api/v1/event-monitoring`

**Request:**
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
  "patchType": "merge"  // or "strategic", "json"
}
```

**Response (Success - 200):**
```json
{
  "success": true,
  "message": "ConfigMap patched successfully",
  "namespace": "default",
  "name": "my-configmap"
}
```

**Response (Error - 400/404/500):**
```json
{
  "success": false,
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

### Alternative: RESTful Design
```
POST /api/v1/namespaces/{namespace}/configmaps/{name}/patch
```

## Security Considerations

### 1. **Network Security (Internal Only)**
- **Service Type:** ClusterIP (not exposed externally)
- **Network Policies (Optional):** Restrict which pods can access the service
  ```yaml
  # Example: Only allow pods with specific labels
  ingress:
  - from:
    - podSelector:
        matchLabels:
          app: allowed-service
  ```
- **No TLS Required:** HTTP is acceptable for internal cluster communication
- **DNS-based Access:** Use Kubernetes service DNS for discovery

### 2. **RBAC Authorization**
- ServiceAccount with minimal required permissions
- Role/RoleBinding restricts what ConfigMaps can be patched
- Application-level validation (validate namespace/name patterns)
- **No application-level authentication** (trusted internal service)

### 3. **Input Validation**
- Validate namespace exists
- Validate ConfigMap name format (Kubernetes naming conventions)
- Validate patch payload structure
- Sanitize input to prevent injection attacks
- Optional: Whitelist allowed namespaces

### 4. **Rate Limiting (Optional)**
- Consider rate limiting per source IP/pod
- Prevent abuse from misconfigured services

## Container Design

### Base Image Options
- **Option A:** Use official kubectl image + add API server
- **Option B:** Use language-specific base image (python:alpine, golang:alpine)
- **Option C:** Distroless images for security

### Required Components in Container
1. API server binary/script
2. Kubernetes client library or kubectl binary
3. ServiceAccount token (mounted at `/var/run/secrets/kubernetes.io/serviceaccount/`)
4. CA certificate (for Kubernetes API)

### Environment Variables
- `KUBERNETES_SERVICE_HOST` (auto-provided)
- `KUBERNETES_SERVICE_PORT` (auto-provided)
- `LOG_LEVEL` (optional)
- `ALLOWED_NAMESPACES` (optional, for filtering)

## Deployment Configuration

### Kubernetes Resources Needed

#### 1. **ServiceAccount**
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eventhandler
  namespace: <namespace>
```

#### 2. **Role/RoleBinding**
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: eventhandler
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: eventhandler
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: eventhandler
subjects:
- kind: ServiceAccount
  name: eventhandler
```

#### 3. **Deployment**
- Replicas: 1-3 (depending on load)
- Resource limits/requests
- Health checks (liveness/readiness probes)

#### 4. **Service**
- **Type:** ClusterIP (internal only)
- **Port:** HTTP (80) or custom port
- **DNS:** `eventhandler.<namespace>.svc.cluster.local`
- **Access:** Only from within cluster

## Implementation Approaches

### Approach 1: Simple Script with kubectl
**Pros:**
- Quick to implement
- Uses familiar kubectl commands

**Cons:**
- Requires kubectl binary in container
- Less control over error handling
- Security concerns (shell execution)

### Approach 2: Native Kubernetes Client Library (Recommended)
**Pros:**
- Better error handling
- Type safety
- No shell execution
- More secure
- Better performance

**Cons:**
- Requires more code
- Language-specific implementation

## Error Handling

### Scenarios to Handle
1. ConfigMap doesn't exist → 404
2. Namespace doesn't exist → 404
3. Invalid patch format → 400
4. Permission denied → 403
5. Kubernetes API unavailable → 503
6. Network timeouts → 504
7. Invalid JSON → 400

## Logging & Observability

### Logging
- Structured logging (JSON format)
- Log all patch operations (audit trail)
- Include: timestamp, namespace, configmap name, requester info

### Metrics (Optional)
- Request count
- Success/failure rates
- Latency
- ConfigMaps patched per namespace

### Tracing (Optional)
- Distributed tracing for debugging

## Testing Strategy

### Unit Tests
- API endpoint validation
- Request parsing
- Error handling

### Integration Tests
- Test against real Kubernetes cluster (kind/minikube)
- Test RBAC permissions
- Test various patch scenarios

### E2E Tests
- Full flow: POST request → ConfigMap patched → Verify

## Deployment Options

### Option 1: Standalone Pod
- Single pod in cluster
- Accessed via Service

### Option 2: Sidecar Pattern
- Deployed alongside other services
- Shared ServiceAccount

### Option 3: Operator Pattern
- More complex, but provides CRD-based management
- Better for advanced use cases

## Future Enhancements

1. **Webhook Support**
   - Support for Kubernetes webhooks
   - Event-driven patching

2. **Batch Operations**
   - Patch multiple ConfigMaps in one request
   - Transaction support

3. **Patch History**
   - Store patch history
   - Rollback capability

4. **Validation Webhooks**
   - Validate patches before applying
   - Custom validation rules

5. **Multi-cluster Support**
   - Patch ConfigMaps across clusters

## Recommended Technology Stack

### **Recommended: Go** ✅
**Why Go is ideal for this service:**
- **Official Kubernetes Client:** `k8s.io/client-go` is the official, well-maintained client
- **Small Container Images:** Can produce tiny distroless images (~20MB)
- **Single Binary:** No runtime dependencies, easy to deploy
- **Performance:** Fast startup, low memory footprint
- **Kubernetes Native:** Go is the language Kubernetes itself is written in
- **Type Safety:** Strong typing helps prevent errors
- **Concurrency:** Built-in goroutines if you need async operations later
- **Standard Library:** Can use `net/http` (no framework needed) or lightweight Gin

**Stack:**
- **Language:** Go 1.21+
- **Framework:** Standard `net/http` (simple) or Gin (if you want routing helpers)
- **K8s Client:** `k8s.io/client-go`
- **Base Image:** `golang:alpine` (build) → `distroless/static` or `alpine` (runtime)
- **Size:** Final image ~20-50MB

### Alternative: Python (for quick prototyping)
- **Language:** Python 3.11+
- **Framework:** FastAPI
- **K8s Client:** `kubernetes` Python library
- **Base Image:** `python:3.11-slim`
- **Pros:** Faster to prototype, easier syntax
- **Cons:** Larger images (~100-200MB), slower startup, more dependencies

## Example Request Flow

```
1. Internal pod/service sends POST request
   POST http://event-monitoring.default.svc.cluster.local/api/v1/event-monitoring
   (No authentication headers required)
   Body: { namespace, name, patch }

2. API Server validates:
   - Request format
   - Namespace/name validity
   - Patch payload structure

3. API Server executes:
   k8sClient.CoreV1().ConfigMaps(namespace).Patch(name, patchType, patchData)
   (Uses native Go Kubernetes client - no kubectl binary needed)

4. API Server responds:
   - Success: 200 with confirmation
   - Error: Appropriate HTTP status with error details
```

### Example Client Call (from another pod)
```bash
# From within cluster
curl -X POST http://event-monitoring.default.svc.cluster.local/api/v1/event-monitoring \
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

## Service Discovery & Access

### DNS Name
- **Format:** `http://<service-name>.<namespace>.svc.cluster.local`
- **Example:** `http://event-monitoring.default.svc.cluster.local`
- **Short form:** `http://event-monitoring` (if in same namespace)

### Access from Other Pods
```python
# Python example
import requests
response = requests.post(
    "http://event-monitoring.default.svc.cluster.local/api/v1/event-monitoring",
    json={
        "namespace": "default",
        "name": "my-configmap",
        "patch": {"data": {"key": "value"}},
        "patchType": "merge"
    }
)
```

```bash
# Shell/curl example
curl -X POST http://event-monitoring.default.svc.cluster.local/api/v1/event-monitoring \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{
  "namespace": "default",
  "name": "my-configmap",
  "patch": {"data": {"key": "value"}},
  "patchType": "merge"
}
EOF
```

## Questions to Consider

1. **Namespace Scope:**
   - Single namespace?
   - Multiple namespaces?
   - Cluster-wide?

2. **Scale:**
   - Expected request volume?
   - Need high availability?
   - How many replicas?

3. **Patch Types:**
   - Only merge patches?
   - Support strategic merge?
   - Support JSON patches?

4. **Network Policies:**
   - Restrict which pods can access the service?
   - Or allow all pods in cluster?

5. **Audit Requirements:**
   - Need audit logging?
   - Log all patch operations?
   - Compliance requirements?

