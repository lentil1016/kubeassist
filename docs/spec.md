# KubeAssist Technical Specification

## System Overview

KubeAssist is an AI-powered Kubernetes operations assistant that enables users to query and manage cluster resources through natural language conversations. Users interact via a browser-based chat interface; the backend orchestrates requests through the Claude API with tool calling, delegating actual Kubernetes operations to a dedicated MCP (Model Context Protocol) Server. The system is designed to run entirely within a Kubernetes cluster, with all three components deployed as independent workloads.

## Architecture

### Components

```
┌────────────┐       SSE        ┌──────────────┐   Streamable HTTP   ┌────────────┐
│  Frontend   │ ───────────────► │  Backend API  │ ──────────────────► │ MCP Server │
│  (React)    │ POST /api/chat   │  (Go)         │   MCP Protocol     │ (Go)       │
└────────────┘                   └──────┬───────┘                     └─────┬──────┘
                                        │                                   │
                                        │ Claude API                        │ K8s API
                                        ▼                                   ▼
                                  ┌───────────┐                     ┌──────────────┐
                                  │ Anthropic  │                     │  K8s Cluster │
                                  │ API        │                     │  (in-cluster)│
                                  └───────────┘                     └──────────────┘
```

### Frontend (React)

- Single-page chat interface for conversational interaction with the cluster.
- Sends user messages to `POST /api/chat`.
- Receives responses as an SSE (Server-Sent Events) stream and renders them incrementally.
- Displays Markdown-formatted responses including code blocks, tables, and lists.
- For destructive operations (e.g. `delete_pod`), renders a confirmation prompt to the user and sends the confirmation back as a follow-up message.

### Backend API (Go)

The orchestration layer responsible for bridging the user, Claude, and the MCP Server.

Request flow:

1. Receive user message from Frontend via `POST /api/chat`.
2. Send the message to Claude API with the MCP tool definitions registered.
3. If Claude responds with `tool_use`, forward the tool call to the MCP Server via Streamable HTTP transport.
4. Return the tool result to Claude as a `tool_result` message.
5. Repeat steps 3-4 if Claude issues additional tool calls.
6. Stream Claude's final text response back to the Frontend via SSE.

The Backend does not hold any Kubernetes credentials. All cluster operations are proxied through the MCP Server.

### MCP Server (Go)

A standalone MCP-compliant server that exposes Kubernetes operations as MCP tools.

- Transport: Streamable HTTP (HTTP-based MCP transport with streaming support).
- Authenticates to the Kubernetes API using the in-cluster ServiceAccount token.
- Implements the MCP tool protocol: receives tool call requests, executes the corresponding K8s API call, and returns structured results.
- Stateless — each tool call is an independent operation.

### Deployment Topology

All three components are deployed as independent Kubernetes Deployments within the same cluster:

| Component    | Deployment        | Service              | Port |
|-------------|-------------------|----------------------|------|
| Frontend    | `kubeassist-frontend` | `kubeassist-frontend` (ClusterIP) | 80   |
| Backend API | `kubeassist-backend`  | `kubeassist-backend` (ClusterIP)  | 8080 |
| MCP Server  | `kubeassist-mcp`      | `kubeassist-mcp` (ClusterIP)      | 3000 |

## MCP Tools Definition

### list_pods

List Pods in the cluster with optional filtering by namespace and status phase.

**Inputs:**

| Field      | Type   | Required | Description                                      |
|-----------|--------|----------|--------------------------------------------------|
| namespace | string | No       | Kubernetes namespace. Defaults to all namespaces. |
| status    | string | No       | Filter by Pod phase: `Running`, `Pending`, `Failed`, `Succeeded`, `Unknown`. |

**Output:**

```json
{
  "pods": [
    {
      "name": "nginx-7d4f8b7d4-abc12",
      "namespace": "default",
      "status": "Running",
      "ready": "1/1",
      "restarts": 0,
      "age": "2d",
      "node": "worker-01"
    }
  ],
  "total": 1
}
```

### get_pod_detail

