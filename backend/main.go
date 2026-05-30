package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

var (
	anthropicKey  string
	mcpServerURL  string
	mcpClient     *mcpclient.Client
	mcpTools      []mcp.Tool
	claudeTools   []map[string]interface{}
	anthropicBase string
	httpClient    *http.Client
)

const (
	maxRequestBody = 64 * 1024 // 64 KB
	maxToolRounds  = 10        // prevent infinite tool_use loops
	sessionTTL     = 30 * time.Minute
	cleanupTick    = 5 * time.Minute
)

// ---------------------------------------------------------------------------
// Session store
// ---------------------------------------------------------------------------

type session struct {
	messages []claudeMessage
	lastUsed time.Time
	mu       sync.Mutex
}

var sessions sync.Map // map[string]*session

func getOrCreateSession(id string) *session {
	if s, ok := sessions.Load(id); ok {
		return s.(*session)
	}
	s := &session{lastUsed: time.Now()}
	actual, _ := sessions.LoadOrStore(id, s)
	return actual.(*session)
}

func startSessionCleaner() {
	go func() {
		for range time.Tick(cleanupTick) {
			now := time.Now()
			sessions.Range(func(key, value interface{}) bool {
				s := value.(*session)
				s.mu.Lock()
				idle := now.Sub(s.lastUsed) > sessionTTL
				s.mu.Unlock()
				if idle {
					sessions.Delete(key)
				}
				return true
			})
		}
	}()
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	anthropicKey = os.Getenv("ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is required")
	}

	mcpServerURL = os.Getenv("MCP_SERVER_URL")
	if mcpServerURL == "" {
		mcpServerURL = "http://localhost:3000"
	}

	anthropicBase = os.Getenv("ANTHROPIC_BASE_URL")
	if anthropicBase == "" {
		anthropicBase = "https://api.anthropic.com"
	}

	httpClient = &http.Client{Timeout: 5 * time.Minute}

	if err := initMCPClient(); err != nil {
		log.Fatalf("Failed to initialize MCP client: %v", err)
	}
	defer mcpClient.Close()

	startSessionCleaner()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", handleChat)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      withCORS(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 5 * time.Minute, // SSE streams can be long
		IdleTimeout:  60 * time.Second,
	}

	log.Println("Backend API listening on :8080")
	log.Fatal(srv.ListenAndServe())
}

func withCORS(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func initMCPClient() error {
	var err error
	mcpClient, err = mcpclient.NewStreamableHttpClient(mcpServerURL + "/mcp")
	if err != nil {
		return fmt.Errorf("create MCP client: %w", err)
	}

	ctx := context.Background()
	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ClientInfo: mcp.Implementation{
				Name:    "kubeassist-backend",
				Version: "0.1.0",
			},
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		},
	})
	if err != nil {
		return fmt.Errorf("MCP initialize: %w", err)
	}

	toolsResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("list MCP tools: %w", err)
	}

	mcpTools = toolsResult.Tools
	claudeTools = convertToolsForClaude(mcpTools)
	log.Printf("Loaded %d MCP tools: %v", len(mcpTools), toolNames(mcpTools))
	return nil
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func convertToolsForClaude(tools []mcp.Tool) []map[string]interface{} {
	result := make([]map[string]interface{}, len(tools))
	for i, t := range tools {
		inputSchema := map[string]interface{}{
			"type":       t.InputSchema.Type,
			"properties": t.InputSchema.Properties,
		}
		if len(t.InputSchema.Required) > 0 {
			inputSchema["required"] = t.InputSchema.Required
		}
		result[i] = map[string]interface{}{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": inputSchema,
		}
	}
	return result
}

// SSE helpers
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

func sendSSEJSON(w http.ResponseWriter, flusher http.Flusher, event string, v interface{}) {
	data, _ := json.Marshal(v)
	sendSSE(w, flusher, event, string(data))
}

// Claude API types
type claudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type      string      `json:"type"`
	Text      string      `json:"text,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   string      `json:"content,omitempty"`
}

type claudeRequest struct {
	Model     string                   `json:"model"`
	MaxTokens int                      `json:"max_tokens"`
	System    string                   `json:"system"`
	Messages  []claudeMessage          `json:"messages"`
	Tools     []map[string]interface{} `json:"tools,omitempty"`
	Stream    bool                     `json:"stream"`
}

const systemPrompt = `You are KubeAssist, an AI-powered Kubernetes operations assistant. You help users monitor and manage their Kubernetes clusters through natural language conversation.

When users ask about cluster status, pods, or resources, use the available tools to query the cluster and provide clear, concise answers.

Format your responses using Markdown for readability. Use tables for listing multiple resources. Highlight any issues (CrashLoopBackOff, Pending pods, high restart counts) prominently.

## Safety rules for destructive operations

The delete_pod tool is a DESTRUCTIVE operation. You MUST follow this protocol strictly:

