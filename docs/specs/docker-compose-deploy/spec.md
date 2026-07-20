# 功能规格：Docker Compose 与镜像部署可跑通

> 状态：开发中　·　关联 PRD：FR-354

## 1. 背景与目标

对外提供可复制的 `docker compose` 部署路径。修正 Worker env 与 BindEnv 不一致，补齐 monorepo 构建上下文，DEPLOY 写 Docker 专节。

## 2. 需求

- 打磨 Dockerfile×2 + compose + make docker*
- compose env 与配置键一致
- DEPLOY Docker 专节
- 不做：GHCR、K8s、强制 UI 见 Worker 在线

## 3. 设计

- Worker 正式键 `JIANMANAGER_CONTROL_PLANE_GRPC`；别名 `JIANMANAGER_CONTROL_PLANE`
- CP Dockerfile 拷贝 packages/{ui,tsconfig,eslint-config,devmock} 与 `internal/version/version.go`
- 验收：compose up 后 HTTP 200 + setup API

## 4. 任务

- [x] compose/env/BindEnv/单测
- [x] Dockerfile monorepo 构建修复
- [x] compose up 冒烟（CP `/` 200，`setupRequired`）
- [x] DEPLOY/README/CHANGELOG/PRD 同步

## 5. 验收标准

1. `docker compose build && up` 后 `:8080` 返回前端 — **已验** 200 + setupRequired
2. `GET /api/v1/setup/status` 可达 — **已验**
3. 配置加载测试覆盖 CONTROL_PLANE_GRPC 与别名 — **已验** `TestLoad_*`

证据：`.tmp/acceptance-deploy-readme-security-2026-07-21.md`
