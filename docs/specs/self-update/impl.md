# 实现计划 — FR-081 面板自更新

> 关联 FR: FR-081 | 关联 ADR: ADR-020 §4 | 状态: 🔨 in-progress

## 模块划分

### 公共：二进制自替换 + sha256（`internal/platform/selfupdate`）

新建包，CP 与 Worker 共用：

- `selfupdate.Download(ctx, url, sha256, destPath, allowInsecure) error`：流式下载到临时文件、边下边算 sha256，校验不符删除临时文件返回 `ErrChecksumMismatch`。
- `selfupdate.ReplaceExecutable(newPath string) error`：把 `newPath` 原子替换当前可执行文件（`os.Executable()` 定位）。Windows 不能覆盖运行中的 exe → 先把当前 exe rename 到 `<exe>.old`（可删可不删），再把 newPath rename/copy 到原路径；Unix 直接 rename（inode 替换，运行中进程不受影响）。
- `selfupdate.Restart() error`：re-exec 自身（`os.StartProcess` 用原 argv/env 拉起新进程，父进程退出）。供裸跑场景；系统服务托管时退出即由服务拉起。
- 纯逻辑可测：sha256 计算、feed 解析、制品选择。

### CP feed 解析 + 制品选择（`internal/controlplane/service/selfupdate.go`）

- `Feed` / `Artifact` 结构（对齐 api.md JSON 契约）。
- `FetchFeed(ctx) (*Feed, error)`：HTTP GET feed_url 解析 JSON；未配 feed_url 返回 `ErrUpdateNotConfigured`。
- `SelectArtifact(feed, component, os, arch) (*Artifact, bool)`：精确匹配。
- 无 feed 但配了 `binary_base_url` 时按约定拼 url（版本/sha256 未知则仅支持「按 base 直降」简化路径——本 FR 以 feed 为主，base_url 为兜底）。
- `CheckUpdate(ctx) (*CheckResult, error)`：组装 CP 自身 + 各节点版本对比。节点版本经 gRPC `GetVersion` 并发拉取。
- `UpgradeControlPlane(ctx, version) error`：选 CP 平台制品 → selfupdate.Download → ReplaceExecutable → 触发延迟 Restart。
- `UpgradeNode(ctx, nodeID, version) (from, to string, err error)`：选节点平台制品 → gRPC `UpgradeWorker` 下发 url/sha256。
- `Rollout`：内存态全网编排器（互斥锁保护单次 rollout 状态），逐节点串行调 UpgradeNode，记录逐节点 state/error，供 `GET /self-update/rollout` 查询。失败隔离、可对失败节点重试。

### Worker 侧（`internal/worker/grpc/selfupdate_ops.go`）

- `GetVersion`：返回 `version.Version` + GOOS/GOARCH。
- `UpgradeWorker`：selfupdate.Download → 校验 → ReplaceExecutable → 起 goroutine 短延迟后 Restart → 同步返回 success。
- 需要把 Worker 自身版本暴露：Worker `Server` 已可直接 import `internal/version`。

### proto

- `proto/worker.proto` 加 `GetVersion` / `UpgradeWorker` RPC + 4 个 message → protoc 重新生成 workerpb。

### HTTP 路由（`internal/controlplane/router/selfupdate.go`）

- `SelfUpdateHandler`，挂 admin 组（仅平台管理员）。
- 端点：`GET /self-update/check`、`POST /self-update/control-plane/upgrade`、`POST /self-update/nodes/:id/upgrade`、`POST /self-update/nodes/upgrade-all`、`GET /self-update/rollout`。
- 审计：`self_update.control_plane` / `self_update.node` / `self_update.rollout`。

### 配置

- CP config 加 `UpdateConfig{ FeedURL, BinaryBaseURL, AllowInsecure }`（mapstructure `update`），默认空 + viper 默认值。

### 前端

- `web/src/api/selfUpdate.ts`：`useSelfUpdateCheck` / `useUpgradeControlPlane` / `useUpgradeNode` / `useUpgradeAllNodes` / `useRollout` hooks。
- 节点页（NodesPage）头部加「检查更新」按钮 → 打开 `SelfUpdateDialog`：展示 CP 当前/最新版本 + 升级按钮，节点列表逐行当前版本 + 升级按钮 + 全网升级 + 逐节点进度（rollout 轮询）。
- i18n：仅加 `selfUpdate.*` 键（zh + en）。

## 测试先行（TDD）

| 测试 | 覆盖 |
|---|---|
| `selfupdate/checksum_test.go` | sha256 计算 + Download 校验匹配/不匹配（用 httptest server） |
| `selfupdate/replace_test.go` | ReplaceExecutable 原子替换一个临时「假二进制」文件（不真重启）+ 文件内容确为新版本 |
| `service/selfupdate_test.go` | Feed JSON 解析、SelectArtifact 精确匹配/缺失、版本比较、Rollout 逐节点状态机（注入 fake upgrade fn：成功/失败/混合） + 未配源错误 |
| `router/selfupdate_test.go` | 端点鉴权（非管理员 403）、未配源 409、check happy path、rollout 查询 |
| `grpc` Worker | `GetVersion` 返回版本；`UpgradeWorker` 校验不符不替换（注入临时 exe 路径 + httptest） |

## 完成判据

- `go build ./...` 不 panic + `go vet` + `go test` 绿；proto 经 protoc 重生成不 panic。
- 前端 tsc/lint/build 绿。
- 真机（CP/Worker 旧→新在线升级，daemon 下游戏服不掉）：二进制热替换重启较重，难做则标「待真机验」。

## 待办勾选

- [x] 写本 spec + api.md
- [ ] proto 加 RPC + protoc 重生成
- [ ] selfupdate 公共包 + 测试
- [ ] CP service + 测试
- [ ] Worker gRPC ops + 测试
- [ ] CP 配置项
- [ ] HTTP 路由 + 测试 + main 接线
- [ ] 前端 api + 页面 + i18n
- [ ] doc-sync（API.md / ARCHITECTURE / ADR-020 / CHANGELOG）
