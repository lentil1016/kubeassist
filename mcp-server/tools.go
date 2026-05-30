package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// --- list_pods ---

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

func podReadyRestarts(pod *corev1.Pod) (string, int32) {
	ready := 0
	total := len(pod.Spec.Containers)
	var restarts int32
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}
	return fmt.Sprintf("%d/%d", ready, total), restarts
}

func listPodsHandler(client kubernetes.Interface) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := request.GetString("namespace", "")
		statusFilter := request.GetString("status", "")

		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
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
			readyStr, restarts := podReadyRestarts(pod)
			result = append(result, podInfo{
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Status:    podStatus(pod),
				Ready:     readyStr,
				Restarts:  restarts,
				Age:       formatAge(time.Since(pod.CreationTimestamp.Time)),
				Node:      pod.Spec.NodeName,
			})
		}

		return toolResultJSON(map[string]interface{}{
			"pods":  result,
			"total": len(result),
		})
	}
}

// --- get_pod_detail ---

type podDetail struct {
	Name       string                 `json:"name"`
	Namespace  string                 `json:"namespace"`
	Status     string                 `json:"status"`
	Node       string                 `json:"node"`
	IP         string                 `json:"ip"`
	CreatedAt  string                 `json:"created_at"`
	Labels     map[string]string      `json:"labels"`
	Conditions []podCondition         `json:"conditions"`
	Containers []containerStatus      `json:"containers"`
	Events     []eventInfo            `json:"events"`
}

type podCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type containerStatus struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	State        string `json:"state"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restart_count"`
	StartedAt    string `json:"started_at"`
}

func containerState(cs corev1.ContainerStatus) (string, string) {
	if cs.State.Running != nil {
		return "running", cs.State.Running.StartedAt.Format(time.RFC3339)
	}
	if cs.State.Waiting != nil {
		return "waiting:" + cs.State.Waiting.Reason, ""
	}
	if cs.State.Terminated != nil {
		return "terminated:" + cs.State.Terminated.Reason, ""
	}
	return "unknown", ""
}

func getPodDetailHandler(client kubernetes.Interface) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace, err := request.RequireString("namespace")
		if err != nil {
			return nil, err
		}
		name, err := request.RequireString("name")
		if err != nil {
			return nil, err
		}

		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get pod: %w", err)
		}

		detail := podDetail{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Status:    podStatus(pod),
			Node:      pod.Spec.NodeName,
			IP:        pod.Status.PodIP,
			CreatedAt: pod.CreationTimestamp.Format(time.RFC3339),
			Labels:    pod.Labels,
		}

		for _, c := range pod.Status.Conditions {
			detail.Conditions = append(detail.Conditions, podCondition{
				Type:    string(c.Type),
				Status:  string(c.Status),
				Reason:  c.Reason,
				Message: c.Message,
			})
		}

		for _, cs := range pod.Status.ContainerStatuses {
			state, startedAt := containerState(cs)
			detail.Containers = append(detail.Containers, containerStatus{
				Name:         cs.Name,
				Image:        cs.Image,
				State:        state,
				Ready:        cs.Ready,
				RestartCount: cs.RestartCount,
				StartedAt:    startedAt,
			})
		}

		// Fetch related events
		events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", name),
		})
		if err == nil {
			for _, e := range events.Items {
				detail.Events = append(detail.Events, eventInfo{
					Type:      e.Type,
					Reason:    e.Reason,
					Message:   e.Message,
					Count:     e.Count,
					FirstSeen: e.FirstTimestamp.Format(time.RFC3339),
					LastSeen:  e.LastTimestamp.Format(time.RFC3339),
				})
			}
		}

		return toolResultJSON(detail)
	}
}

// --- get_pod_logs ---

type podLogs struct {
	Logs      string `json:"logs"`
	Container string `json:"container"`
	Truncated bool   `json:"truncated"`
}

const maxLogBytes = 256 * 1024 // 256 KB

func getPodLogsHandler(client kubernetes.Interface) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace, err := request.RequireString("namespace")
		if err != nil {
			return nil, err
		}
		name, err := request.RequireString("name")
		if err != nil {
			return nil, err
		}
		container := request.GetString("container", "")
		tailLines := int64(request.GetInt("tail", 100))
		previous := request.GetBool("previous", false)

		opts := &corev1.PodLogOptions{
			TailLines: &tailLines,
			Previous:  previous,
		}
		if container != "" {
			opts.Container = container
		}

		req := client.CoreV1().Pods(namespace).GetLogs(name, opts)
		stream, err := req.Stream(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get logs: %w", err)
		}
		defer stream.Close()

		var buf bytes.Buffer
		truncated := false
		n, _ := io.Copy(&buf, io.LimitReader(stream, maxLogBytes+1))
		if n > maxLogBytes {
			buf.Truncate(maxLogBytes)
			truncated = true
		}

		resolvedContainer := container
		if resolvedContainer == "" {
			// Get the pod to find the default container name
			pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil && len(pod.Spec.Containers) > 0 {
				resolvedContainer = pod.Spec.Containers[0].Name
			}
		}

		return toolResultJSON(podLogs{
			Logs:      buf.String(),
			Container: resolvedContainer,
			Truncated: truncated,
		})
	}
}

// --- get_events ---

type eventInfo struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Object    string `json:"object,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Message   string `json:"message"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

func getEventsHandler(client kubernetes.Interface) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := request.GetString("namespace", "")
		typeFilter := request.GetString("type", "")

		events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list events: %w", err)
		}

		var result []eventInfo
		for _, e := range events.Items {
			if typeFilter != "" && e.Type != typeFilter {
				continue
			}
			result = append(result, eventInfo{
				Type:      e.Type,
				Reason:    e.Reason,
				Object:    fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
				Namespace: e.Namespace,
				Message:   e.Message,
				Count:     e.Count,
				FirstSeen: e.FirstTimestamp.Format(time.RFC3339),
				LastSeen:  e.LastTimestamp.Format(time.RFC3339),
			})
		}

		return toolResultJSON(map[string]interface{}{
			"events": result,
			"total":  len(result),
		})
	}
}

// --- delete_pod ---

type deletePodResult struct {
	Deleted   bool   `json:"deleted"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Message   string `json:"message"`
}

func deletePodHandler(client kubernetes.Interface) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace, err := request.RequireString("namespace")
		if err != nil {
			return nil, err
		}
		name, err := request.RequireString("name")
		if err != nil {
			return nil, err
		}

		err = client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to delete pod: %w", err)
		}

		return toolResultJSON(deletePodResult{
			Deleted:   true,
			Name:      name,
			Namespace: namespace,
			Message:   "Pod deleted successfully",
		})
	}
}

// --- helpers ---

func toolResultJSON(v interface{}) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(data)), nil
}
