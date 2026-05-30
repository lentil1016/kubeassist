# KubeAssist

AI-powered Kubernetes operations assistant. Users interact through a browser-based chat interface to query and manage cluster resources using natural language. The backend orchestrates requests through the Claude API with tool calling, delegating actual Kubernetes operations to a dedicated MCP Server.

## Architecture

```
Browser ──► Frontend (React) ──► Backend (Go) ──► Claude API
                                      │
                                      ▼
                                 MCP Server (Go) ──► K8s API
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full topology, request flow, and security design.

## Quick Start — Local Development

Prerequisites: Go 1.25+, Node.js 22+, a working `kubectl` cluster, and a Claude API key.

```bash
# Terminal 1 — MCP Server (connects to your local kubeconfig cluster)
cd mcp-server && go run .

# Terminal 2 — Backend
cd backend && ANTHROPIC_API_KEY=sk-ant-... go run .

# Terminal 3 — Frontend (Vite dev server with /api proxy to localhost:8080)
cd frontend && npm install && npm run dev
```

Open http://localhost:5173 and try asking: "Are there any unhealthy pods?"

If your Claude API uses a custom base URL:

```bash
ANTHROPIC_API_KEY=sk-... ANTHROPIC_BASE_URL=https://your-proxy.example.com go run .
```

## Deploy to Kubernetes

### Option A: Kustomize

```bash
# 1. Deploy all resources
kubectl apply -k deploy/base/

# 2. Set the API key
kubectl -n kubeassist create secret generic kubeassist-api-key \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...
```

### Option B: Helm

```bash
helm install kubeassist deploy/helm/kubeassist/ \
  --namespace kubeassist --create-namespace \
  --set anthropicApiKey=sk-ant-...
```

Optional values:

| Value | Default | Description |
|-------|---------|-------------|
| `anthropicApiKey` | (required) | Claude API key |
| `anthropicBaseUrl` | `""` (official API) | Custom Claude API base URL |
| `image.registry` | `docker.io/lentil1016` | Image registry prefix (override for air-gap / mirror) |
| `image.tag` | `latest` | Image tag for all three components |
| `frontend.service.type` | `ClusterIP` | Frontend Service type (`NodePort` / `LoadBalancer` for external access) |

### Air-gap / Offline Deployment

**Step 1 — Download the image bundle from CI**

On a machine with internet access, download the artifact from the latest successful GitHub Actions run:

```bash
# Find the latest successful run
gh run list --repo lentil1016/kubeassist --status success --limit 1

# Download the artifact (replace RUN_ID with the actual run ID)
gh run download RUN_ID --repo lentil1016/kubeassist --name 'kubeassist-images-*'
```

This produces a `kubeassist-images.tar.gz` file containing all three images.

**Step 2 — Transfer and load images**

Copy the file to the air-gapped environment, then load:

```bash
gunzip kubeassist-images.tar.gz
docker load -i kubeassist-images.tar

# For containerd (e.g. K3s, RKE2):
# ctr -n k8s.io images import kubeassist-images.tar
```

After loading, the following images are available locally (tag matches the git commit SHA from CI):

```
docker.io/lentil1016/kubeassist-frontend:<sha>
docker.io/lentil1016/kubeassist-backend:<sha>
docker.io/lentil1016/kubeassist-mcp:<sha>
```

**Step 3 — Push to internal registry** (if cluster nodes don't share the local Docker daemon)

```bash
INTERNAL=your-registry.example.com/kubeassist
SHA=<sha>   # the git short SHA from the artifact filename

for comp in frontend backend mcp; do
  docker tag docker.io/lentil1016/kubeassist-${comp}:${SHA} ${INTERNAL}/kubeassist-${comp}:${SHA}
  docker push ${INTERNAL}/kubeassist-${comp}:${SHA}
done
```

**Step 4 — Deploy with Helm**

```bash
helm install kubeassist deploy/helm/kubeassist/ \
  --namespace kubeassist --create-namespace \
  --set anthropicApiKey=sk-ant-... \
  --set image.registry=your-registry.example.com/kubeassist \
  --set image.tag=<sha>
```

**Step 5 — Verify**

```bash
kubectl -n kubeassist get pods
# All three pods should be Running

kubectl -n kubeassist port-forward svc/kubeassist-frontend 8888:80
# Open http://localhost:8888 and send a message
```

## Development

### Run Tests

```bash
# MCP Server unit tests (22 tests, uses fake K8s client)
cd mcp-server && go test -v ./...

# Backend unit tests
cd backend && go test -v ./...

# E2E integration test (mock Claude API, no external deps)
cd test && go test -v ./...
```

### Build Docker Images

```bash
make docker-build                    # Build all three images
make docker-save                     # Save to kubeassist-images.tar
```

### Project Structure

```
kubeassist/
├── frontend/          React + TypeScript chat UI
├── backend/           Go orchestration layer (Claude API + MCP client)
├── mcp-server/        Go MCP Server (K8s tools)
├── test/              E2E integration tests
├── deploy/
│   ├── base/          Kustomize manifests
│   └── helm/          Helm chart
├── docs/spec.md       Technical specification
├── ARCHITECTURE.md    Architecture documentation
└── AI_PROMPTS.md      AI collaboration report
```
