# Project: Prometheus Docker Collector

## Overview
This is a Go webserver that provides HTTP Service Discovery for Prometheus to automatically discover Docker containers. It finds containers with specific labels and exposes them as targets through the HTTP SD API.

## Key Features
- Discovers containers with `prometheus.auto.enable=true` label
- Exposes container targets via HTTP Service Discovery API
- Supports custom labels via `prometheus.auto.label.*` 
- Updates target list every 30 seconds
- Provides health check endpoint

## Architecture
- **Language**: Go 1.24
- **Dependencies**: 
  - Docker SDK for Go (v28.2.2)
- **Design**: Uses interfaces for Docker client to enable testing

## Docker Labels
Containers must have these labels to be discovered:
- `prometheus.auto.enable=true` - Required to enable discovery
- `prometheus.auto.port=<port>` - Optional, defaults to 80
- `prometheus.auto.label.<name>=<value>` - Optional, labels to expose in HTTP Service Discovery. Only labels with this prefix are exposed in the `/sd` endpoint.
  
  Example:
  ```bash
  docker run -d \
    --label prometheus.auto.enable=true \
    --label prometheus.auto.port=9090 \
    --label prometheus.auto.label.environment=production \
    --label prometheus.auto.label.service=api \
    --label prometheus.auto.label.version=1.2.3 \
    my-app:latest
  ```

## Endpoints
- `/health` - Health check endpoint
- `/sd` - HTTP Service Discovery endpoint for Prometheus (returns JSON targets)

## Development Commands
```bash
# Build the application
go build

# Run tests
go test -v

# Run tests with coverage
go test -v -cover

# Run the application
./prometheus-docker-collector
```

## Docker Usage

### Building the Docker Image
```bash
docker build -t prometheus-docker-collector:latest .
```

### Running with Docker
```bash
# Basic run
docker run -d \
  --name prometheus-collector \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  prometheus-docker-collector:latest

# With environment variables
docker run -d \
  --name prometheus-collector \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e PROMETHEUS_LABEL_FILTER="environment=production" \
  prometheus-docker-collector:latest
```

### Docker Image Details
- Base image: Google Distroless (gcr.io/distroless/static:latest)
- Size: ~12.4MB
- Runs as root user (required for Docker socket access)
- Includes CA certificates for HTTPS support
- Multi-stage build for minimal size

### Security Considerations
The container runs as root to access the Docker socket. For enhanced security in production:

1. **Use Docker Socket Proxy**: Deploy a separate container like `docker-socket-proxy` that filters Docker API calls
2. **Adjust Host Permissions**: Add a `docker` group with appropriate GID and modify socket permissions
3. **Docker Rootless Mode**: Use Docker's rootless mode where possible
4. **Network Isolation**: Run in a restricted network with limited access

Example with docker-socket-proxy:
```bash
# Run socket proxy first
docker run -d \
  --name docker-socket-proxy \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e CONTAINERS=1 \
  --network prometheus-net \
  tecnativa/docker-socket-proxy

# Run collector connecting to proxy
docker run -d \
  --name prometheus-collector \
  -p 8080:8080 \
  -e DOCKER_HOST=tcp://docker-socket-proxy:2375 \
  --network prometheus-net \
  prometheus-docker-collector:latest
```

## Testing
The project includes comprehensive tests covering:
- Container discovery logic
- Metrics fetching from containers
- Metrics aggregation
- HTTP endpoint handlers
- Full update cycle

Test coverage is currently at 62.2%.

## Configuration
The application runs on port 8080 by default. It connects to Docker using environment variables (DOCKER_HOST, etc.).

### Environment Variables
- `PROMETHEUS_LABEL_FILTER` - Optional. Comma-separated list of label key-value pairs to filter containers. Only containers matching ALL specified labels will be discovered.
  
  Example: `PROMETHEUS_LABEL_FILTER="environment=production,service=api"`
  
  This would only discover containers that have both:
  - `environment=production` label
  - `service=api` label
  - `prometheus.auto.enable=true` label (always required)

## How It Works
1. Connects to Docker daemon
2. Lists all running containers every 30 seconds
3. Filters containers with `prometheus.auto.enable=true`
4. For each discovered container:
   - Gets container IP address and port
   - Creates HTTP SD target with `ip:port`
   - Extracts labels from `prometheus.auto.label.*` container labels
5. Exposes targets via `/sd` endpoint for Prometheus HTTP Service Discovery

## HTTP Service Discovery

The `/sd` endpoint provides Prometheus HTTP Service Discovery compatible JSON output. This allows Prometheus to dynamically discover containers managed by this collector.

### Prometheus Configuration Example

```yaml
scrape_configs:
  - job_name: 'docker-containers'
    http_sd_configs:
      - url: 'http://prometheus-docker-collector:8080/sd'
        refresh_interval: 30s
```

### SD Response Format

The endpoint returns a JSON array of targets with labels:

```json
[
  {
    "targets": ["192.168.1.100:80"],
    "labels": {
      "environment": "production",
      "service": "api",
      "version": "1.2.3"
    }
  }
]
```

Only labels defined with `prometheus.auto.label.<name>=<value>` are exposed. If a container has no such labels, the `labels` object will be empty.