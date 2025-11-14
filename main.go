package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultPort                = "8080"
	defaultDeploymentToRestart = "registration-agent"
	restartAnnotationKey       = "kubectl.kubernetes.io/restartedAt"
)

type PatchRequest struct {
	Namespace string                 `json:"namespace"`
	Name      string                 `json:"name"`
	Patch     map[string]interface{} `json:"patch"`
	PatchType string                 `json:"patchType,omitempty"` // "merge", "strategic", "json"
}

type PatchResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"` // true if patch was skipped (no changes needed)
}

type Server struct {
	clientset *kubernetes.Clientset
}

func main() {
	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Initialize Kubernetes client
	clientset, err := initKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to initialize Kubernetes client: %v", err)
	}

	server := &Server{
		clientset: clientset,
	}

	// Setup routes
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/api/v1/event-monitoring", server.patchConfigMapHandler)

	log.Printf("Starting server on port %s", port)
	log.Printf("Health check: http://localhost:%s/health", port)
	log.Printf("Event monitoring endpoint: http://localhost:%s/api/v1/event-monitoring", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

func initKubernetesClient() (*kubernetes.Clientset, error) {
	// This will work both in-cluster and out-of-cluster
	// In-cluster: uses service account token
	// Out-of-cluster: uses ~/.kube/config
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local config for development
		log.Printf("Not running in-cluster, trying local config: %v", err)
		return nil, fmt.Errorf("in-cluster config required: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return clientset, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"service": "configmap-event-handler",
	})
}

