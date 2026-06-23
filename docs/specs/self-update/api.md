# API Spec — FR-081 面板自更新（CP/Worker 二进制在线升级）

> 关联 FR: FR-081 | 关联 ADR: ADR-020（§4 自更新来源/校验/编排立场）
> 权威定义见 `docs/API.md`（HTTP）与 `proto/worker.proto`（gRPC）；本文件为 feature 视角汇总。

## 决策摘要

- **可配更新源 + sha256 校验，CP 统一编排**（用户已定）。
- 更新源：release feed（JSON manifest，含各平台二进制 url + sha256 + 版本号）或私有基址 URL。
- CP 自更新：下载新二进制 → sha256 校验 → 原子替换自身二进制 → 平滑重启（保留运行态：daemon 游戏服不受影响；CP 重启后心跳重建反向连接）。
- Worker 升级：经 CP gRPC 编排（CP 下发目标版本/url/sha256 → Worker 下载校验替换 → 计划重启）。daemon 模式下不杀游戏服（ADR-003 wrapper 子进程与 Worker 主进程隔离，Worker 重启后 `RecoverDaemonInstances` 重连存活 wrapper）。
- 仅平台管理员 + 升级审计（FR-015）。

## 更新源配置（control-plane.yaml）

```yaml
update:
  feed_url: ""            # release feed JSON 地址；非空时「检查更新」据此解析最新版本与各平台制品
  binary_base_url: ""     # 私有二进制基址（无 feed 时按 <base>/<component>-<os>-<arch> 约定拼下载地址）
  allow_insecure: false   # 是否允许 http（默认仅 https，本地/内网自测可开）
```

- 全部可选、有合理默认（留空表示「未配置更新源」，检查更新返回未配置提示而非报错）。
- 敏感信息（如私有源 token）经 `${ENV_VAR}` 引用，不硬编码（config-files 规范）。

### Release feed JSON 契约

```json
{
  "version": "0.8.0",
  "notes": "可选更新说明",
  "artifacts": [
    { "component": "control-plane", "os": "linux",   "arch": "amd64", "url": "https://.../control-plane-linux-amd64",   "sha256": "abc..." },
    { "component": "worker",        "os": "windows", "arch": "amd64", "url": "https://.../worker-windows-amd64.exe",   "sha256": "def..." }
  ]
}
```

- `component`：`control-plane` | `worker`。`os`/`arch`：`runtime.GOOS`/`runtime.GOARCH` 取值。
- 选制品：按目标组件 + 目标节点（或 CP 自身）的 os/arch 精确匹配。

## HTTP（浏览器 ↔ Control Plane，均仅平台管理员）

### 检查更新

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/self-update/check` | 返回 CP 当前版本 + feed 最新版本 + 各节点当前版本 + 是否有更新 |

响应（`200`）：

```json
{
  "configured": true,
  "latestVersion": "0.8.0",
  "notes": "...",
  "controlPlane": {
    "currentVersion": "0.7.0",
    "os": "linux", "arch": "amd64",
    "updateAvailable": true,
    "artifactAvailable": true
  },
  "nodes": [
    {
      "nodeId": 3, "nodeUuid": "uuid", "name": "node-01",
      "online": true, "currentVersion": "0.7.0",
      "os": "linux", "arch": "amd64",
      "updateAvailable": true, "artifactAvailable": true
    }
  ]
}
```

- `configured=false`（未配 feed）时 `latestVersion` 空、`updateAvailable` 恒 false，前端提示先配置更新源。
- 节点当前版本经 gRPC `GetVersion` 实时拉取；离线节点 `online=false`、`currentVersion` 取最近一次已知值或空。

### 升级 Control Plane 自身

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/self-update/control-plane/upgrade` | 下载目标版本 CP 二进制 → sha256 校验 → 替换 → 平滑重启 |

请求体（可选）：

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `version` | string | feed 最新 | 目标版本；留空取 feed 最新版本 |

响应（`202 Accepted`，替换成功、重启已计划）：

```json
{ "status": "restarting", "fromVersion": "0.7.0", "toVersion": "0.8.0" }
```

- 流程：解析 feed → 选 CP 自身平台制品 → 下载到 `cache/` → sha256 校验（不符 `422`）→ 原子替换当前可执行文件（同目录临时文件 + rename；Windows 先把旧文件改名再落新文件）→ 返回 202 → 延迟数百毫秒后 `自我重启`（os.StartProcess 拉起新二进制后退出，或退出交由系统服务/supervisor 拉起）。
- 错误码：`409 UPDATE_NOT_CONFIGURED`（未配源）、`422 UPDATE_CHECKSUM_MISMATCH`、`422 UPDATE_NO_ARTIFACT`（feed 无本平台制品）、`502 UPDATE_DOWNLOAD_FAILED`、`409 UPDATE_ALREADY_LATEST`（已是最新且未强制）、`500 INTERNAL_ERROR`。
- 审计：`self_update.control_plane`（detail：fromVersion/toVersion，绝不含 url 凭据）。

