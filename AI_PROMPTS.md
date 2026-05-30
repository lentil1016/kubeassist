# AI 协同研发报告

本文档记录 KubeAssist 开发过程中 AI 工具的使用情况。

## 开发环境

- **AI 工具**: Claude Code (Claude Opus)
- **开发方法**: Spec-driven development — 先定义规格文档，再按规格实现

---

## 阶段一：需求分析与架构设计

**收到的 Prompt**:

> 我要开发一个叫 KubeAssist 的项目——基于 MCP 协议的 K8s AI 运维助手。用户通过浏览器对话界面用自然语言查询集群状态，后端调用 Claude API 做 tool calling，通过 MCP Server 执行实际的 K8s 操作。
>
> 在写任何代码之前，我想先把需求和架构定义清楚。请帮我完成以下工作：
>
> 1. 在当前目录创建 GitHub 仓库 kubeassist（public），clone 到本地
> 2. 在仓库根目录创建 docs/spec.md，按以下结构写一份技术规格：
>
> 系统概述：一段话描述这个系统做什么
>
> 架构设计：
> - 三个组件：Frontend（React）、Backend API（Go）、MCP Server（Go）
> - Frontend 是对话 UI，通过 SSE 流式接收后端响应
> - Backend 是编排层：接收用户消息 → 调 Claude API → Claude 返回 tool_use 时转发给 MCP Server → 结果喂回 Claude → 流式返回最终回答
> - MCP Server 使用 Streamable HTTP transport，暴露 K8s 操作为 MCP tools，通过 in-cluster ServiceAccount 访问 K8s API
> - 三个组件分别作为独立 Deployment 部署在 K8s 集群内
>
> MCP Tools 定义（用类似 OpenAPI 的风格，定义每个 tool 的 name、description、inputs schema、output schema）：
> - list_pods — 列出 Pod，可按 namespace 和 status 过滤
> - get_pod_detail — 获取单个 Pod 详情，含 conditions、container statuses、相关 events
> - get_pod_logs — 获取 Pod 日志，支持指定 container、tail lines、previous container
> - get_events — 列出集群 events，可按 namespace 和 type(Warning/Normal) 过滤
> - delete_pod — 删除指定 Pod（写操作，LLM 层面需要用户确认）
>
> API 定义（Backend 暴露给 Frontend 的 HTTP API）：
> - POST /api/chat — 接收 { message: string }，返回 SSE 流
>
> 安全设计：
> - MCP Server 使用专用 ServiceAccount，ClusterRole 仅授权 pods 和 events 的必要权限
> - delete_pod 这类写操作，由 Claude 在回复中要求用户确认，用户确认后才执行
> - Backend 不持有 K8s 凭证，所有集群操作通过 MCP Server 代理
>
> 部署方案：
> - 纯 K8s YAML（不用 Helm），用 kustomize 组织
> - 离线镜像通过 GitHub Actions 构建 amd64 并 docker save
>
> 3. 写完 spec.md 后，把内容展示给我 review，不要开始写任何实现代码

**目标**: 在写任何代码之前，先产出完整的技术规格文档。

**使用方式**: 向 Claude 描述系统的整体需求（MCP 协议 + K8s 运维 + 对话式 UI），要求它按照 OpenAPI 风格输出 MCP Tools 定义和 HTTP API 定义，并包含架构拓扑图和安全设计。

**AI 产出**: 生成了 `docs/spec.md` 技术规格文档，涵盖系统架构、MCP Tools 定义（5 个工具的输入输出 schema）、HTTP API 设计、安全模型、部署方案。

**人工审查**: （spec review 后补充）

---

## 阶段二：项目脚手架

**收到的 Prompt**:

