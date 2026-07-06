# 实现进度 — FR-080 Worker 一键安装 / 傻瓜部署

> 关联 ADR-020。状态随开发更新，完成后标 ✅。

## 任务清单

### 地基
- [x] ADR-020：节点 enrollment 一键安装 + 部署机制（含 FR-081 自更新原则）
- [x] PRD FR-080 → 🔨 in-progress
- [x] spec api.md / impl.md

### CP 侧（control-plane）
- [x] `model.NodeEnrollToken`（哈希/过期/消费状态/预设名）+ AutoMigrate
- [x] `service.EnrollTokenService`：签发（随机明文→存哈希→明文一次性返回）/校验消费（一次性原子）/列出/吊销
- [x] 单测：签发、校验（有效/过期/已消费/已吊销/不存在）、一次性消费并发安全、列出、吊销
- [x] `grpc.ControlPlaneHandler.Register` 分叉：新节点必须带 token、老节点重注册放行；注入 EnrollTokenService
- [x] 单测：Register 五条路径（新节点+有效 token / 新节点缺 token / 新节点过期 token / 新节点已消费 token / 老节点无 token 重注册）
- [x] HTTP 路由：`POST /nodes/enroll-token`、`GET /nodes/enroll-tokens`、`DELETE /nodes/enroll-tokens/:id`（平台管理员 + 审计）
- [x] 一键安装命令拼装（Linux/Windows，含 CP gRPC 地址 + token）
- [x] main.go 接线（构造服务、注入 handler、挂路由）

### Worker 侧（worker）
- [x] 真正加载 `worker.yaml`（viper，env 覆盖）——补 FR-004 遗留缺口
- [x] 本地身份持久化 `etc/node-identity.json`（读/写，0600，复用 node_uuid/secret）
- [x] `register.Register` 扩展：带 enroll token（metadata）/ 带既有身份（重注册）
- [x] main.go：身份文件优先 → 否则用 enroll token 首注册 → 持久化身份
- [x] 单测：身份文件读写、注册入参选择逻辑

### 安装脚本（build）
- [x] `scripts/install-worker.sh`（Linux/macOS）
- [x] `scripts/install-worker.ps1`（Windows）
- [x] **BUG-B 修复**：CP 经 `go:embed` 内嵌脚本（`internal/controlplane/embed/install_scripts.go`，源 `embed/install-scripts/`，`make embed-install-scripts` 从 canonical `scripts/` 同步）并匿名托管 `GET /install-worker.sh`、`GET /install-worker.ps1`（`router/install_script.go`，根路径、先于 SPA 回退），修「一键命令 curl `<cp>/install-worker.sh` 404」根因。内嵌副本与 canonical 字节一致由 `embed/install_scripts_test.go` 守护；签发响应补 `scriptBaseUrl` 供前端拼手动步骤
- [x] 下载资产名对齐 ADR-036 GitHub Releases 契约（`worker-<os>-<arch>[.exe]`），FR-190 起默认基址改为 CP-local Worker 资产 URL，`--binary` 兜底保留
- [x] FR-190 起一键命令默认签发 CP-local Worker 资产 `--download-url`；`enroll.binary_url` 仅作为显式覆盖源保留，单测覆盖默认 CP-local 与覆盖源两条路径

### 前端（web）
- [x] 「添加节点」向导：调签发端点 → 展示一键命令（复制）+ token 一次性提示
- [x] i18n zh/en（仅新增本 FR 键）

### 文档同步
- [x] `docs/API.md`：enroll-token 端点 + Register 行为说明
- [x] `docs/ARCHITECTURE.md`：部署章节补 enrollment 一键安装流程
- [x] `CHANGELOG.md` 末尾追加（只加不改）

### 完成判据
- [x] `go build ./...` + `go vet ./...` 绿
- [x] 相关 `go test` 绿（service/grpc/router/register 全过；新增下载基址命令单测）
- [x] **BUG-B 回归**：`router/install_script_test.go`（匿名 GET 脚本端点 200 + 内容校验，red→green）、`embed/install_scripts_test.go`（内嵌非空 + 与 canonical 字节一致防漂移）绿
- [x] 前端 tsc / lint / build 绿
- [x] 安装脚本 POSIX `sh -n` / PowerShell Parser 语法校验通过
- [x] 真机：2026-07-04 本机临时 CP 托管 `install-worker.ps1`，PowerShell 按 `iex (iwr ...).Content` 拉取脚本，使用 `-Binary` 本地兜底启动 Worker → 节点 `fr080-e2e` 自动注册上线（CP `/nodes` 返回 `status=1`、gRPC 19101 / WS 19102）；Worker setup 写出 `worker.yml`，日志确认 `enrollment token 未写入`。FR-190 的 CP-local 资产下载真机链路待 release 资产可用环境继续覆盖。

## 设计要点（落地备忘）
- token 经 gRPC metadata header `enroll-token`，不改 proto。
- 新节点（name 未命中）卡 token；老节点（name 命中）重注册不卡 token——为不破网的有意权衡。
- 身份文件存数据根 `etc/`（ADR-010），token 不留盘。
