# Prometheus Docker Collector

A lightweight Go service that automatically discovers and aggregates Prometheus metrics from Docker containers.

The use-case is to run the collector to scrape metrics from multiple containers and scrape only the collector inside your Grafana Alloy / Prometheus instance. This way you can avoid scraping each container individually and reduce the complexity of your Prometheus setup.

## Features

- **Auto-discovery**: Automatically finds containers with `prometheus.auto.enable=true` label
- **Metrics Aggregation**: Collects and aggregates metrics from multiple containers
- **Flexible Configuration**: Configure ports and filter metrics via Docker labels
- **Label-based Filtering**: Filter containers by additional labels
- **Lightweight**: ~12.4MB Docker image using Google Distroless
- **Multi-platform**: Supports linux/amd64 and linux/arm64

## Quick Start

### Using Docker

```bash
docker run -d \
  --name prometheus-collector \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  ghcr.io/shyim/prometheus-docker-collector:latest
```

### Using Docker Compose

```yaml
version: '3.8'
services:
  prometheus-collector:
    image: ghcr.io/shyim/prometheus-docker-collector:latest
    ports:
      - "8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    restart: unless-stopped
```

## Integration with Prometheus

### Option 1: Static Configuration

Add this job to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'docker-containers'
    static_configs:
      - targets: ['prometheus-collector:8080']
    relabel_configs:
      - source_labels: [__name__]
        target_label: collected_from
        replacement: docker
```

### Option 2: HTTP Service Discovery

Use HTTP SD to dynamically discover containers:

```yaml
scrape_configs:
  - job_name: 'docker-containers'
    http_sd_configs:
      - url: 'http://prometheus-collector:8080/sd'
        refresh_interval: 30s
```

This will automatically discover and scrape metrics from individual containers. Only labels defined with `prometheus.auto.label.<name>` are exposed for relabeling in Prometheus.

## Configuration

### Container Labels

To enable metric collection from a container, add these labels:

| Label | Required | Default | Description |
|-------|----------|---------|-------------|
| `prometheus.auto.enable` | Yes | - | Set to `true` to enable discovery |
| `prometheus.auto.port` | No | `80` | Port where metrics are exposed |
| `prometheus.auto.metrics.drop` | No | - | Comma-separated list of metrics to exclude |
| `prometheus.auto.label.<name>` | No | - | Labels to expose in HTTP Service Discovery |

#### Example: Basic Setup

```bash
docker run -d \
  --label prometheus.auto.enable=true \
  --label prometheus.auto.port=9090 \
  my-app:latest
```

#### Example: With Metric Filtering

```bash
docker run -d \
  --label prometheus.auto.enable=true \
  --label prometheus.auto.metrics.drop="go_.*,process_.*" \
  my-app:latest
```

#### Example: With HTTP SD Labels

```bash
docker run -d \
  --label prometheus.auto.enable=true \
  --label prometheus.auto.port=9090 \
  --label prometheus.auto.label.environment=production \
  --label prometheus.auto.label.service=api \
  --label prometheus.auto.label.team=platform \
  my-app:latest
```

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `PROMETHEUS_LABEL_FILTER` | Filter containers by additional labels | `environment=production,service=api` |
| `DOCKER_HOST` | Docker daemon socket | `tcp://docker-proxy:2375` |

## Endpoints

- `/metrics` - Aggregated metrics from all discovered containers
- `/internal/metrics` - Internal Go runtime metrics
- `/health` - Health check endpoint
- `/sd` - HTTP Service Discovery endpoint for Prometheus (returns JSON targets)

## Security Considerations

The collector requires access to the Docker socket. For production environments, consider these security enhancements:


### Read-only Socket Mount

Always mount the Docker socket as read-only:

```bash
-v /var/run/docker.sock:/var/run/docker.sock:ro
```


## How It Works

1. Connects to Docker daemon via socket
2. Lists all running containers every 30 seconds
3. Filters containers with `prometheus.auto.enable=true` label
4. Fetches metrics from each container's exposed endpoint
5. Aggregates and caches metrics
6. Serves aggregated metrics at `/metrics`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the LICENSE file for details.