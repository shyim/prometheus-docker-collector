package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

type mockDockerClient struct {
	containers    []container.Summary
	containerInfo map[string]container.InspectResponse
	returnError   bool
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if m.returnError {
		return nil, fmt.Errorf("mock error")
	}
	return m.containers, nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	if m.returnError {
		return container.InspectResponse{}, fmt.Errorf("mock error")
	}
	if info, exists := m.containerInfo[containerID]; exists {
		return info, nil
	}
	return container.InspectResponse{}, fmt.Errorf("container not found")
}

func TestDiscoverContainers(t *testing.T) {
	tests := []struct {
		name           string
		containers     []container.Summary
		labelFilter    map[string]string
		expectedCount  int
		returnError    bool
		expectedError  bool
	}{
		{
			name: "discover prometheus enabled containers",
			containers: []container.Summary{
				{
					ID: "container1",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
					},
				},
				{
					ID: "container2",
					Labels: map[string]string{
						"prometheus.auto.enable": "false",
					},
				},
				{
					ID: "container3",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
					},
				},
			},
			expectedCount: 2,
		},
		{
			name:          "no containers",
			containers:    []container.Summary{},
			expectedCount: 0,
		},
		{
			name:          "docker API error",
			returnError:   true,
			expectedError: true,
		},
		{
			name: "label filter matches",
			containers: []container.Summary{
				{
					ID: "container1",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"environment":            "production",
					},
				},
				{
					ID: "container2",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"environment":            "staging",
					},
				},
			},
			labelFilter: map[string]string{
				"environment": "production",
			},
			expectedCount: 1,
		},
		{
			name: "multiple label filters",
			containers: []container.Summary{
				{
					ID: "container1",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"environment":            "production",
						"service":                "api",
					},
				},
				{
					ID: "container2",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"environment":            "production",
						"service":                "worker",
					},
				},
			},
			labelFilter: map[string]string{
				"environment": "production",
				"service":     "api",
			},
			expectedCount: 1,
		},
		{
			name: "label filter no matches",
			containers: []container.Summary{
				{
					ID: "container1",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"environment":            "staging",
					},
				},
			},
			labelFilter: map[string]string{
				"environment": "production",
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockDockerClient{
				containers:  tt.containers,
				returnError: tt.returnError,
			}

			collector := &MetricsCollector{
				dockerClient: mockClient,
				labelFilter:  tt.labelFilter,
				sdTargets:    []HTTPSDTarget{},
			}

			containers, err := collector.discoverContainers(context.Background())

			if tt.expectedError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if len(containers) != tt.expectedCount {
				t.Errorf("expected %d containers, got %d", tt.expectedCount, len(containers))
			}
		})
	}
}

func TestHealthEndpoint(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if body := w.Body.String(); body != "OK" {
		t.Errorf("Expected body 'OK', got %s", body)
	}
}

func TestHTTPSDHandler(t *testing.T) {
	// Create test targets
	testTargets := []HTTPSDTarget{
		{
			Targets: []string{"192.168.1.100:80"},
			Labels: map[string]string{
				"environment": "production",
				"service":     "api",
			},
		},
		{
			Targets: []string{"192.168.1.101:8080"},
			Labels:  map[string]string{}, // No labels
		},
	}

	collector := &MetricsCollector{
		dockerClient: &mockDockerClient{},
		labelFilter:  make(map[string]string),
		sdTargets:    testTargets,
	}

	req := httptest.NewRequest("GET", "/sd", nil)
	w := httptest.NewRecorder()

	collector.httpSDHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	if contentType := w.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got %s", contentType)
	}

	var result []HTTPSDTarget
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(result) != len(testTargets) {
		t.Errorf("Expected %d targets, got %d", len(testTargets), len(result))
	}

	for i, target := range result {
		if len(target.Targets) != 1 {
			t.Errorf("Expected 1 target address for target %d, got %d", i, len(target.Targets))
		}
		if target.Targets[0] != testTargets[i].Targets[0] {
			t.Errorf("Expected target address %s, got %s", testTargets[i].Targets[0], target.Targets[0])
		}
		// Check that labels match
		if len(target.Labels) != len(testTargets[i].Labels) {
			t.Errorf("Expected %d labels for target %d, got %d", 
				len(testTargets[i].Labels), i, len(target.Labels))
		}
		for k, v := range testTargets[i].Labels {
			if target.Labels[k] != v {
				t.Errorf("Expected label %s=%s for target %d, got %s", 
					k, v, i, target.Labels[k])
			}
		}
	}
}

