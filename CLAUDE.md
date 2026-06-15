# CLAUDE.md — JianManager 项目指引

## 项目概述

JianManager 是一个面向中小型游戏服务器运营商的多节点管理平台。Go + 内嵌 React 架构，单二进制部署。

## 技术栈

- **后端**: Go 1.22+, Gin, GORM, gRPC
- **前端**: React 19, Vite 6, shadcn/ui, TailwindCSS, TanStack Query, Zustand
- **Bot**: Node.js 20+, Mineflayer, stdin/stdout JSON IPC
- **数据库**: SQLite(dev) / MySQL(prod)
- **通信**: gRPC (Control Plane ↔ Worker), WebSocket (浏览器 ↔ Worker), Unix Socket (Worker ↔ Daemon)

## 开发流程

本项目严格遵循 SDD 开发体系。开发前请阅读：

1. `docs/PRD.md` — 产品需求（FR 列表 + 状态）
2. `docs/ARCHITECTURE.md` — 系统架构（当前真貌）
3. `docs/API.md` — API 参考（当前真貌）
4. `docs/CONVENTIONS.md` — 编码规范
5. `.claude/rules/` — 所有规则（自动加载）
6. `.claude/skills/` — 所有技能（通过 /sdd-xxx 调用）

## 工作流

```
新 Feature:  /sdd-develop-feature FR-XXX
Bug 修复:    /sdd-fix-bug
紧急修复:    /sdd-hotfix
重构:        /sdd-refactor-code
发版:        /sdd-release-version
```

## 关键约束

- **三进程模型**: Control Plane / Worker Node / Bot Worker，进程边界不可逾越
- **通信协议**: gRPC（节点间）、WS（终端直连）、stdin/stdout JSON（Bot IPC）
- **文档演进**: PRD 增量+状态、ARCHITECTURE/API 原地更新、ADR 只追加不删
- **提交规范**: 中文描述，Conventional Commits，禁止 AI 签名

## 目录结构

```
cmd/                    # Go 入口
internal/               # Go 内部包
proto/                  # Protobuf 定义
web/                    # React 前端
bot-worker/             # Node.js Bot Worker
docs/                   # 文档
.claude/rules/          # SDD 规则
.claude/skills/         # SDD 技能
scripts/                # 构建/检查脚本
```
