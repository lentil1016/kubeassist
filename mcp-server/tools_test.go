package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func makeRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "test",
			Arguments: args,
		},
	}
}

func makePod(name, namespace string, phase corev1.PodPhase, containers []corev1.Container, statuses []corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            map[string]string{"app": name},
			CreationTimestamp:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Spec: corev1.PodSpec{
			Containers: containers,
			NodeName:   "node-1",
		},
		Status: corev1.PodStatus{
			Phase:             phase,
			PodIP:             "10.0.0.1",
			ContainerStatuses: statuses,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func TestListPods(t *testing.T) {
	runningPod := makePod("web", "default", corev1.PodRunning,
		[]corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		[]corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 0, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
	)
	pendingPod := makePod("pending", "kube-system", corev1.PodPending,
		[]corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		nil,
	)

	tests := []struct {
		name       string
		args       map[string]interface{}
		wantTotal  int
		wantFirst  string
	}{
		{
			name:      "all namespaces",
			args:      map[string]interface{}{},
			wantTotal: 2,
		},
		{
			name:      "filter by namespace",
			args:      map[string]interface{}{"namespace": "default"},
			wantTotal: 1,
			wantFirst: "web",
		},
		{
			name:      "filter by status",
			args:      map[string]interface{}{"status": "Pending"},
			wantTotal: 1,
			wantFirst: "pending",
		},
		{
			name:      "no match",
			args:      map[string]interface{}{"status": "Failed"},
			wantTotal: 0,
		},
	}

	client := fake.NewSimpleClientset(runningPod, pendingPod)
	handler := listPodsHandler(client)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handler(context.Background(), makeRequest(tt.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var out struct {
				Pods  []podInfo `json:"pods"`
				Total int       `json:"total"`
			}
			parseResult(t, result, &out)

			if out.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", out.Total, tt.wantTotal)
			}
			if tt.wantFirst != "" && len(out.Pods) > 0 && out.Pods[0].Name != tt.wantFirst {
				t.Errorf("first pod = %s, want %s", out.Pods[0].Name, tt.wantFirst)
			}
		})
	}
}

func TestGetPodDetail(t *testing.T) {
	pod := makePod("web", "default", corev1.PodRunning,
		[]corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		[]corev1.ContainerStatus{{
			Name: "app", Image: "nginx:1.25", Ready: true, RestartCount: 2,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
				StartedAt: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
			}},
		}},
	)

	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "web.abc", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web", Namespace: "default"},
		Type:           "Normal",
		Reason:         "Pulled",
		Message:        "Successfully pulled image",
		Count:          1,
		FirstTimestamp:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		LastTimestamp:   metav1.NewTime(time.Now().Add(-1 * time.Hour)),
	}

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantErr bool
	}{
		{
			name: "existing pod",
			args: map[string]interface{}{"namespace": "default", "name": "web"},
		},
		{
			name:    "missing namespace",
			args:    map[string]interface{}{"name": "web"},
			wantErr: true,
		},
		{
			name:    "pod not found",
			args:    map[string]interface{}{"namespace": "default", "name": "nonexistent"},
			wantErr: true,
		},
	}

	client := fake.NewSimpleClientset(pod, event)
	handler := getPodDetailHandler(client)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handler(context.Background(), makeRequest(tt.args))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var detail podDetail
			parseResult(t, result, &detail)

			if detail.Name != "web" {
				t.Errorf("name = %s, want web", detail.Name)
			}
			if len(detail.Containers) != 1 || detail.Containers[0].RestartCount != 2 {
				t.Errorf("unexpected containers: %+v", detail.Containers)
			}
			if detail.IP != "10.0.0.1" {
				t.Errorf("ip = %s, want 10.0.0.1", detail.IP)
			}
		})
	}
}

func TestGetPodLogs(t *testing.T) {
	pod := makePod("web", "default", corev1.PodRunning,
		[]corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		[]corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
	)

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantErr bool
	}{
		{
			name: "default options",
			args: map[string]interface{}{"namespace": "default", "name": "web"},
		},
		{
			name: "with container and tail",
			args: map[string]interface{}{"namespace": "default", "name": "web", "container": "app", "tail": 50.0},
		},
		{
			name:    "missing name",
			args:    map[string]interface{}{"namespace": "default"},
			wantErr: true,
		},
	}

	client := fake.NewSimpleClientset(pod)
	handler := getPodLogsHandler(client)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler(context.Background(), makeRequest(tt.args))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			// fake client returns empty logs, which is fine — we're testing the handler logic
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetEvents(t *testing.T) {
	events := []runtime.Object{
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "evt1", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
			Type:           "Warning",
			Reason:         "BackOff",
			Message:        "Back-off restarting",
			Count:          5,
			FirstTimestamp:  metav1.NewTime(time.Now().Add(-10 * time.Minute)),
			LastTimestamp:   metav1.NewTime(time.Now()),
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "evt2", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
			Type:           "Normal",
			Reason:         "Pulled",
			Message:        "Successfully pulled",
			Count:          1,
			FirstTimestamp:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			LastTimestamp:   metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
	}

	tests := []struct {
		name      string
		args      map[string]interface{}
		wantTotal int
	}{
		{
			name:      "all events",
			args:      map[string]interface{}{},
			wantTotal: 2,
		},
		{
			name:      "warning only",
			args:      map[string]interface{}{"type": "Warning"},
			wantTotal: 1,
		},
		{
			name:      "normal only",
			args:      map[string]interface{}{"type": "Normal"},
			wantTotal: 1,
		},
		{
			name:      "filter by namespace",
			args:      map[string]interface{}{"namespace": "kube-system"},
			wantTotal: 0,
		},
	}

	client := fake.NewSimpleClientset(events...)
	handler := getEventsHandler(client)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handler(context.Background(), makeRequest(tt.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var out struct {
				Events []eventInfo `json:"events"`
				Total  int         `json:"total"`
			}
			parseResult(t, result, &out)

			if out.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", out.Total, tt.wantTotal)
			}
		})
	}
}

func TestDeletePod(t *testing.T) {
	pod := makePod("victim", "default", corev1.PodRunning,
		[]corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		nil,
	)

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantErr bool
	}{
		{
			name: "delete existing pod",
			args: map[string]interface{}{"namespace": "default", "name": "victim"},
		},
		{
			name:    "delete nonexistent pod",
			args:    map[string]interface{}{"namespace": "default", "name": "ghost"},
			wantErr: true,
		},
		{
			name:    "missing namespace",
			args:    map[string]interface{}{"name": "victim"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh client per test since delete mutates state
			client := fake.NewSimpleClientset(pod.DeepCopy())
			handler := deletePodHandler(client)

			result, err := handler(context.Background(), makeRequest(tt.args))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var out deletePodResult
			parseResult(t, result, &out)

			if !out.Deleted {
				t.Error("expected deleted=true")
			}
			if out.Name != "victim" {
				t.Errorf("name = %s, want victim", out.Name)
			}

			// Verify pod is gone
			_, err = client.CoreV1().Pods("default").Get(context.Background(), "victim", metav1.GetOptions{})
			if err == nil {
				t.Error("pod should have been deleted")
			}
		})
	}
}

func parseResult(t *testing.T, result *mcp.CallToolResult, v interface{}) {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("empty result content")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), v); err != nil {
		t.Fatalf("failed to parse result JSON: %v\nraw: %s", err, text.Text)
	}
}