### 升级单个 Worker 节点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/self-update/nodes/:id/upgrade` | 经 gRPC 令目标节点下载校验替换重启 |

请求体（可选）：`{ "version": "0.8.0" }`（留空取 feed 最新）。

响应（`202`）：

```json
{ "status": "upgrading", "nodeId": 3, "fromVersion": "0.7.0", "toVersion": "0.8.0" }
```

- 错误码：`409 UPDATE_NOT_CONFIGURED`、`503 NODE_OFFLINE`、`422 UPDATE_NO_ARTIFACT`、`422 UPDATE_CHECKSUM_MISMATCH`（Worker 校验失败回报）、`502 UPDATE_DOWNLOAD_FAILED`、`409 UPDATE_ALREADY_LATEST`。
- 审计：`self_update.node`（detail：nodeId/fromVersion/toVersion）。

### 全网逐节点升级编排（进度 + 失败重试）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/self-update/nodes/upgrade-all` | 对所有在线节点逐个发起升级，异步执行，返回任务 id |
| GET | `/api/v1/self-update/rollout` | 查询当前/最近一次全网升级编排进度（逐节点状态） |

`upgrade-all` 请求体（可选）：`{ "version": "0.8.0", "nodeIds": [3,4] }`（`nodeIds` 留空=全部在线节点）。

`rollout` 响应（`200`）：

```json
{
  "rolloutId": "uuid",
  "targetVersion": "0.8.0",
  "state": "running",            // idle | running | completed
  "startedAt": "...", "finishedAt": null,
  "total": 5, "succeeded": 3, "failed": 1, "pending": 1,
  "nodes": [
    { "nodeId": 3, "name": "node-01", "state": "succeeded", "fromVersion": "0.7.0", "toVersion": "0.8.0", "error": "" },
    { "nodeId": 4, "name": "node-02", "state": "failed", "error": "checksum mismatch", "attempts": 1 }
  ]
}
```

- 逐节点串行（避免一次性把全网打满）；单节点失败不阻断后续，记 `failed` + error，可对失败节点重试（再次对该节点调 `/nodes/:id/upgrade` 或 `upgrade-all` 仅传其 nodeIds）。
- 节点状态：`pending | upgrading | succeeded | failed`。
- 审计：`self_update.rollout`（detail：targetVersion/total）。

## gRPC（Control Plane ↔ Worker Node）—— 改 proto，protoc 重新生成

> 改 `proto/worker.proto` 新增两个 RPC + 对应 message，protoc 重新生成 `proto/workerpb`（禁 sed）。

```proto
service WorkerService {
  // ... 既有 RPC ...

  // GetVersion 返回 Worker 当前版本与平台信息（自更新检查用，FR-081）。
  rpc GetVersion(GetVersionRequest) returns (GetVersionResponse);
  // UpgradeWorker 令 Worker 下载指定二进制 → sha256 校验 → 替换自身 → 计划重启（FR-081）。
  // daemon 模式下不杀游戏服（ADR-003 wrapper 隔离；重启后 RecoverDaemonInstances 重连）。
  rpc UpgradeWorker(UpgradeWorkerRequest) returns (UpgradeWorkerResponse);
}

message GetVersionRequest {}

message GetVersionResponse {
  string version = 1; // internal/version.Version
  string os = 2;      // runtime.GOOS
  string arch = 3;    // runtime.GOARCH
}

message UpgradeWorkerRequest {
  string download_url = 1; // 目标 Worker 二进制下载地址（CP 据 feed + 节点平台解析后下发）
  string sha256 = 2;       // 期望 sha256（Worker 下载后校验，不符拒绝替换）
  string target_version = 3; // 目标版本号（仅记录/回报）
}

message UpgradeWorkerResponse {
  bool success = 1;       // 替换成功、重启已计划
  string error = 2;       // 失败原因（下载失败/校验不符/替换失败）
  string from_version = 3; // 升级前版本
}
```

- **Worker 升级流程**：收 `UpgradeWorker` → 下载到 `cache/` → sha256 校验（不符回 `success=false, error="checksum mismatch"`，不替换）→ 原子替换自身可执行文件 → 回 `success=true` → 短延迟后退出/重启（由系统服务或安装脚本注册的 supervisor 拉起；裸跑则自 re-exec）。回应**先于重启**返回，确保 CP 收到结果。
- **不杀游戏服**：替换与重启只动 Worker 主进程，daemon wrapper 子进程（持有 Java 游戏服）独立存活；Worker 重启后 `RecoverDaemonInstances` 经 PID 文件重连。

## 不做（范围边界）

- 不做自动定时检查/自动升级（仅手动触发；自动化留后续 FR）。
- 不做二进制签名验证（sha256 完整性校验即本 FR 范围；签名分发是客户端 OTA FR-087 的范畴，两线隔离）。
- 不做灰度/金丝雀百分比放量（逐节点串行 + 失败隔离即可）。
- 公网 release feed 端点本 FR 不架设（同 FR-080，先支持可配 feed_url/binary_base_url + 本地/内网源自测）。
