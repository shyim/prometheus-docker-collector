# Prometheus Docker Collector

A lightweight Go service that provides HTTP Service Discovery for Prometheus to automatically discover Docker containers.

The use-case is to run the collector to discover containers running metrics endpoints and expose them via HTTP Service Discovery API. This allows Prometheus to automatically discover and scrape targets without manual configuration.

## Features

- **Auto-discovery**: Automatically finds containers with `prometheus.auto.enable=true` label
- **HTTP Service Discovery**: Provides Prometheus HTTP SD compatible endpoint
- **Custom Labels**: Expose container metadata via `prometheus.auto.label.*` labels
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

### HTTP Service Discovery

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
| `prometheus.auto.label.<name>` | No | - | Labels to expose in HTTP Service Discovery |

#### Example: Basic Setup

```bash
docker run -d \
  --label prometheus.auto.enable=true \
  --label prometheus.auto.port=9090 \
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
4. For each discovered container:
   - Gets container IP address and port
   - Creates HTTP SD target with `ip:port`
   - Extracts labels from `prometheus.auto.label.*` container labels
5. Exposes targets via `/sd` endpoint for Prometheus HTTP Service Discovery

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the LICENSE file for details.