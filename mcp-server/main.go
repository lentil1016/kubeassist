package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var k8sClient *kubernetes.Clientset

func initK8sClient() error {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("failed to build k8s config: %w", err)
		}
	}
	k8sClient, err = kubernetes.NewForConfig(config)
	return err
}

type podInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Ready     string `json:"ready"`
	Restarts  int32  `json:"restarts"`
	Age       string `json:"age"`
	Node      string `json:"node"`
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func podStatus(pod *corev1.Pod) string {
	// Show more useful status for non-running pods
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	return string(pod.Status.Phase)
}

func listPodsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace := request.GetString("namespace", "")
	statusFilter := request.GetString("status", "")

	pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	var result []podInfo
	for i := range pods.Items {
		pod := &pods.Items[i]
		phase := string(pod.Status.Phase)
		if statusFilter != "" && phase != statusFilter {
			continue
		}

		ready := 0
		total := len(pod.Spec.Containers)
		var restarts int32
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}

		result = append(result, podInfo{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Status:    podStatus(pod),
			Ready:     fmt.Sprintf("%d/%d", ready, total),
			Restarts:  restarts,
			Age:       formatAge(time.Since(pod.CreationTimestamp.Time)),
			Node:      pod.Spec.NodeName,
		})
	}

	output := map[string]interface{}{
		"pods":  result,
		"total": len(result),
	}
	data, _ := json.Marshal(output)
	return mcp.NewToolResultText(string(data)), nil
}

func main() {
	if err := initK8sClient(); err != nil {
		log.Fatalf("Failed to initialize k8s client: %v", err)
	}
	log.Println("K8s client initialized")

	s := server.NewMCPServer("kubeassist-mcp", "0.1.0")

	tool := mcp.NewTool("list_pods",
		mcp.WithDescription("List Pods in the cluster with optional filtering by namespace and status phase."),
		mcp.WithString("namespace", mcp.Description("Kubernetes namespace. Defaults to all namespaces if not specified.")),
		mcp.WithString("status", mcp.Description("Filter by Pod phase: Running, Pending, Failed, Succeeded, Unknown.")),
	)
	s.AddTool(tool, listPodsHandler)

	httpServer := server.NewStreamableHTTPServer(s)
	log.Println("MCP Server listening on :3000")
	if err := httpServer.Start(":3000"); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