Get detailed information about a specific Pod, including conditions, container statuses, and related Events.

**Inputs:**

| Field      | Type   | Required | Description          |
|-----------|--------|----------|----------------------|
| namespace | string | Yes      | Pod namespace.       |
| name      | string | Yes      | Pod name.            |

**Output:**

```json
{
  "name": "nginx-7d4f8b7d4-abc12",
  "namespace": "default",
  "status": "Running",
  "node": "worker-01",
  "ip": "10.244.1.5",
  "created_at": "2026-05-28T10:00:00Z",
  "labels": { "app": "nginx" },
  "conditions": [
    { "type": "Ready", "status": "True", "reason": "", "message": "" }
  ],
  "containers": [
    {
      "name": "nginx",
      "image": "nginx:1.25",
      "state": "running",
      "ready": true,
      "restart_count": 0,
      "started_at": "2026-05-28T10:00:05Z"
    }
  ],
  "events": [
    {
      "type": "Normal",
      "reason": "Pulled",
      "message": "Successfully pulled image \"nginx:1.25\"",
      "count": 1,
      "first_seen": "2026-05-28T10:00:02Z",
      "last_seen": "2026-05-28T10:00:02Z"
    }
  ]
}
```

### get_pod_logs

Retrieve logs from a specific container in a Pod.

**Inputs:**

| Field      | Type    | Required | Description                                           |
|-----------|---------|----------|-------------------------------------------------------|
| namespace | string  | Yes      | Pod namespace.                                        |
| name      | string  | Yes      | Pod name.                                             |
| container | string  | No       | Container name. Required if the Pod has multiple containers. |
| tail      | integer | No       | Number of lines from the end of the log. Defaults to 100. |
| previous  | boolean | No       | Return logs from the previous terminated container instance. Defaults to false. |

**Output:**

```json
{
  "logs": "2026-05-28T10:00:05Z INFO  Starting nginx...\n2026-05-28T10:00:06Z INFO  Listening on :80\n",
  "container": "nginx",
  "truncated": false
}
```

### get_events

List cluster Events with optional filtering by namespace and event type.

**Inputs:**

| Field      | Type   | Required | Description                                      |
|-----------|--------|----------|--------------------------------------------------|
| namespace | string | No       | Kubernetes namespace. Defaults to all namespaces. |
| type      | string | No       | Event type filter: `Warning` or `Normal`.        |

**Output:**

```json
{
  "events": [
    {
      "type": "Warning",
      "reason": "BackOff",
      "object": "Pod/crash-loop-abc12",
      "namespace": "default",
      "message": "Back-off restarting failed container",
      "count": 5,
      "first_seen": "2026-05-28T09:50:00Z",
      "last_seen": "2026-05-28T10:00:00Z"
    }
  ],
  "total": 1
}
```

### delete_pod

Delete a specific Pod. This is a destructive operation — the LLM must ask the user for explicit confirmation before executing.

**Inputs:**

| Field      | Type   | Required | Description    |
|-----------|--------|----------|----------------|
| namespace | string | Yes      | Pod namespace. |
| name      | string | Yes      | Pod name.      |

**Output:**

```json
{
  "deleted": true,
  "name": "nginx-7d4f8b7d4-abc12",
  "namespace": "default",
  "message": "Pod deleted successfully"
}
```

## API Definition

### POST /api/chat

The single API endpoint exposed by the Backend to the Frontend.

**Request:**

