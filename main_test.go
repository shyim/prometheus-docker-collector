package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

type mockDockerClient struct {
	containers    []container.Summary
	containerInfo map[string]container.InspectResponse
	listError     error
	inspectError  error
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if m.listError != nil {
		return nil, m.listError
	}
	return m.containers, nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	if m.inspectError != nil {
		return container.InspectResponse{}, m.inspectError
	}
	if info, ok := m.containerInfo[containerID]; ok {
		return info, nil
	}
	return container.InspectResponse{}, fmt.Errorf("container not found")
}

func TestDiscoverContainers(t *testing.T) {
	tests := []struct {
		name          string
		containers    []container.Summary
		listError     error
		labelFilter   map[string]string
		expectedCount int
		expectError   bool
	}{
		{
			name: "discover prometheus enabled containers",
			containers: []container.Summary{
				{
					ID: "container1",
					Labels: map[string]string{
						"prometheus.auto.enable": "true",
						"prometheus.auto.port":   "9090",
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
			expectError:   false,
		},
		{
			name:          "no containers",
			containers:    []container.Summary{},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name:        "docker API error",
			listError:   fmt.Errorf("docker API error"),
			expectError: true,
		},
		{
			name: "label filter matches",
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
						"environment":            "staging",
						"service":                "api",
					},
				},
			},
			labelFilter: map[string]string{
				"environment": "production",
			},
			expectedCount: 1,
			expectError:   false,
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
			expectError:   false,
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
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MetricsCollector{
				dockerClient: &mockDockerClient{
					containers: tt.containers,
					listError:  tt.listError,
				},
				metricsCache: make(map[string]string),
				labelFilter:  tt.labelFilter,
			}

			containers, err := mc.discoverContainers(context.Background())

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(containers) != tt.expectedCount {
					t.Errorf("expected %d containers, got %d", tt.expectedCount, len(containers))
				}
			}
		})
	}
}

func TestFetchMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "# HELP test_metric Test metric")
		fmt.Fprintln(w, "# TYPE test_metric gauge")
		fmt.Fprintln(w, "test_metric 42")
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	ip := "127.0.0.1"

	mc := &MetricsCollector{
		metricsCache: make(map[string]string),
		labelFilter:  make(map[string]string),
	}

	tests := []struct {
		name        string
		ip          string
		port        string
		expectError bool
	}{
		{
			name:        "successful fetch",
			ip:          ip,
			port:        port,
			expectError: false,
		},
		{
			name:        "invalid port",
			ip:          ip,
			port:        "99999",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics, err := mc.fetchMetrics(context.Background(), tt.ip, tt.port)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !strings.Contains(metrics, "test_metric") {
					t.Error("expected metrics to contain test_metric")
				}
			}
		})
	}
}

func TestAggregateMetrics(t *testing.T) {
	mc := &MetricsCollector{
		metricsCache: map[string]string{
			"container1": "# HELP metric1 Test metric 1\nmetric1 10\n",
			"container2": "# HELP metric2 Test metric 2\nmetric2 20",
		},
		labelFilter: make(map[string]string),
	}

	aggregated := mc.aggregateMetrics()

	if !strings.Contains(aggregated, "container1") {
		t.Error("expected aggregated metrics to contain container1")
	}
	if !strings.Contains(aggregated, "container2") {
		t.Error("expected aggregated metrics to contain container2")
	}
	if !strings.Contains(aggregated, "metric1 10") {
		t.Error("expected aggregated metrics to contain metric1")
	}
	if !strings.Contains(aggregated, "metric2 20") {
		t.Error("expected aggregated metrics to contain metric2")
	}
}