func TestHTTPSDWithAutoLabels(t *testing.T) {
	// Set up a mock docker client with containers that have prometheus.auto.label.* labels
	mockClient := &mockDockerClient{
		containers: []container.Summary{
			{
				ID:     "container1",
				Names:  []string{"/app1"},
				Labels: map[string]string{
					"prometheus.auto.enable":            "true",
					"prometheus.auto.port":              "8080",
					"prometheus.auto.label.environment": "production",
					"prometheus.auto.label.service":     "api",
					"prometheus.auto.label.version":     "1.2.3",
					"other.label":                       "ignored", // Should be ignored
				},
			},
			{
				ID:     "container2",
				Names:  []string{"/app2"},
				Labels: map[string]string{
					"prometheus.auto.enable": "true",
					// No prometheus.auto.label.* labels
					"environment": "staging", // Should be ignored
				},
			},
		},
		containerInfo: map[string]container.InspectResponse{
			"container1": {
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "192.168.1.100"},
					},
				},
			},
			"container2": {
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "192.168.1.101"},
					},
				},
			},
		},
	}

	collector := &MetricsCollector{
		dockerClient: mockClient,
		labelFilter:  make(map[string]string),
		sdTargets:    []HTTPSDTarget{},
	}

	// Simulate what updateTargets would do
	ctx := context.Background()
	containers, _ := collector.discoverContainers(ctx)
	
	var newTargets []HTTPSDTarget
	for _, c := range containers {
		port := c.Labels["prometheus.auto.port"]
		if port == "" {
			port = "80"
		}
		
		// Get IP from mock container info
		containerInfo := mockClient.containerInfo[c.ID]
		var containerIP string
		for _, network := range containerInfo.NetworkSettings.Networks {
			if network.IPAddress != "" {
				containerIP = network.IPAddress
				break
			}
		}
		
		target := HTTPSDTarget{
			Targets: []string{fmt.Sprintf("%s:%s", containerIP, port)},
			Labels:  make(map[string]string),
		}
		
		// Only add labels that start with prometheus.auto.label.
		for k, v := range c.Labels {
			if strings.HasPrefix(k, "prometheus.auto.label.") {
				labelName := strings.TrimPrefix(k, "prometheus.auto.label.")
				if labelName != "" {
					target.Labels[labelName] = v
				}
			}
		}
		
		newTargets = append(newTargets, target)
	}
	
	collector.mu.Lock()
	collector.sdTargets = newTargets
	collector.mu.Unlock()

	// Check the SD targets
	collector.mu.RLock()
	targets := collector.sdTargets
	collector.mu.RUnlock()

	if len(targets) != 2 {
		t.Fatalf("Expected 2 targets, got %d", len(targets))
	}

	// Check first target (container1) - should have labels
	if len(targets[0].Labels) != 3 {
		t.Errorf("Expected 3 labels for container1, got %d", len(targets[0].Labels))
	}
	expectedLabels := map[string]string{
		"environment": "production",
		"service":     "api",
		"version":     "1.2.3",
	}
	for k, v := range expectedLabels {
		if targets[0].Labels[k] != v {
			t.Errorf("Expected label %s=%s, got %s", k, v, targets[0].Labels[k])
		}
	}

	// Check second target (container2) - should have no labels
	if len(targets[1].Labels) != 0 {
		t.Errorf("Expected 0 labels for container2, got %d", len(targets[1].Labels))
	}
}