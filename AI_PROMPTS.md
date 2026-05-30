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