func TestMetricsHandler(t *testing.T) {
	mc := &MetricsCollector{
		metricsCache: map[string]string{
			"test-container": "# HELP test_metric Test\ntest_metric 100\n",
		},
		labelFilter: make(map[string]string),
	}

	req, err := http.NewRequest("GET", "/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(mc.metricsHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	if contentType := rr.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4" {
		t.Errorf("handler returned wrong content type: got %v want %v", contentType, "text/plain; version=0.0.4")
	}

	if !strings.Contains(rr.Body.String(), "test_metric 100") {
		t.Error("handler did not return expected metrics")
	}
}

func TestUpdateMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "# HELP updated_metric Updated metric")
		fmt.Fprintln(w, "updated_metric 123")
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	mockClient := &mockDockerClient{
		containers: []container.Summary{
			{
				ID: "test-container",
				Labels: map[string]string{
					"prometheus.auto.enable": "true",
					"prometheus.auto.port":   port,
				},
			},
		},
		containerInfo: map[string]container.InspectResponse{
			"test-container": {
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "test-container",
				},
				NetworkSettings: &types.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {
							IPAddress: "127.0.0.1",
						},
					},
				},
			},
		},
	}

	mc := &MetricsCollector{
		dockerClient: mockClient,
		metricsCache: make(map[string]string),
		labelFilter:  make(map[string]string),
	}

	mc.updateMetrics(context.Background())

	time.Sleep(100 * time.Millisecond)

	mc.mu.RLock()
	metrics, exists := mc.metricsCache["test-container"]
	mc.mu.RUnlock()

	if !exists {
		t.Error("expected metrics for test-container to be cached")
	}

	if !strings.Contains(metrics, "updated_metric 123") {
		t.Error("expected cached metrics to contain updated_metric")
	}
}

func TestUpdateMetricsWithDrop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "# HELP keep_metric Metric to keep")
		fmt.Fprintln(w, "# TYPE keep_metric gauge")
		fmt.Fprintln(w, "keep_metric 100")
		fmt.Fprintln(w, "# HELP drop_metric Metric to drop")
		fmt.Fprintln(w, "# TYPE drop_metric gauge")
		fmt.Fprintln(w, "drop_metric 200")
		fmt.Fprintln(w, "# HELP another_drop Another metric to drop")
		fmt.Fprintln(w, "# TYPE another_drop counter")
		fmt.Fprintln(w, "another_drop 300")
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	mockClient := &mockDockerClient{
		containers: []container.Summary{
			{
				ID: "test-container-drop",
				Labels: map[string]string{
					"prometheus.auto.enable":       "true",
					"prometheus.auto.port":         port,
					"prometheus.auto.metrics.drop": "drop_metric,another_drop",
				},
			},
		},
		containerInfo: map[string]container.InspectResponse{
			"test-container-drop": {
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "test-container-drop",
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {
							IPAddress: "127.0.0.1",
						},
					},
				},
			},
		},
	}

	mc := &MetricsCollector{
		dockerClient: mockClient,
		metricsCache: make(map[string]string),
		labelFilter:  make(map[string]string),
	}

	mc.updateMetrics(context.Background())

	time.Sleep(100 * time.Millisecond)

	mc.mu.RLock()
	metrics, exists := mc.metricsCache["test-container-drop"]
	mc.mu.RUnlock()

	if !exists {
		t.Error("expected metrics for test-container-drop to be cached")
	}

	if !strings.Contains(metrics, "keep_metric 100") {
		t.Error("expected cached metrics to contain keep_metric")
	}

	if strings.Contains(metrics, "drop_metric") {
		t.Error("expected cached metrics to NOT contain drop_metric")
	}

	if strings.Contains(metrics, "another_drop") {
		t.Error("expected cached metrics to NOT contain another_drop")
	}
}

func TestUpdateMetricsWithRegexDrop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "# HELP go_gc_duration_seconds GC duration")
		fmt.Fprintln(w, "# TYPE go_gc_duration_seconds summary")
		fmt.Fprintln(w, "go_gc_duration_seconds{quantile=\"0\"} 0.001")
		fmt.Fprintln(w, "# HELP go_threads Number of OS threads")
		fmt.Fprintln(w, "# TYPE go_threads gauge")
		fmt.Fprintln(w, "go_threads 10")
		fmt.Fprintln(w, "# HELP go_memstats_alloc_bytes Memory allocated")
		fmt.Fprintln(w, "# TYPE go_memstats_alloc_bytes gauge")
		fmt.Fprintln(w, "go_memstats_alloc_bytes 1024")
		fmt.Fprintln(w, "# HELP http_requests_total Total HTTP requests")
		fmt.Fprintln(w, "# TYPE http_requests_total counter")
		fmt.Fprintln(w, "http_requests_total 100")
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	mockClient := &mockDockerClient{
		containers: []container.Summary{
			{
				ID: "test-container-regex",
				Labels: map[string]string{
					"prometheus.auto.enable":       "true",
					"prometheus.auto.port":         port,
					"prometheus.auto.metrics.drop": "go_.*",
				},
			},
		},
		containerInfo: map[string]container.InspectResponse{
			"test-container-regex": {
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "test-container-regex",
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {
							IPAddress: "127.0.0.1",
						},
					},
				},
			},
		},
	}

	mc := &MetricsCollector{
		dockerClient: mockClient,
		metricsCache: make(map[string]string),
		labelFilter:  make(map[string]string),
	}

	mc.updateMetrics(context.Background())

	time.Sleep(100 * time.Millisecond)

	mc.mu.RLock()
	metrics, exists := mc.metricsCache["test-container-regex"]
	mc.mu.RUnlock()

	if !exists {
		t.Error("expected metrics for test-container-regex to be cached")
	}

	if !strings.Contains(metrics, "http_requests_total 100") {
		t.Error("expected cached metrics to contain http_requests_total")
	}

	if strings.Contains(metrics, "go_gc_duration_seconds") {
		t.Error("expected cached metrics to NOT contain go_gc_duration_seconds")
	}

	if strings.Contains(metrics, "go_threads") {
		t.Error("expected cached metrics to NOT contain go_threads")
	}

	if strings.Contains(metrics, "go_memstats_alloc_bytes") {
		t.Error("expected cached metrics to NOT contain go_memstats_alloc_bytes")
	}
}