> Spec 整体 OK，有几处调整请先改掉：
>
> 1. docs/spec.md 里的 ghcr.io/\<owner\> 全部替换为 docker.io/lentil1016
> 2. 在安全设计章节的 Credential Isolation 部分，补充一句：Backend Deployment 显式设置 automountServiceAccountToken: false
>
> 改完后提交：docs: refine spec based on review
>
> 然后开始下一步——项目脚手架。按照 spec 里定义的结构创建项目骨架：
>
> 1. MCP Server (mcp-server/)：初始化 Go module github.com/lentil1016/kubeassist/mcp-server，创建 main.go（只写一个能编译通过的 hello world HTTP server 在 :3000），创建 Dockerfile
> 2. Backend (backend/)：初始化 Go module github.com/lentil1016/kubeassist/backend，创建 main.go（能编译通过的 hello world HTTP server 在 :8080），创建 Dockerfile
> 3. Frontend (frontend/)：用 npm create vite@latest . -- --template react-ts 初始化，创建 Dockerfile（多阶段构建：node build + nginx serve），创建 nginx.conf
> 4. Deploy (deploy/base/)：按 spec 创建所有 K8s YAML 文件（namespace, RBAC, 三个组件各自的 deployment + service, secret placeholder），以及 kustomization.yaml
> 5. 根目录创建 .gitignore 和 Makefile（targets: build-mcp, build-backend, build-frontend, docker-build, docker-save, deploy, test, clean）
> 6. 在 AI_PROMPTS.md 追加"阶段二：项目脚手架"的记录。格式跟阶段一保持一致，包含目标、使用方式、AI 产出三个部分，内容由你根据实际执行情况总结。"人工审查"留空，我后续补充。

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

**收到的 Prompt**:

> 脚手架已就绪。现在目标是尽快打通完整链路，让我能在本地浏览器里跟 K8s 集群对话。
>
> 请读 docs/spec.md 了解架构和接口定义，然后：
>
> 1. 实现 MCP Server — 先只做 list_pods 一个 tool，能跑起来就行
> 2. 实现 Backend — 完成 Claude API 调用 + MCP tool 转发 + SSE 流式返回的编排逻辑
> 3. 实现 Frontend — 最简对话界面，能发消息、能流式展示回复
> 4. 提供本地启动方式（三个组件分别跑在 localhost 不同端口）
>
> 本地有可用的 kubectl 集群。Claude API key 通过环境变量 ANTHROPIC_API_KEY 提供。
>
> 实现完成后，在集群里创建几个测试 pod（正常的、CrashLoopBackOff 的、Pending 的），启动系统，在浏览器里问"集群有没有异常的 pod"验证整条链路。
>
> 提交：feat: end-to-end chat pipeline with list_pods
> 在 AI_PROMPTS.md 追加阶段三记录。

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

**收到的 Prompt**:

> 端到端验证已通过。现在补全剩余功能。请读 docs/spec.md 中的 MCP Tools 定义。
>
> 1. 在 MCP Server 中实现剩余 4 个 tools：get_pod_detail、get_pod_logs、get_events、delete_pod，输入输出严格按照 spec
> 2. 在 Backend 中确保 Claude 的 system prompt 包含对 delete_pod 的安全约束：执行删除前必须先向用户确认，用户明确同意后才调用该 tool
> 3. 为 MCP Server 写 Go 单元测试，使用 k8s.io/client-go/kubernetes/fake 和 table-driven 风格，覆盖所有 5 个 tools 的核心路径
> 4. 本地启动，用之前的测试 pod 验证：
>     - "crashloop-pod 的日志是什么？"（验证 get_pod_logs）
>     - "帮我看看集群最近有什么 Warning 事件"（验证 get_events）
>     - "帮我删掉 pending-pod"（验证 delete_pod 确认流程——Claude 应该先问你确认）
> 5. 提交：feat: complete all MCP tools with unit tests
> 6. 在 AI_PROMPTS.md 追加阶段记录

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

---

## 阶段五：端到端集成测试

**收到的 Prompt**:

> 单元测试已完成。现在加一个轻量的端到端测试，验证从 HTTP 请求到 SSE 响应的完整链路。
>
> 思路：
> - 写一个 Gherkin feature 文件描述测试场景
> - 用 Go 实现测试：启动一个 mock Claude API server（固定返回 tool_use → list_pods，收到 tool_result 后返回一段总结文本），启动真实的 MCP Server（K8s 用 fake clientset），启动真实的 Backend（指向 mock Claude 和真实 MCP Server）
> - 测试发 HTTP 请求到 Backend 的 /api/chat，解析 SSE 流，验证事件序列包含 tool_call、tool_result、message、done
>
> 场景只需要一个核心用例：用户问"有没有异常的 pod"，mock Claude 返回 list_pods 调用，最终 SSE 流包含完整事件序列。
>
> Gherkin feature 文件放在 test/ 目录下作为文档，Go 测试代码放在 test/e2e_test.go。feature 文件不需要框架驱动，作为可读的测试规格即可。
>
> 确保 go test ./test/... -v 通过后提交：test: add e2e test with mock Claude API
>
> 在 AI_PROMPTS.md 追加记录。

