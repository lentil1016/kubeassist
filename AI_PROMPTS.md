# AI 协同研发报告

本文档记录 KubeAssist 开发过程中 AI 工具的使用情况。

## 开发环境

- **AI 工具**: Claude Code (Claude Opus)
- **开发方法**: Spec-driven development — 先定义规格文档，再按规格实现

---

## 阶段一：需求分析与架构设计

**目标**: 在写任何代码之前，先产出完整的技术规格文档。

**使用方式**: 向 Claude 描述系统的整体需求（MCP 协议 + K8s 运维 + 对话式 UI），要求它按照 OpenAPI 风格输出 MCP Tools 定义和 HTTP API 定义，并包含架构拓扑图和安全设计。

**AI 产出**: 生成了 `docs/spec.md` 技术规格文档，涵盖系统架构、MCP Tools 定义（5 个工具的输入输出 schema）、HTTP API 设计、安全模型、部署方案。

**人工审查**: （spec review 后补充）

---

## 阶段二：项目脚手架

**目标**: 按照 spec.md 定义的项目结构，创建可编译、可部署的项目骨架，为后续功能实现提供基础。

**使用方式**: 将 spec 中的组件结构、部署拓扑、镜像命名作为约束条件提供给 Claude，要求它创建三个组件的最小可运行代码（hello world HTTP server）、Dockerfile（多阶段构建）、完整的 Kustomize 部署清单（含 RBAC、Secret placeholder），以及根目录的 Makefile 和 .gitignore。

**AI 产出**:
- `mcp-server/`: Go module + main.go（:3000 HTTP server）+ Dockerfile（distroless 多阶段构建）
- `backend/`: Go module + main.go（:8080 HTTP server）+ Dockerfile（distroless 多阶段构建）
- `frontend/`: Vite + React + TypeScript 初始化项目 + Dockerfile（node build + nginx serve 多阶段构建）+ nginx.conf（含 /api/ 反向代理配置）
- `deploy/base/`: 完整 Kustomize 清单 — namespace、ServiceAccount、ClusterRole/Binding、三组件 Deployment + Service、Secret placeholder，Backend Deployment 设置了 `automountServiceAccountToken: false`
- 根目录 Makefile（8 个 targets）和 .gitignore
- 验证通过：两个 Go 模块编译成功，`kubectl kustomize` 渲染正常

**人工审查**: （scaffold review 后补充）

---

## 阶段三：端到端链路打通

**目标**: 实现最小可用的完整链路 — 用户在浏览器对话界面输入自然语言，Backend 调用 Claude API 做 tool calling，通过 MCP Server 查询 K8s 集群，将结果流式返回前端展示。

**使用方式**: 以 spec.md 为实现合约，要求 Claude 按照 MCP Server → Backend → Frontend 的顺序实现三个组件。MCP Server 使用 mcp-go 库实现 Streamable HTTP transport，仅实现 `list_pods` 一个 tool；Backend 使用 Claude Messages API 的原生 HTTP 流式调用 + mcp-go client 转发 tool call；Frontend 使用 React + react-markdown 实现对话 UI，通过 SSE 流式接收响应。开发过程中遇到 mcp-go v0.54.1 的 API 变更（`InitializeParams` / `CallToolParams` 等类型名变化），通过阅读源码修复。

**AI 产出**:
- `mcp-server/main.go`: 完整 MCP Server 实现 — K8s client 初始化（支持 in-cluster 和 kubeconfig 回退）、`list_pods` tool 注册与处理（含 namespace/status 过滤、pod 状态解析、容器 ready/restart 统计）、Streamable HTTP transport 启动
- `backend/main.go`: 完整编排层实现 — MCP client 初始化与 tool 发现、MCP tools → Claude tool 格式转换、`POST /api/chat` 处理（Claude 流式调用 + SSE 事件解析 + tool_use 循环 + MCP tool 转发 + SSE 流式输出）、CORS 支持
- `frontend/src/App.tsx` + `App.css`: 对话式 UI — 暗色主题、SSE 流式消息渲染、tool call 可视化、Markdown 渲染（表格/代码块）、空状态引导
- `frontend/vite.config.ts`: 开发环境 `/api` 反向代理配置
- 三个组件均编译/构建通过（Go build + TypeScript + Vite production build）
- 本地验证通过：在 ACP 集群中创建 3 个测试 Pod（Running / CrashLoopBackOff / Pending），通过 `POST /api/chat` 发送"帮我看看 kubeassist-test 命名空间里有没有异常的 pod"，Claude 正确调用 `list_pods` 并生成包含表格、状态标记和排查建议的 Markdown 分析报告，完整 SSE 事件流（145 message + 1 tool_call + 1 tool_result + 1 done）

**人工审查**: （e2e verification 后补充）

---

## 阶段四：补全 MCP Tools 与单元测试

**目标**: 实现 spec 中定义的全部 5 个 MCP tools，为 MCP Server 编写单元测试，并强化 delete_pod 的安全约束。

**使用方式**: 以 spec.md 中的 tool schema 为实现合约，要求 Claude 实现 get_pod_detail、get_pod_logs、get_events、delete_pod 四个剩余 tool，并重构代码将 tool handler 与 k8s client 解耦（依赖注入 `kubernetes.Interface`）以支持 fake client 测试。同时更新 Backend 的 system prompt，增加 delete_pod 的安全协议（必须先向用户确认再执行）。

**AI 产出**:
- `mcp-server/tools.go`: 所有 5 个 tool handler 的完整实现，使用依赖注入模式接受 `kubernetes.Interface`
  - `get_pod_detail`: 获取 pod 详情 + conditions + container statuses + 关联 events
  - `get_pod_logs`: 支持 container/tail/previous 参数，含 256KB 截断保护
  - `get_events`: 支持 namespace + type 过滤
  - `delete_pod`: 执行删除操作
- `mcp-server/main.go`: 重构为 `registerTools()` 函数统一注册
- `mcp-server/tools_test.go`: 22 个 table-driven 单元测试，使用 `k8s.io/client-go/kubernetes/fake`，覆盖所有 5 个 tool 的正常路径、错误路径和边界条件
- `backend/main.go`: system prompt 增加 delete_pod 安全协议（4 步确认流程）
- 本地验证通过 3 项测试：
  1. `get_pod_logs` — Claude 组合调用 `get_pod_detail` + `get_pod_logs(previous: true)` 获取崩溃日志
  2. `get_events` — Claude 正确过滤 Warning 事件并分析 BackOff + FailedScheduling
  3. `delete_pod` — Claude 遵循安全协议，输出确认提示而未调用 delete_pod tool

**人工审查**: （review 后补充）