```
POST /api/chat
Content-Type: application/json

{
  "message": "Show me all pods in the kube-system namespace that are not running",
  "conversation_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

| Field             | Type   | Required | Description                                                        |
|-------------------|--------|----------|--------------------------------------------------------------------|
| `message`         | string | Yes      | User's natural language message.                                   |
| `conversation_id` | string | No       | UUID identifying the conversation. Enables multi-turn context. If omitted, the request is stateless. |

**Response:**

The response is an SSE stream (`Content-Type: text/event-stream`). Each event follows the format:

```
event: <event_type>
data: <json_payload>
```

Event types:

| Event        | Payload                                      | Description                                    |
|-------------|----------------------------------------------|------------------------------------------------|
| `message`   | `{ "content": "..." }`                       | Incremental text chunk from Claude's response. |
| `tool_call` | `{ "tool": "list_pods", "input": {...} }`    | Indicates a tool is being invoked (for UI feedback). |
| `tool_result` | `{ "tool": "list_pods", "result": {...} }` | Tool execution result (for UI feedback).       |
| `error`     | `{ "error": "..." }`                         | Error message.                                 |
| `done`      | `{}`                                         | Stream is complete.                            |

## Security Design

### MCP Server ServiceAccount

The MCP Server runs with a dedicated ServiceAccount (`kubeassist-mcp`) that has the minimum required permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubeassist-mcp
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["get", "list"]
```

### Write Operation Confirmation

Destructive operations (`delete_pod`) are guarded at the LLM layer:

1. When the user's intent implies a destructive action, Claude includes a confirmation prompt in its response (e.g., "Are you sure you want to delete Pod X in namespace Y?").
2. The user must send an explicit confirmation message (e.g., "yes", "confirm").
3. Only upon receiving confirmation does Claude issue the `delete_pod` tool call.

This is enforced via the Claude system prompt, not at the MCP Server level. The MCP Server itself will execute any valid tool call it receives.

### Credential Isolation

- The Backend does not mount any Kubernetes ServiceAccount token. Its Deployment explicitly sets `automountServiceAccountToken: false`. It cannot access the K8s API directly.
- The Anthropic API key is provided to the Backend as a Kubernetes Secret, mounted as an environment variable.
- The MCP Server uses its in-cluster ServiceAccount token (auto-mounted by Kubernetes) for K8s API access.

## Deployment

### Directory Structure

```
deploy/
├── kustomization.yaml
├── base/
│   ├── kustomization.yaml
│   ├── namespace.yaml
│   ├── frontend/
│   │   ├── deployment.yaml
│   │   └── service.yaml
│   ├── backend/
│   │   ├── deployment.yaml
│   │   ├── service.yaml
│   │   └── secret.yaml        # Anthropic API key (placeholder)
│   └── mcp/
│       ├── deployment.yaml
│       ├── service.yaml
│       ├── serviceaccount.yaml
│       ├── clusterrole.yaml
│       └── clusterrolebinding.yaml
```

All manifests are available in two formats: plain Kubernetes YAML organized with Kustomize, and a Helm chart.

### Container Images

Images are built via GitHub Actions for `linux/amd64`:

| Component   | Image                              |
|------------|------------------------------------|
| Frontend   | `docker.io/lentil1016/kubeassist-frontend:latest` |
| Backend    | `docker.io/lentil1016/kubeassist-backend:latest`  |
| MCP Server | `docker.io/lentil1016/kubeassist-mcp:latest`      |

For air-gapped environments, the CI pipeline produces a `docker save` tarball as a release artifact.

### Helm Chart

A Helm chart is provided at `deploy/helm/kubeassist/` with the following configurable values:

| Value | Required | Default | Description |
|-------|----------|---------|-------------|
| `anthropicApiKey` | Yes | — | Claude API key |
| `anthropicBaseUrl` | No | `""` (official API) | Custom Claude API base URL |
| `image.registry` | No | `docker.io/lentil1016` | Image registry prefix (override for air-gap / mirror) |
| `image.tag` | No | `latest` | Image tag for all three components |
| `frontend.service.type` | No | `ClusterIP` | Frontend Service type |

All other configuration (image names, ports, RBAC rules, resource limits) is hardcoded in the templates.

### Deployment Commands

```bash
# Option A: Kustomize
kubectl apply -k deploy/base/
kubectl -n kubeassist create secret generic kubeassist-api-key \
  --from-literal=ANTHROPIC_API_KEY=<your-key>

# Option B: Helm
helm install kubeassist deploy/helm/kubeassist/ \
  --namespace kubeassist --create-namespace \
  --set anthropicApiKey=<your-key>
```
