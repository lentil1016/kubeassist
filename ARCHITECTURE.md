# KubeAssist Architecture

## System Topology

```
                         ┌──────────────────────────────────────────────────────────┐
                         │                    Kubernetes Cluster                     │
                         │                                                          │
┌─────────┐   POST       │  ┌───────────┐       SSE       ┌──────────────┐          │
│ Browser │──/api/chat──►│  │ Frontend  │───────────────►  │  Backend API │          │
│  (User) │◄─────────────│  │ (React)   │                  │  (Go :8080)  │          │
└─────────┘   SSE stream │  │  :80      │                  └──────┬───────┘          │
                         │  └───────────┘                         │                  │
                         │     nginx proxies /api/                │                  │
                         │     to backend:8080                    │ Claude Messages  │
                         │                                        │ API (streaming)  │
                         │                                        ▼                  │
                         │                                 ┌─────────────┐           │
                         │                                 │ Anthropic   │           │
                         │                                 │ Claude API  │           │
                         │                                 └──────┬──────┘           │
                         │                                        │                  │
                         │                          tool_use ─────┘                  │
                         │                          │                                │
                         │                          ▼                                │
                         │  ┌──────────────┐   MCP Streamable HTTP   ┌───────────┐   │
                         │  │  MCP Server  │◄───────────────────────►│ K8s API   │   │
                         │  │  (Go :3000)  │   tool call / result    │ (in-cluster)│  │
                         │  └──────────────┘                        └───────────┘   │
                         │    ServiceAccount:                                        │
                         │    kubeassist-mcp                                         │
                         └──────────────────────────────────────────────────────────┘
```

## Component Responsibilities

| Component | Language | Role |
|-----------|----------|------|
| **Frontend** | React + TypeScript | Chat UI. Sends user messages via `POST /api/chat`, consumes SSE stream, renders Markdown responses with tool call visualization. |
| **Backend API** | Go | Orchestration layer. Receives user messages, calls Claude Messages API with MCP tool definitions, forwards tool_use to MCP Server, feeds tool results back to Claude, streams final response to frontend via SSE. |
| **MCP Server** | Go | Kubernetes operations executor. Exposes 5 MCP tools (`list_pods`, `get_pod_detail`, `get_pod_logs`, `get_events`, `delete_pod`) via Streamable HTTP transport. Accesses K8s API using in-cluster ServiceAccount. |

## Request Processing Flow

A single user message goes through these steps:

```
1. User types "Are there any unhealthy pods?" in the browser

2. Frontend sends POST /api/chat { "message": "..." }

3. Backend opens SSE response stream to Frontend

4. Backend calls Claude Messages API (streaming) with:
   - System prompt (includes delete_pod safety rules)
   - User message
   - MCP tool definitions (discovered from MCP Server at startup)

5. Claude decides to call list_pods tool
   → Backend receives tool_use content block via Claude SSE
   → Backend sends "tool_call" SSE event to Frontend (UI shows spinner)
   → Backend forwards tool call to MCP Server via MCP Streamable HTTP

6. MCP Server executes list_pods:
   → Calls K8s API: client.CoreV1().Pods("").List(...)
   → Filters by namespace/status
   → Returns structured JSON result

7. Backend receives tool result from MCP Server
   → Sends "tool_result" SSE event to Frontend
   → Sends tool_result back to Claude as a follow-up message

8. Claude generates a text response analyzing the pod data
   → Backend streams text deltas as "message" SSE events to Frontend

9. Claude returns stop_reason=end_turn
   → Backend sends "done" SSE event
   → Frontend renders the complete Markdown response

Steps 5-8 may repeat if Claude needs to call multiple tools.
```

## Security Design

### RBAC — Minimum Privilege

The MCP Server runs with a dedicated ServiceAccount (`kubeassist-mcp`) whose ClusterRole grants only the permissions required by the 5 tools:

```yaml
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

No wildcard permissions. No access to secrets, configmaps, or other sensitive resources.

### Credential Isolation

| Component | K8s Token | Anthropic API Key |
|-----------|-----------|-------------------|
| Frontend | `automountServiceAccountToken: false` | None |
| Backend | `automountServiceAccountToken: false` | Injected via Secret env var |
| MCP Server | Auto-mounted (in-cluster SA) | None |

The Backend cannot access the K8s API — it has no ServiceAccount token. All cluster operations are proxied through the MCP Server. The Anthropic API key is only accessible to the Backend.

### Write Operation Confirmation

Destructive operations (`delete_pod`) are guarded at the LLM layer via the system prompt:

1. User requests a deletion → Claude responds with a confirmation prompt (does NOT call the tool)
2. User confirms ("yes") → Claude calls `delete_pod`
3. User declines → Claude acknowledges cancellation

The MCP Server itself has no confirmation logic — the safety boundary is enforced by Claude's system prompt instructions.

## Technology Choices

### Why MCP Streamable HTTP Transport

MCP (Model Context Protocol) provides a standardized interface between AI models and tool providers. Streamable HTTP was chosen over stdio transport because:

- The MCP Server runs as a **separate Deployment** from the Backend — they communicate over the network, not via process pipes.
- HTTP transport is stateless and horizontally scalable — each tool call is an independent request.
- Streamable HTTP supports both request-response and streaming patterns within the MCP specification.

### Why Three Independent Deployments

Each component has different scaling, security, and lifecycle characteristics:

- **Frontend**: Static assets served by nginx. Can be scaled independently, cached by CDN, or replaced without backend changes.
- **Backend**: CPU-bound during Claude API streaming. Scaling depends on concurrent user sessions. Holds API keys but no K8s credentials.
- **MCP Server**: Needs K8s API access (ServiceAccount). Scaling depends on tool call volume. Isolated with minimum-privilege RBAC.

Separating them also enables independent image builds, rolling updates, and failure isolation.
