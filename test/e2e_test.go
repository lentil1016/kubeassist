package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// Mock Claude API — returns scripted SSE responses
// ---------------------------------------------------------------------------

func setupMockClaude(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Detect whether the request contains tool_result (i.e. 2nd round)
		hasToolResult := strings.Contains(string(body), `"tool_result"`)

		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)

		if !hasToolResult {
			// 1st call → return tool_use for list_pods
			sseWrite(w, f, `{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
			sseWrite(w, f, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_test1","name":"list_pods","input":{}}}`)
			sseWrite(w, f, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
			sseWrite(w, f, `{"type":"content_block_stop","index":0}`)
			sseWrite(w, f, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
			sseWrite(w, f, `{"type":"message_stop"}`)
		} else {
			// 2nd call → return summary text
			sseWrite(w, f, `{"type":"message_start","message":{"id":"msg_2","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"usage":{"input_tokens":100,"output_tokens":0}}}`)
			sseWrite(w, f, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			sseWrite(w, f, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Found 2 pods. "}}`)
			sseWrite(w, f, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"crash-pod is in CrashLoopBackOff."}}`)
			sseWrite(w, f, `{"type":"content_block_stop","index":0}`)
			sseWrite(w, f, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)
			sseWrite(w, f, `{"type":"message_stop"}`)
		}
	}))
}

func sseWrite(w http.ResponseWriter, f http.Flusher, data string) {
	fmt.Fprintf(w, "event: data\ndata: %s\n\n", data)
	f.Flush()
}

// ---------------------------------------------------------------------------
// Real MCP Server with fake K8s client
// ---------------------------------------------------------------------------

func setupMCPServer(t *testing.T) *httptest.Server {
	t.Helper()

	runningPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy-pod", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour))},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}}, NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 0, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
	crashPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "crash-pod", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute))},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox:latest"}}, NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: false, RestartCount: 5, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}},
	}

	k8s := fake.NewSimpleClientset(runningPod, crashPod)

	mcpSrv := server.NewMCPServer("test-mcp", "0.1.0")
	mcpSrv.AddTool(mcp.NewTool("list_pods",
		mcp.WithDescription("List pods"),
		mcp.WithString("namespace", mcp.Description("namespace")),
		mcp.WithString("status", mcp.Description("status filter")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := req.GetString("namespace", "")
		pods, err := k8s.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		type podInfo struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Status    string `json:"status"`
			Ready     string `json:"ready"`
			Restarts  int32  `json:"restarts"`
			Node      string `json:"node"`
		}
		var result []podInfo
		for i := range pods.Items {
			p := &pods.Items[i]
			status := string(p.Status.Phase)
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					status = cs.State.Waiting.Reason
				}
			}
			ready, total := 0, len(p.Spec.Containers)
			var restarts int32
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
				restarts += cs.RestartCount
			}
			result = append(result, podInfo{
				Name: p.Name, Namespace: p.Namespace, Status: status,
				Ready: fmt.Sprintf("%d/%d", ready, total), Restarts: restarts, Node: p.Spec.NodeName,
			})
		}
		data, _ := json.Marshal(map[string]interface{}{"pods": result, "total": len(result)})
		return mcp.NewToolResultText(string(data)), nil
	})

	httpSrv := server.NewStreamableHTTPServer(mcpSrv)
	return httptest.NewServer(httpSrv)
}

// ---------------------------------------------------------------------------
// Backend handler — re-creates the core orchestration logic
// ---------------------------------------------------------------------------

func setupBackend(t *testing.T, claudeURL, mcpURL string) *httptest.Server {
	t.Helper()

	ctx := context.Background()
	mcpCl, err := mcpclient.NewStreamableHttpClient(mcpURL + "/mcp")
	if err != nil {
		t.Fatalf("MCP client: %v", err)
	}
	_, err = mcpCl.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ClientInfo:      mcp.Implementation{Name: "test", Version: "0.1.0"},
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		},
	})
	if err != nil {
		t.Fatalf("MCP init: %v", err)
	}
	toolsResult, err := mcpCl.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	claudeTools := make([]map[string]interface{}, len(toolsResult.Tools))
	for i, tool := range toolsResult.Tools {
		schema := map[string]interface{}{"type": tool.InputSchema.Type, "properties": tool.InputSchema.Properties}
		if len(tool.InputSchema.Required) > 0 {
			schema["required"] = tool.InputSchema.Required
		}
		claudeTools[i] = map[string]interface{}{"name": tool.Name, "description": tool.Description, "input_schema": schema}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Message string `json:"message"` }
		json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		f := w.(http.Flusher)

		messages := []map[string]interface{}{{"role": "user", "content": req.Message}}

		for {
			stopReason, newMsgs := streamClaude(t, w, f, claudeURL, messages, claudeTools, mcpCl)
			messages = newMsgs
			if stopReason != "tool_use" {
				break
			}
		}
		sendSSE(w, f, "done", "{}")
	})

	return httptest.NewServer(mux)
}

// streamClaude calls the (mock) Claude API, forwards text and tool events.
func streamClaude(
	t *testing.T,
	w http.ResponseWriter, f http.Flusher,
	claudeURL string,
	messages []map[string]interface{},
	tools []map[string]interface{},
	mcpCl *mcpclient.Client,
) (string, []map[string]interface{}) {
	t.Helper()

	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-sonnet-4-20250514", "max_tokens": 4096, "stream": true,
		"system": "You are KubeAssist.", "messages": messages, "tools": tools,
	})
	resp, err := http.Post(claudeURL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("claude request: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	type contentBlock struct {
		Type  string      `json:"type"`
		ID    string      `json:"id,omitempty"`
		Name  string      `json:"name,omitempty"`
		Text  string      `json:"text,omitempty"`
		Input interface{} `json:"input,omitempty"`
	}

	var (
		stopReason    string
		textBlocks    []contentBlock
		toolUseBlocks []contentBlock
		cur           contentBlock
		inputJSON     strings.Builder
	)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}

		switch ev["type"] {
		case "content_block_start":
			cb, _ := ev["content_block"].(map[string]interface{})
			btype, _ := cb["type"].(string)
			cur = contentBlock{Type: btype}
			if btype == "tool_use" {
				cur.ID, _ = cb["id"].(string)
				cur.Name, _ = cb["name"].(string)
				inputJSON.Reset()
			}
		case "content_block_delta":
			delta, _ := ev["delta"].(map[string]interface{})
			dt, _ := delta["type"].(string)
			if dt == "text_delta" {
				txt, _ := delta["text"].(string)
				cur.Text += txt
				data, _ := json.Marshal(map[string]string{"content": txt})
				sendSSE(w, f, "message", string(data))
			} else if dt == "input_json_delta" {
				pj, _ := delta["partial_json"].(string)
				inputJSON.WriteString(pj)
			}
		case "content_block_stop":
			if cur.Type == "text" {
				textBlocks = append(textBlocks, cur)
			} else if cur.Type == "tool_use" {
				var inp interface{}
				if inputJSON.Len() > 0 {
					json.Unmarshal([]byte(inputJSON.String()), &inp)
				} else {
					inp = map[string]interface{}{}
				}
				cur.Input = inp
				toolUseBlocks = append(toolUseBlocks, cur)
			}
		case "message_delta":
			d, _ := ev["delta"].(map[string]interface{})
			if sr, ok := d["stop_reason"].(string); ok {
				stopReason = sr
			}
		}
	}

	if stopReason == "tool_use" && len(toolUseBlocks) > 0 {
		var asstContent []map[string]interface{}
		for _, b := range textBlocks {
			asstContent = append(asstContent, map[string]interface{}{"type": "text", "text": b.Text})
		}
		for _, b := range toolUseBlocks {
			asstContent = append(asstContent, map[string]interface{}{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Input})
		}
		messages = append(messages, map[string]interface{}{"role": "assistant", "content": asstContent})

		var toolResults []map[string]interface{}
		for _, tb := range toolUseBlocks {
			tcData, _ := json.Marshal(map[string]interface{}{"tool": tb.Name, "input": tb.Input})
			sendSSE(w, f, "tool_call", string(tcData))

			args, _ := tb.Input.(map[string]interface{})
			if args == nil {
				args = map[string]interface{}{}
			}
			res, callErr := mcpCl.CallTool(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: tb.Name, Arguments: args},
			})

			var resultStr string
			if callErr != nil {
				resultStr = fmt.Sprintf(`{"error":"%v"}`, callErr)
			} else if len(res.Content) > 0 {
				if tc, ok := res.Content[0].(mcp.TextContent); ok {
					resultStr = tc.Text
				}
			}

			trData, _ := json.Marshal(map[string]interface{}{"tool": tb.Name, "result": json.RawMessage(resultStr)})
			sendSSE(w, f, "tool_result", string(trData))

			toolResults = append(toolResults, map[string]interface{}{
				"type": "tool_result", "tool_use_id": tb.ID, "content": resultStr,
			})
		}
		messages = append(messages, map[string]interface{}{"role": "user", "content": toolResults})
	}

	return stopReason, messages
}

func sendSSE(w http.ResponseWriter, f http.Flusher, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	f.Flush()
}

// ---------------------------------------------------------------------------
// SSE parser
// ---------------------------------------------------------------------------

type sseEvent struct {
	Type string
	Data json.RawMessage
}

func parseSSEStream(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var events []sseEvent
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") && eventType != "" {
			events = append(events, sseEvent{
				Type: eventType,
				Data: json.RawMessage(strings.TrimPrefix(line, "data: ")),
			})
			eventType = ""
		}
	}
	return events
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

func TestE2EChatPipeline(t *testing.T) {
	// 1. Start mock Claude
	claude := setupMockClaude(t)
	defer claude.Close()

	// 2. Start real MCP Server (fake K8s)
	mcpSrv := setupMCPServer(t)
	defer mcpSrv.Close()

	// 3. Start Backend
	backend := setupBackend(t, claude.URL, mcpSrv.URL)
	defer backend.Close()

	// 4. Send chat request
	resp, err := http.Post(
		backend.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"message":"有没有异常的 pod"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/chat failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// 5. Parse SSE stream
	events := parseSSEStream(t, resp.Body)

	// 6. Verify event types in order
	got := make([]string, len(events))
	for i, e := range events {
		got[i] = e.Type
	}
	t.Logf("SSE events: %v", got)

	required := []string{"tool_call", "tool_result", "message", "done"}
	idx := 0
	for _, e := range got {
		if idx < len(required) && e == required[idx] {
			idx++
		}
	}
	if idx != len(required) {
		t.Fatalf("expected event sequence %v within %v (matched %d/%d)", required, got, idx, len(required))
	}

	// 7. Verify tool_call is for list_pods
	for _, e := range events {
		if e.Type == "tool_call" {
			var tc struct{ Tool string `json:"tool"` }
			json.Unmarshal(e.Data, &tc)
			if tc.Tool != "list_pods" {
				t.Errorf("tool_call tool = %s, want list_pods", tc.Tool)
			}
		}
	}

	// 8. Verify tool_result contains both pods
	for _, e := range events {
		if e.Type == "tool_result" {
			var tr struct {
				Tool   string `json:"tool"`
				Result struct {
					Pods []struct {
						Name   string `json:"name"`
						Status string `json:"status"`
					} `json:"pods"`
					Total int `json:"total"`
				} `json:"result"`
			}
			json.Unmarshal(e.Data, &tr)
			if tr.Result.Total != 2 {
				t.Errorf("total = %d, want 2", tr.Result.Total)
			}
			podMap := map[string]string{}
			for _, p := range tr.Result.Pods {
				podMap[p.Name] = p.Status
			}
			if podMap["healthy-pod"] != "Running" {
				t.Errorf("healthy-pod status = %s, want Running", podMap["healthy-pod"])
			}
			if podMap["crash-pod"] != "CrashLoopBackOff" {
				t.Errorf("crash-pod status = %s, want CrashLoopBackOff", podMap["crash-pod"])
			}
		}
	}

	// 9. Verify message text mentions crash-pod
	var fullText strings.Builder
	for _, e := range events {
		if e.Type == "message" {
			var m struct{ Content string `json:"content"` }
			json.Unmarshal(e.Data, &m)
			fullText.WriteString(m.Content)
		}
	}
	if !strings.Contains(fullText.String(), "crash-pod") {
		t.Errorf("response text does not mention crash-pod: %s", fullText.String())
	}
}
