package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type DockerClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
}

// HTTPSDTarget represents a target in Prometheus HTTP SD format
type HTTPSDTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

type MetricsCollector struct {
	dockerClient DockerClient
	mu           sync.RWMutex
	labelFilter  map[string]string
	sdTargets    []HTTPSDTarget // Cache for HTTP SD targets
}

func NewMetricsCollector() (*MetricsCollector, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	labelFilter := make(map[string]string)
	if filterEnv := os.Getenv("PROMETHEUS_LABEL_FILTER"); filterEnv != "" {
		pairs := strings.Split(filterEnv, ",")
		for _, pair := range pairs {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				labelFilter[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
		log.Printf("Using label filter: %v", labelFilter)
	}

	return &MetricsCollector{
		dockerClient: cli,
		labelFilter:  labelFilter,
		sdTargets:    []HTTPSDTarget{},
	}, nil
}

func (mc *MetricsCollector) discoverContainers(ctx context.Context) ([]container.Summary, error) {
	containers, err := mc.dockerClient.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var prometheusContainers []container.Summary
	for _, container := range containers {
		if container.Labels["prometheus.auto.enable"] != "true" {
			continue
		}

		// Check if container matches all label filters
		if len(mc.labelFilter) > 0 {
			matches := true
			for filterKey, filterValue := range mc.labelFilter {
				if labelValue, ok := container.Labels[filterKey]; !ok || labelValue != filterValue {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
		}

		prometheusContainers = append(prometheusContainers, container)
	}

	return prometheusContainers, nil
}


func (mc *MetricsCollector) updateTargets(ctx context.Context) {
	containers, err := mc.discoverContainers(ctx)
	if err != nil {
		log.Printf("Error discovering containers: %v", err)
		return
	}

	newTargets := []HTTPSDTarget{}
	var wg sync.WaitGroup
	var targetsMu sync.Mutex

	for _, c := range containers {
		wg.Add(1)
		go func(c container.Summary) {
			defer wg.Done()

			port := c.Labels["prometheus.auto.port"]
			if port == "" {
				port = "80"
			}

			containerInfo, err := mc.dockerClient.ContainerInspect(ctx, c.ID)
			if err != nil {
				log.Printf("Error inspecting container %s: %v", c.ID, err)
				return
			}

			var containerIP string
			for _, network := range containerInfo.NetworkSettings.Networks {
				if network.IPAddress != "" {
					containerIP = network.IPAddress
					break
				}
			}

			if containerIP == "" {
				log.Printf("No IP address found for container %s", c.ID)
				return
			}

			// Create HTTP SD target
			target := HTTPSDTarget{
				Targets: []string{fmt.Sprintf("%s:%s", containerIP, port)},
				Labels:  make(map[string]string),
			}

			// Only add labels that start with prometheus.auto.label.
			for k, v := range c.Labels {
				if strings.HasPrefix(k, "prometheus.auto.label.") {
					// Extract the label name after prometheus.auto.label.
					labelName := strings.TrimPrefix(k, "prometheus.auto.label.")
					if labelName != "" {
						target.Labels[labelName] = v
					}
				}
			}

			targetsMu.Lock()
			newTargets = append(newTargets, target)
			targetsMu.Unlock()
		}(c)
	}

	wg.Wait()

	mc.mu.Lock()
	mc.sdTargets = newTargets
	mc.mu.Unlock()
}


func (mc *MetricsCollector) httpSDHandler(w http.ResponseWriter, r *http.Request) {
	mc.mu.RLock()
	targets := mc.sdTargets
	mc.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	if err := json.NewEncoder(w).Encode(targets); err != nil {
		log.Printf("Error encoding HTTP SD response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func main() {
	collector, err := NewMetricsCollector()
	if err != nil {
		log.Fatalf("Failed to create metrics collector: %v", err)
	}

	ctx := context.Background()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		collector.updateTargets(ctx)

		for range ticker.C {
			collector.updateTargets(ctx)
		}
	}()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})
	http.HandleFunc("/sd", collector.httpSDHandler)

	log.Println("Starting Prometheus Docker Collector on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