func (s *Server) patchConfigMapHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendErrorResponse(w, http.StatusBadRequest, "Invalid JSON in request body", err.Error())
		return
	}

	// Validate request
	if req.Namespace == "" {
		sendErrorResponse(w, http.StatusBadRequest, "Missing required field", "namespace is required")
		return
	}
	if req.Name == "" {
		sendErrorResponse(w, http.StatusBadRequest, "Missing required field", "name is required")
		return
	}
	if req.Patch == nil {
		sendErrorResponse(w, http.StatusBadRequest, "Missing required field", "patch is required")
		return
	}

	// Default to merge patch type
	patchType := req.PatchType
	if patchType == "" {
		patchType = "merge"
	}

	// Check if patch is needed and apply if necessary
	needsPatch, err := s.checkIfPatchNeeded(req.Namespace, req.Name, req.Patch)
	if err != nil {
		log.Printf("Error checking ConfigMap %s/%s: %v", req.Namespace, req.Name, err)
		sendErrorResponse(w, http.StatusInternalServerError, "Failed to check ConfigMap", err.Error())
		return
	}

	var response PatchResponse
	if !needsPatch {
		// ConfigMap already has the required values, skip patching
		log.Printf("ConfigMap %s/%s already has required values, skipping patch", req.Namespace, req.Name)
		response = PatchResponse{
			Success:   true,
			Message:   "ConfigMap already has required values, no patch needed",
			Namespace: req.Namespace,
			Name:      req.Name,
			Skipped:   true,
		}
	} else {
		// Patch the ConfigMap
		err = s.patchConfigMap(req.Namespace, req.Name, req.Patch, patchType)
		if err != nil {
			log.Printf("Error patching ConfigMap %s/%s: %v", req.Namespace, req.Name, err)
			sendErrorResponse(w, http.StatusInternalServerError, "Failed to patch ConfigMap", err.Error())
			return
		}

		// Restart the registration-agent deployment after successful patch
		deploymentName := os.Getenv("RESTART_DEPLOYMENT")
		if deploymentName == "" {
			deploymentName = defaultDeploymentToRestart
		}

		restartErr := s.restartDeployment(req.Namespace, deploymentName)
		if restartErr != nil {
			// Log error but don't fail the request - patch was successful
			log.Printf("Warning: Failed to restart deployment %s/%s: %v", req.Namespace, deploymentName, restartErr)
		} else {
			log.Printf("Successfully restarted deployment %s/%s", req.Namespace, deploymentName)
		}

		response = PatchResponse{
			Success:   true,
			Message:   "ConfigMap patched successfully",
			Namespace: req.Namespace,
			Name:      req.Name,
			Skipped:   false,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// checkIfPatchNeeded checks if the ConfigMap needs to be patched by comparing current state with desired patch
func (s *Server) checkIfPatchNeeded(namespace, name string, patch map[string]interface{}) (bool, error) {
	// Get the current ConfigMap
	cm, err := s.clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Extract the data section from the patch
	patchData, ok := patch["data"].(map[string]interface{})
	if !ok {
		// If patch doesn't have data section, we'll apply it anyway
		return true, nil
	}

	// Compare current ConfigMap data with patch data
	currentData := cm.Data
	if currentData == nil {
		currentData = make(map[string]string)
	}

	// Check if all patch values already exist and match current values
	for key, value := range patchData {
		currentValue, exists := currentData[key]

		// Check if this is a YAML field update (value is a map, not a string)
		if patchMap, isMap := value.(map[string]interface{}); isMap {
			// This is a YAML field update - parse and compare YAML structures
			if !exists {
				// Key doesn't exist, patch needed
				log.Printf("ConfigMap %s/%s needs patch: key=%s doesn't exist", namespace, name, key)
				return true, nil
			}

			// Parse current YAML
			var currentYAML map[string]interface{}
			if err := yaml.Unmarshal([]byte(currentValue), &currentYAML); err != nil {
				// Not valid YAML, treat as regular string comparison
				log.Printf("ConfigMap %s/%s key=%s is not valid YAML, treating as string", namespace, name, key)
				if currentValue != fmt.Sprintf("%v", value) {
					return true, nil
				}
				continue
			}

			// Check if YAML fields need updating
			needsUpdate, err := compareYAMLFields(currentYAML, patchMap)
			if err != nil {
				return false, fmt.Errorf("failed to compare YAML fields for key %s: %w", key, err)
			}
			if needsUpdate {
				log.Printf("ConfigMap %s/%s needs patch: YAML fields in key=%s differ", namespace, name, key)
				return true, nil
			}
		} else {
			// Regular string value comparison
			var patchValueStr string
			switch v := value.(type) {
			case string:
				patchValueStr = v
			default:
				patchValueStr = fmt.Sprintf("%v", v)
			}

			// If key doesn't exist or value is different, patch is needed
			if !exists || currentValue != patchValueStr {
				log.Printf("ConfigMap %s/%s needs patch: key=%s, current=%v, desired=%v",
					namespace, name, key, currentValue, patchValueStr)
				return true, nil
			}
		}
	}

	// All values match, no patch needed
	return false, nil
}

func (s *Server) patchConfigMap(namespace, name string, patch map[string]interface{}, patchType string) error {
	// Get current ConfigMap to handle YAML merging
	cm, err := s.clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Process YAML field updates before patching
	processedPatch, err := s.processYAMLUpdates(cm.Data, patch)
	if err != nil {
		return fmt.Errorf("failed to process YAML updates: %w", err)
	}

	// Convert patch to JSON bytes
	patchBytes, err := json.Marshal(processedPatch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	// Patch the ConfigMap
	_, err = s.clientset.CoreV1().ConfigMaps(namespace).Patch(
		context.Background(),
		name,
		getPatchType(patchType),
		patchBytes,
		metav1.PatchOptions{},
	)

	if err != nil {
		return fmt.Errorf("configmap patch failed: %w", err)
	}

	log.Printf("Successfully patched ConfigMap %s/%s", namespace, name)
	return nil
}

// restartDeployment restarts a deployment by updating its pod template annotation
// This triggers Kubernetes to recreate the pods, picking up the new ConfigMap values
func (s *Server) restartDeployment(namespace, deploymentName string) error {
	// Update the pod template annotation with current timestamp
	// This will trigger a rollout restart
	restartTime := time.Now().Format(time.RFC3339)

	// Create patch to update the annotation
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						restartAnnotationKey: restartTime,
					},
				},
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	// Patch the deployment
	_, err = s.clientset.AppsV1().Deployments(namespace).Patch(
		context.Background(),
		deploymentName,
		types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)

	if err != nil {
		return fmt.Errorf("failed to patch deployment: %w", err)
	}

	log.Printf("Deployment %s/%s restart triggered (annotation updated to %s)", namespace, deploymentName, restartTime)
	return nil
}

// processYAMLUpdates processes YAML field updates by merging map values into YAML strings
func (s *Server) processYAMLUpdates(currentData map[string]string, patch map[string]interface{}) (map[string]interface{}, error) {
	// Deep copy the patch to avoid modifying the original
	patchData, ok := patch["data"].(map[string]interface{})
	if !ok {
		return patch, nil
	}

	processedData := make(map[string]interface{})

	for key, value := range patchData {
		// Check if this is a YAML field update (value is a map, not a string)
		if patchMap, isMap := value.(map[string]interface{}); isMap {
			// Get current YAML value
			currentValue, exists := currentData[key]
			if !exists {
				currentValue = ""
			}

			// Parse current YAML
			var currentYAML map[string]interface{}
			if err := yaml.Unmarshal([]byte(currentValue), &currentYAML); err != nil {
				// Not valid YAML, create new YAML from patch map
				log.Printf("Key %s doesn't contain valid YAML, creating new YAML", key)
				currentYAML = make(map[string]interface{})
			}

			// Merge patch fields into YAML structure
			mergedYAML, err := mergeYAMLFields(currentYAML, patchMap)
			if err != nil {
				return nil, fmt.Errorf("failed to merge YAML fields for key %s: %w", key, err)
			}

			// Serialize merged YAML back to string
			yamlBytes, err := yaml.Marshal(mergedYAML)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal YAML for key %s: %w", key, err)
			}

			processedData[key] = string(yamlBytes)
		} else {
			// Regular string value, keep as is
			processedData[key] = value
		}
	}

	result := make(map[string]interface{})
	for k, v := range patch {
		if k == "data" {
			result[k] = processedData
		} else {
			result[k] = v
		}
	}

	return result, nil
}

