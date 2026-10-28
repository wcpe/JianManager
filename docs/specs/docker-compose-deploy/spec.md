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

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-354 Docker Compose 与镜像部署可跑通（feat，对外 compose 路径产品化）

**需求补充**：打磨 `Dockerfile.control-plane` / `Dockerfile.worker` + `docker-compose.yml` + `make docker*`；校正 compose env 与 Worker/CP 配置键一致；`docs/DEPLOY.md` Docker 专节；验收=干净环境 `compose build && up` 后浏览器可达 CP 并进入登录/引导，配置键错误不静默；**不**要求本 FR 内 UI 见 Worker 在线、**不**推 GHCR/K8s（需 spec）→ `docs/specs/docker-compose-deploy/spec.md`

**交付状态**：✅ 已交付@v0.19.0

## 附：PRD 索引段原始记录（2026-10-26 迁入）

按「PRD 只留索引行，详情见 spec」的约定，以下内容自 PRD 的「活跃 FR 详细规格索引」迁入（保留原文）。

### FR-354~355（对外部署与展示 2026-07-21：Docker/compose 打磨可跑 + Control Plane 一键下载安装脚本；同批 docs：README 安装步骤与 MSW mock 压缩截图入库、根目录 SECURITY.md、上游 sdd 规则对账；**不做** GHCR 发布 / K8s / CP systemd 服务化 / 重做面板 Worker 装机；计划见 `.tmp/brainstorm-deploy-readme-security-2026-07-21.md`；验收证据 `.tmp/acceptance-deploy-readme-security-2026-07-21.md`）

FR-354~355（对外部署与展示 2026-07-21：Docker/compose 打磨可跑 + Control Plane 一键下载安装脚本；同批 docs：README 安装步骤与 MSW mock 压缩截图入库、根目录 SECURITY.md、上游 sdd 规则对账；**不做** GHCR 发布 / K8s / CP systemd 服务化 / 重做面板 Worker 装机；计划见 `.tmp/brainstorm-deploy-readme-security-2026-07-21.md`；验收证据 `.tmp/acceptance-deploy-readme-security-2026-07-21.md`）→ FR-354 `docs/specs/docker-compose-deploy/spec.md`；FR-355 `docs/specs/cp-oneclick-install/spec.md`（均已交付@v0.19.0）；DOCS-README / DOCS-SECURITY / GOV-RULES 不占 §4

### FR-354~355（对外部署与展示 2026-07-21…

FR-354~355（对外部署与展示 2026-07-21：Docker/compose 打磨可跑 + Control Plane 一键下载安装脚本；同批 docs：README 安装步骤与 MSW mock 压缩截图入库、根目录 SECURITY.md、上游 sdd 规则对账；**不做** GHCR 发布 / K8s / CP systemd 服务化 / 重做面板 Worker 装机；计划见 `.tmp/brainstorm-deploy-readme-security-2026-07-21.md`；验收证据 `.tmp/acceptance-deploy-readme-security-2026-07-21.md`） → `docs/specs/docker-compose-deploy/spec.md`

