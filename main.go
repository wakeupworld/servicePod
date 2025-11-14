package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultPort = "8080"
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
		// Convert patch value to string for comparison
		var patchValueStr string
		switch v := value.(type) {
		case string:
			patchValueStr = v
		default:
			// Convert other types to string
			patchValueStr = fmt.Sprintf("%v", v)
		}

		// If key doesn't exist or value is different, patch is needed
		currentValue, exists := currentData[key]
		if !exists || currentValue != patchValueStr {
			log.Printf("ConfigMap %s/%s needs patch: key=%s, current=%v, desired=%v",
				namespace, name, key, currentValue, patchValueStr)
			return true, nil
		}
	}

	// All values match, no patch needed
	return false, nil
}

func (s *Server) patchConfigMap(namespace, name string, patch map[string]interface{}, patchType string) error {
	// Convert patch to JSON bytes
	patchBytes, err := json.Marshal(patch)
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