// mergeYAMLFields merges field updates into a YAML structure
// Supports dot-notation paths like "nested.field" for deep updates
func mergeYAMLFields(base map[string]interface{}, updates map[string]interface{}) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Copy base values
	for k, v := range base {
		result[k] = v
	}

	// Apply updates
	for key, value := range updates {
		// Check if key contains dot notation (nested path)
		if strings.Contains(key, ".") {
			// Handle nested field updates
			parts := strings.Split(key, ".")
			if err := setNestedField(result, parts, value); err != nil {
				return nil, fmt.Errorf("failed to set nested field %s: %w", key, err)
			}
		} else {
			// Simple field update
			result[key] = value
		}
	}

	return result, nil
}

// setNestedField sets a nested field in a map using a path of keys
func setNestedField(m map[string]interface{}, path []string, value interface{}) error {
	if len(path) == 0 {
		return fmt.Errorf("path cannot be empty")
	}

	if len(path) == 1 {
		m[path[0]] = value
		return nil
	}

	// Navigate/create nested structure
	key := path[0]
	remainingPath := path[1:]

	// Get or create nested map
	var nested map[string]interface{}
	if existing, ok := m[key]; ok {
		if existingMap, ok := existing.(map[string]interface{}); ok {
			nested = existingMap
		} else {
			// Overwrite with new map
			nested = make(map[string]interface{})
			m[key] = nested
		}
	} else {
		nested = make(map[string]interface{})
		m[key] = nested
	}

	// Recursively set nested field
	return setNestedField(nested, remainingPath, value)
}

// compareYAMLFields compares YAML structures to see if updates are needed
func compareYAMLFields(current map[string]interface{}, updates map[string]interface{}) (bool, error) {
	for key, updateValue := range updates {
		// Check if key contains dot notation (nested path)
		if strings.Contains(key, ".") {
			parts := strings.Split(key, ".")
			currentValue, exists := getNestedField(current, parts)
			if !exists {
				log.Printf("Field %s doesn't exist, update needed", key)
				return true, nil
			}
			if !valuesEqual(currentValue, updateValue) {
				log.Printf("Field %s differs: current=%v, update=%v", key, currentValue, updateValue)
				return true, nil
			}
		} else {
			// Simple field comparison
			currentValue, exists := current[key]
			if !exists {
				log.Printf("Field %s doesn't exist, update needed", key)
				return true, nil
			}
			if !valuesEqual(currentValue, updateValue) {
				log.Printf("Field %s differs: current=%v, update=%v", key, currentValue, updateValue)
				return true, nil
			}
		}
	}
	log.Printf("All YAML fields match, no update needed")
	return false, nil
}

// valuesEqual compares two values with type conversion support
// Handles cases like int(5432) == "5432" or float64(1.0) == int(1)
func valuesEqual(a, b interface{}) bool {
	// Direct equality check first
	if a == b {
		return true
	}

	// Convert both to strings and compare (handles type mismatches)
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)

	// Normalize whitespace for string comparison
	aStr = strings.TrimSpace(aStr)
	bStr = strings.TrimSpace(bStr)

	return aStr == bStr
}

// getNestedField gets a nested field from a map using a path of keys
func getNestedField(m map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}

	key := path[0]
	value, exists := m[key]
	if !exists {
		return nil, false
	}

	if len(path) == 1 {
		return value, true
	}

	// Navigate nested structure
	if nestedMap, ok := value.(map[string]interface{}); ok {
		return getNestedField(nestedMap, path[1:])
	}

	return nil, false
}

func getPatchType(patchType string) types.PatchType {
	switch patchType {
	case "merge":
		return types.MergePatchType
	case "strategic":
		return types.StrategicMergePatchType
	case "json":
		return types.JSONPatchType
	default:
		return types.MergePatchType
	}
}

func sendErrorResponse(w http.ResponseWriter, statusCode int, message, errorDetail string) {
	response := PatchResponse{
		Success: false,
		Error:   fmt.Sprintf("%s: %s", message, errorDetail),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}
