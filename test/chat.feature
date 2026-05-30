Feature: End-to-end chat pipeline
  As a user of KubeAssist
  I want to ask about cluster status in natural language
  So that I can quickly identify problematic pods

  Background:
    Given a Kubernetes cluster with the following pods:
      | name          | namespace | status             | ready | restarts |
      | healthy-pod   | default   | Running            | 1/1   | 0        |
      | crash-pod     | default   | CrashLoopBackOff   | 0/1   | 5        |
    And the MCP Server is running with a fake Kubernetes client
    And the Backend is running with a mock Claude API
    And the mock Claude API is configured to:
      | request | response                                               |
      | 1st     | tool_use: list_pods with empty args                    |
      | 2nd     | text: "Found 2 pods. crash-pod is in CrashLoopBackOff" |

  Scenario: User asks about abnormal pods
    When I send a POST request to /api/chat with message "有没有异常的 pod"
    Then I receive an SSE stream
    And the stream contains events in this order:
      | event       | description                                |
      | tool_call   | tool=list_pods                             |
      | tool_result | tool=list_pods, result contains 2 pods     |
      | message     | text contains "crash-pod"                  |
      | done        | stream complete                            |
    And the tool_result contains pod "crash-pod" with status "CrashLoopBackOff"
    And the tool_result contains pod "healthy-pod" with status "Running"
