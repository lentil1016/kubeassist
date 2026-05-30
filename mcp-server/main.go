package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func initK8sClient() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build k8s config: %w", err)
		}
	}
	return kubernetes.NewForConfig(config)
}

func registerTools(s *server.MCPServer, client kubernetes.Interface) {
	s.AddTool(mcp.NewTool("list_pods",
		mcp.WithDescription("List Pods in the cluster with optional filtering by namespace and status phase."),
		mcp.WithString("namespace", mcp.Description("Kubernetes namespace. Defaults to all namespaces if not specified.")),
		mcp.WithString("status", mcp.Description("Filter by Pod phase: Running, Pending, Failed, Succeeded, Unknown.")),
	), listPodsHandler(client))

	s.AddTool(mcp.NewTool("get_pod_detail",
		mcp.WithDescription("Get detailed information about a specific Pod, including conditions, container statuses, and related Events."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Pod name.")),
	), getPodDetailHandler(client))

	s.AddTool(mcp.NewTool("get_pod_logs",
		mcp.WithDescription("Retrieve logs from a specific container in a Pod."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Pod name.")),
		mcp.WithString("container", mcp.Description("Container name. Required if the Pod has multiple containers.")),
		mcp.WithNumber("tail", mcp.Description("Number of lines from the end of the log. Defaults to 100.")),
		mcp.WithBoolean("previous", mcp.Description("Return logs from the previous terminated container instance. Defaults to false.")),
	), getPodLogsHandler(client))

	s.AddTool(mcp.NewTool("get_events",
		mcp.WithDescription("List cluster Events with optional filtering by namespace and event type."),
		mcp.WithString("namespace", mcp.Description("Kubernetes namespace. Defaults to all namespaces.")),
		mcp.WithString("type", mcp.Description("Event type filter: Warning or Normal.")),
	), getEventsHandler(client))

	s.AddTool(mcp.NewTool("delete_pod",
		mcp.WithDescription("Delete a specific Pod. This is a DESTRUCTIVE operation."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Pod name.")),
	), deletePodHandler(client))
}

func main() {
	client, err := initK8sClient()
	if err != nil {
		log.Fatalf("Failed to initialize k8s client: %v", err)
	}
	log.Println("K8s client initialized")

	s := server.NewMCPServer("kubeassist-mcp", "0.1.0")
	registerTools(s, client)

	// Health endpoint on a separate port for K8s probes
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		log.Fatal(http.ListenAndServe(":3001", mux))
	}()

	httpServer := server.NewStreamableHTTPServer(s)
	log.Println("MCP Server listening on :3000")
	if err := httpServer.Start(":3000"); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
