# Google Automation — Docker Deployment

## Quick Start (Docker Compose)

```bash
# Build + start both containers
docker compose up -d --build

# View logs
docker compose logs -f

# Stop
docker compose down

# Stop + remove data
docker compose down -v
```

## What Runs

```
Container: ga-worker (Python)       Container: ga-orchestrator (Go)
├─ Playwright + stealth browser      ├─ Proxy manager (scrape + health check)
├─ gRPC server on :50051             ├─ Article collector (sitemap.xml)
├─ Humanized search execution        ├─ Scheduler (dynamic throttle)
└─ Reading simulation                ├─ Analytics + SERP tracking
    │                                └─ gRPC client → ga-worker:50051
    └──────────────────────────────────┘
```

## Volumes

- `db-data` — SQLite database (persistent across restarts)
- `screenshots` — Error screenshots from worker
- `results` — JSON result files

## Config

Edit `config/config.yaml` before `docker compose up`. The file is mounted read-only into the orchestrator container.

## GitHub Container Registry (Auto-deploy)

Every push to `main` triggers GitHub Actions CI:

```
push to main
  ├── go-check job: go build + go vet
  ├── python-check job: install deps + import check
  ├── build-orchestrator job: build Docker image → push to ghcr.io
  └── build-worker job: build Docker image → push to ghcr.io
```

Images published to:
- `ghcr.io/bagasunix/google-automation/orchestrator:latest`
- `ghcr.io/bagasunix/google-automation/worker:latest`

### Pull & Run from GHCR

```bash
# Pull latest images
docker pull ghcr.io/bagasunix/google-automation/orchestrator:latest
docker pull ghcr.io/bagasunix/google-automation/worker:latest

# Run with docker-compose (uses pre-built images)
# Create a docker-compose.override.yml:
cat > docker-compose.override.yml << 'EOF'
services:
  worker:
    image: ghcr.io/bagasunix/google-automation/worker:latest
    build: null
  orchestrator:
    image: ghcr.io/bagasunix/google-automation/orchestrator:latest
    build: null
EOF

docker compose up -d
```

## Manual Docker Build (no compose)

```bash
# Build worker
docker build -f Dockerfile.worker -t ga-worker .

# Build orchestrator
docker build -f Dockerfile -t ga-orchestrator .

# Create network
docker network create ga-net

# Run worker
docker run -d --name ga-worker --network ga-net -p 50051:50051 ga-worker

# Run orchestrator
docker run -d --name ga-orchestrator --network ga-net \
  -v $(pwd)/config/config.yaml:/app/config/config.yaml:ro \
  -v ga-db:/app/data \
  ga-orchestrator --config config/config.yaml --db /app/data/search_automation.db --worker-host worker
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `DB_PATH` | `/app/data/search_automation.db` | SQLite database path |

## Health Check

Worker container has a healthcheck that verifies the gRPC port is listening:
```yaml
healthcheck:
  test: ["CMD", "python", "-c", "import grpc; grpc.insecure_channel('localhost:50051')"]
```

Orchestrator `depends_on: worker: condition: service_healthy` — it waits for worker to be ready before starting.