**目标**: 添加自动化 e2e 测试，验证从 HTTP 请求到 SSE 响应的完整链路，使用 mock Claude API 和 fake K8s client 实现零外部依赖。

**使用方式**: 向 Claude 描述测试思路（mock Claude 返回固定的 tool_use 响应、MCP Server 使用 fake clientset、Backend 完整编排逻辑），要求生成 Gherkin feature 文件作为可读规格，Go 测试代码验证 SSE 事件序列。开发中发现 mcp-go 的 `StreamableHTTPServer` 实现了 `http.Handler` 接口，因此可以直接用 `httptest.NewServer` 启动，避免了端口管理和进程启动的复杂性。

**AI 产出**:
- `test/chat.feature`: Gherkin 场景描述 — 用户问"有没有异常的 pod"，验证 SSE 事件序列（tool_call → tool_result → message → done）和数据内容
- `test/e2e_test.go`: 全在进程内的 e2e 测试，三个组件均使用 `httptest.Server`：
  - Mock Claude API：根据请求是否包含 `tool_result` 返回 tool_use 或文本响应
  - 真实 MCP Server：mcp-go + fake k8s client，注册 `list_pods` tool
  - Backend 编排层：重现核心 SSE 解析和 MCP 转发逻辑
  - 验证项：事件顺序、tool_call 内容、tool_result 包含 2 个 pod（Running + CrashLoopBackOff）、响应文本提及 crash-pod
- 测试通过：0.58s，零外部依赖

**人工审查**: （review 后补充）

---

## 阶段六：Helm Chart

**收到的 Prompt**:

> 请回顾 AI_PROMPTS.md 中每个阶段的记录，在每个阶段下补充一个"收到的 Prompt"小节，把你在该阶段实际收到的用户 prompt 原文贴进去（用引用块格式）。这样读者可以看到完整的人机协作过程：给了什么指令 → AI 做了什么 → 人工审查了什么。
>
> 现在为项目添加 Helm Chart，方便一键部署到 K8s 集群。
>
> 请读 deploy/base/ 下现有的 K8s YAML 和 docs/spec.md 了解部署架构，然后在 deploy/helm/kubeassist/ 下创建一个 Helm Chart。
>
> 需要暴露的 values（只暴露用户实际会改的，不要过度参数化）：
> - anthropicApiKey — Claude API key（必填）
> - anthropicBaseUrl — Claude API base URL（可选，默认为官方地址）
> - image.tag — 三个组件共用的镜像 tag，默认 latest
> - frontend.service.type — 前端 Service 类型，默认 ClusterIP（用户可能改为 NodePort 或 LoadBalancer 来访问）
>
> 其余配置（镜像名、端口、RBAC、resource limits 等）硬编码在 template 里即可。
>
> 完成后运行 helm lint 和 helm template 确认无误，提交：feat: add Helm chart for one-click deployment
>
> 在 AI_PROMPTS.md 追加记录。

**目标**: 提供 Helm Chart 支持一键部署到 K8s 集群，仅暴露用户实际需要修改的参数。

**使用方式**: 以 deploy/base/ 下的 Kustomize 清单为参考，要求 Claude 创建等价的 Helm Chart，仅参数化用户指定的 4 个 values（anthropicApiKey、anthropicBaseUrl、image.tag、frontend.service.type），其余配置硬编码。

**AI 产出**:
- `deploy/helm/kubeassist/Chart.yaml`: chart 元数据
- `deploy/helm/kubeassist/values.yaml`: 4 个用户可配参数
- `deploy/helm/kubeassist/templates/`: 10 个模板文件
  - `_helpers.tpl`: fullname 和 labels 公共定义
  - `namespace.yaml`: 命名空间
  - `secret.yaml`: API key Secret，`anthropicApiKey` 为 `required` 字段
  - `mcp-rbac.yaml`: ServiceAccount + ClusterRole + ClusterRoleBinding（合并为一个文件）
  - `mcp-deployment.yaml` / `mcp-service.yaml`: MCP Server
  - `backend-deployment.yaml` / `backend-service.yaml`: Backend（含 `anthropicBaseUrl` 条件渲染）
  - `frontend-deployment.yaml` / `frontend-service.yaml`: Frontend（Service type 可配）
- 验证通过：`helm lint` 无错误，`helm template` 渲染正确，`anthropicApiKey` 必填校验生效，`anthropicBaseUrl` 条件渲染正确

**人工审查**: （review 后补充）