1. When the user asks to delete a pod, DO NOT call delete_pod immediately.
2. Instead, respond with a clear confirmation prompt that includes the pod name and namespace, e.g.: "Are you sure you want to delete Pod X in namespace Y? Please confirm with 'yes' to proceed."
3. ONLY call the delete_pod tool AFTER the user explicitly confirms (e.g., replies "yes", "confirm", "do it").
4. If the user says "no", "cancel", or anything ambiguous, do NOT delete and acknowledge the cancellation.`

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message        string `json:"message"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Session management: load or create conversation
	var messages []claudeMessage
	var sess *session
	if req.ConversationID != "" {
		sess = getOrCreateSession(req.ConversationID)
		sess.mu.Lock()
		sess.lastUsed = time.Now()
		messages = append(messages, sess.messages...)
		sess.mu.Unlock()
	}
	messages = append(messages, claudeMessage{Role: "user", Content: req.Message})

	ctx := r.Context()

	// Tool-use loop with round limit
	for round := 0; round < maxToolRounds; round++ {
		stopReason, updatedMessages, err := streamClaudeResponse(ctx, w, flusher, messages)
		if err != nil {
			sendSSEJSON(w, flusher, "error", map[string]string{"error": err.Error()})
			break
		}
		messages = updatedMessages
		if stopReason != "tool_use" {
			break
		}
	}

	sendSSE(w, flusher, "done", "{}")

	// Persist conversation
	if sess != nil {
		sess.mu.Lock()
		sess.messages = messages
		sess.lastUsed = time.Now()
		sess.mu.Unlock()
	}
}

// streamClaudeResponse calls Claude streaming API, streams text to frontend,
// and handles tool_use blocks. Returns the stop_reason and updated messages slice.
func streamClaudeResponse(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, messages []claudeMessage) (string, []claudeMessage, error) {
	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 4096,
		System:    systemPrompt,
		Messages:  messages,
		Tools:     claudeTools,
		Stream:    true,
	}

	body, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", anthropicKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("Claude API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", nil, fmt.Errorf("Claude API error %d: %s", resp.StatusCode, string(errBody))
	}

	// Parse SSE stream from Claude
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var (
		stopReason    string
		textBlocks    []contentBlock
		toolUseBlocks []contentBlock
		currentBlock  contentBlock
		inputJSON     strings.Builder
	)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "content_block_start":
			cb, _ := event["content_block"].(map[string]interface{})
			blockType, _ := cb["type"].(string)
			currentBlock = contentBlock{Type: blockType}
			if blockType == "tool_use" {
				currentBlock.ID, _ = cb["id"].(string)
				currentBlock.Name, _ = cb["name"].(string)
				inputJSON.Reset()
			}

		case "content_block_delta":
			delta, _ := event["delta"].(map[string]interface{})
			deltaType, _ := delta["type"].(string)

			if deltaType == "text_delta" {
				text, _ := delta["text"].(string)
				currentBlock.Text += text
				sendSSEJSON(w, flusher, "message", map[string]string{"content": text})
			} else if deltaType == "input_json_delta" {
				partialJSON, _ := delta["partial_json"].(string)
				inputJSON.WriteString(partialJSON)
			}

		case "content_block_stop":
			if currentBlock.Type == "text" {
				textBlocks = append(textBlocks, currentBlock)
			} else if currentBlock.Type == "tool_use" {
				var input interface{}
				if inputJSON.Len() > 0 {
					json.Unmarshal([]byte(inputJSON.String()), &input)
				} else {
					input = map[string]interface{}{}
				}
				currentBlock.Input = input
				toolUseBlocks = append(toolUseBlocks, currentBlock)
			}

		case "message_delta":
			delta, _ := event["delta"].(map[string]interface{})
			if sr, ok := delta["stop_reason"].(string); ok {
				stopReason = sr
			}
		}
	}

	if stopReason == "tool_use" && len(toolUseBlocks) > 0 {
		// Build the assistant message with all content blocks
		var assistantContent []contentBlock
		assistantContent = append(assistantContent, textBlocks...)
		assistantContent = append(assistantContent, toolUseBlocks...)
		messages = append(messages, claudeMessage{
			Role:    "assistant",
			Content: toRawBlocks(assistantContent),
		})

		// Execute each tool call and build tool_result blocks
		var toolResults []contentBlock
		for _, tb := range toolUseBlocks {
			sendSSEJSON(w, flusher, "tool_call", map[string]interface{}{
				"tool":  tb.Name,
				"input": tb.Input,
			})

			result, err := callMCPTool(ctx, tb.Name, tb.Input)
			if err != nil {
				result = fmt.Sprintf("Error calling tool: %v", err)
			}

			sendSSEJSON(w, flusher, "tool_result", map[string]interface{}{
				"tool":   tb.Name,
				"result": json.RawMessage(result),
			})

			toolResults = append(toolResults, contentBlock{
				Type:      "tool_result",
				ToolUseID: tb.ID,
				Content:   result,
			})
		}

		messages = append(messages, claudeMessage{
			Role:    "user",
			Content: toRawBlocks(toolResults),
		})
	}

	return stopReason, messages, nil
}

func toRawBlocks(blocks []contentBlock) []map[string]interface{} {
	result := make([]map[string]interface{}, len(blocks))
	for i, b := range blocks {
		m := map[string]interface{}{"type": b.Type}
		switch b.Type {
		case "text":
			m["text"] = b.Text
		case "tool_use":
			m["id"] = b.ID
			m["name"] = b.Name
			m["input"] = b.Input
		case "tool_result":
			m["tool_use_id"] = b.ToolUseID
			m["content"] = b.Content
		}
		result[i] = m
	}
	return result
}

func callMCPTool(ctx context.Context, name string, input interface{}) (string, error) {
	args, ok := input.(map[string]interface{})
	if !ok {
		args = map[string]interface{}{}
	}

	result, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	})
	if err != nil {
		return "", err
	}

	if len(result.Content) > 0 {
		if textContent, ok := result.Content[0].(mcp.TextContent); ok {
			return textContent.Text, nil
		}
	}
	data, _ := json.Marshal(result.Content)
	return string(data), nil
}