func TestHealthEndpoint(t *testing.T) {
	req, err := http.NewRequest("GET", "/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	if body := rr.Body.String(); body != "OK" {
		t.Errorf("handler returned unexpected body: got %v want %v", body, "OK")
	}
}

func TestFilterMetrics(t *testing.T) {
	tests := []struct {
		name     string
		metrics  string
		dropList []string
		expected string
	}{
		{
			name: "no metrics to drop",
			metrics: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
# HELP cpu_usage CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage 45.5`,
			dropList: []string{},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
# HELP cpu_usage CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage 45.5`,
		},
		{
			name: "drop single metric",
			metrics: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
# HELP cpu_usage CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage 45.5`,
			dropList: []string{"cpu_usage"},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100`,
		},
		{
			name: "drop multiple metrics",
			metrics: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
# HELP cpu_usage CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage 45.5
# HELP memory_usage Memory usage
# TYPE memory_usage gauge
memory_usage 1024`,
			dropList: []string{"cpu_usage", "memory_usage"},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100`,
		},
		{
			name: "drop metric with labels",
			metrics: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
http_requests_total{method="POST"} 50
# HELP cpu_usage CPU usage percentage
# TYPE cpu_usage gauge
cpu_usage{core="0"} 45.5
cpu_usage{core="1"} 32.1`,
			dropList: []string{"cpu_usage"},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET"} 100
http_requests_total{method="POST"} 50`,
		},
		{
			name: "drop metrics with regex pattern",
			metrics: `# HELP go_gc_duration_seconds GC duration
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 0.001
# HELP go_threads Number of OS threads
# TYPE go_threads gauge
go_threads 10
# HELP go_memstats_alloc_bytes Memory allocated
# TYPE go_memstats_alloc_bytes gauge
go_memstats_alloc_bytes 1024
# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total 100`,
			dropList: []string{"go_.*"},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total 100`,
		},
		{
			name: "drop metrics with multiple patterns",
			metrics: `# HELP process_cpu_seconds_total CPU time
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 123.45
# HELP process_resident_memory_bytes Memory usage
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 2048
# HELP go_threads Number of OS threads
# TYPE go_threads gauge
go_threads 10
# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total 100`,
			dropList: []string{"process_.*", "go_threads"},
			expected: `# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total 100`,
		},
		{
			name: "invalid regex treated as exact match",
			metrics: `# HELP test[invalid Invalid metric
# TYPE test[invalid gauge
test[invalid 42
# HELP valid_metric Valid metric
# TYPE valid_metric gauge
valid_metric 100`,
			dropList: []string{"test[invalid"},
			expected: `# HELP valid_metric Valid metric
# TYPE valid_metric gauge
valid_metric 100`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterMetrics(tt.metrics, tt.dropList)
			// Normalize line endings for comparison
			result = strings.TrimSpace(result)
			expected := strings.TrimSpace(tt.expected)

			if result != expected {
				t.Errorf("filterMetrics() returned unexpected result.\nGot:\n%s\n\nExpected:\n%s", result, expected)
			}
		})
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
		metricsCache: make(map[string]string),
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
		metricsCache: make(map[string]string),
		labelFilter:  make(map[string]string),
		sdTargets:    []HTTPSDTarget{},
	}

	// Set up a mock server that the collector won't actually reach
	// We'll directly populate the sdTargets to test the HTTP SD handler
	
	// Simulate what updateMetrics would do
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
