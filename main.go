package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type DockerClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
}

type MetricsCollector struct {
	dockerClient DockerClient
	mu           sync.RWMutex
	metricsCache map[string]string
	labelFilter  map[string]string
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
		metricsCache: make(map[string]string),
		labelFilter:  labelFilter,
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

func (mc *MetricsCollector) fetchMetrics(ctx context.Context, ip string, port string) (string, error) {
	url := fmt.Sprintf("http://%s:%s/metrics", ip, port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch metrics from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	return string(body), nil
}

func (mc *MetricsCollector) updateMetrics(ctx context.Context) {
	containers, err := mc.discoverContainers(ctx)
	if err != nil {
		log.Printf("Error discovering containers: %v", err)
		return
	}

	newMetrics := make(map[string]string)
	var wg sync.WaitGroup

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

			metrics, err := mc.fetchMetrics(ctx, containerIP, port)
			if err != nil {
				log.Printf("Error fetching metrics from container %s: %v", c.ID, err)
				return
			}

			// Apply metric filtering if specified
			if dropMetrics := c.Labels["prometheus.auto.metrics.drop"]; dropMetrics != "" {
				dropList := strings.Split(dropMetrics, ",")
				for i := range dropList {
					dropList[i] = strings.TrimSpace(dropList[i])
				}
				metrics = filterMetrics(metrics, dropList)
			}

			mc.mu.Lock()
			newMetrics[c.ID] = metrics
			mc.mu.Unlock()
		}(c)
	}

	wg.Wait()

	mc.mu.Lock()
	mc.metricsCache = newMetrics
	mc.mu.Unlock()
}

func filterMetrics(metrics string, dropList []string) string {
	if len(dropList) == 0 {
		return metrics
	}

	// Compile regex patterns
	var patterns []*regexp.Regexp
	var exactMatches []string

	for _, drop := range dropList {
		// Check if it looks like a regex pattern (contains regex metacharacters)
		if strings.ContainsAny(drop, ".*+?^$[]{}()|\\") {
			pattern, err := regexp.Compile(drop)
			if err != nil {
				log.Printf("Invalid regex pattern '%s': %v, treating as exact match", drop, err)
				exactMatches = append(exactMatches, drop)
			} else {
				patterns = append(patterns, pattern)
			}
		} else {
			exactMatches = append(exactMatches, drop)
		}
	}

	lines := strings.Split(metrics, "\n")
	var filtered []string
	var currentMetric string
	skipMetric := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip empty lines
		if trimmed == "" {
			if !skipMetric {
				filtered = append(filtered, line)
			}
			continue
		}

		// Check if it's a comment line
		if strings.HasPrefix(trimmed, "#") {
			// Extract metric name from HELP or TYPE comments
			if strings.HasPrefix(trimmed, "# HELP") || strings.HasPrefix(trimmed, "# TYPE") {
				parts := strings.Fields(trimmed)
				if len(parts) >= 3 {
					currentMetric = parts[2]
					skipMetric = false

					// Check exact matches
					for _, exact := range exactMatches {
						if currentMetric == exact {
							skipMetric = true
							break
						}
					}

					// Check regex patterns
					if !skipMetric {
						for _, pattern := range patterns {
							if pattern.MatchString(currentMetric) {
								skipMetric = true
								break
							}
						}
					}
				}
			}
			if !skipMetric {
				filtered = append(filtered, line)
			}
		} else {
			// It's a metric line
			if !skipMetric {
				// Extract metric name from the line (everything before the first space or {)
				metricName := trimmed
				if idx := strings.IndexAny(trimmed, " {"); idx != -1 {
					metricName = trimmed[:idx]
				}

				// Check exact matches
				for _, exact := range exactMatches {
					if metricName == exact {
						skipMetric = true
						break
					}
				}

				// Check regex patterns
				if !skipMetric {
					for _, pattern := range patterns {
						if pattern.MatchString(metricName) {
							skipMetric = true
							break
						}
					}
				}

				if !skipMetric {
					filtered = append(filtered, line)
				}
			}
		}
	}

	return strings.Join(filtered, "\n")
}

func (mc *MetricsCollector) aggregateMetrics() string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	var aggregated strings.Builder

	for containerID, metrics := range mc.metricsCache {
		aggregated.WriteString(fmt.Sprintf("# Metrics from container %s\n", containerID))
		aggregated.WriteString(metrics)
		if !strings.HasSuffix(metrics, "\n") {
			aggregated.WriteString("\n")
		}
		aggregated.WriteString("\n")
	}

	return aggregated.String()
}

func (mc *MetricsCollector) metricsHandler(w http.ResponseWriter, r *http.Request) {
	aggregatedMetrics := mc.aggregateMetrics()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, aggregatedMetrics)
}

func main() {
	collector, err := NewMetricsCollector()
	if err != nil {
		log.Fatalf("Failed to create metrics collector: %v", err)
	}

	reg := prometheus.NewRegistry()

	ctx := context.Background()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		collector.updateMetrics(ctx)

		for range ticker.C {
			collector.updateMetrics(ctx)
		}
	}()

	http.HandleFunc("/metrics", collector.metricsHandler)
	http.Handle("/internal/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})

	log.Println("Starting Prometheus Docker Collector on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
