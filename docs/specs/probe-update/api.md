# FR-068 探针在线更新 — API Spec

> 探针在线更新：平台「点一下」把 CP 内嵌的最新 ServerProbe jar 推到实例 plugins 目录，
> 下次重启生效，可选「推送并重启」立即生效。复用已有 gRPC `DeployServerProbe`（FR-010/ADR-014），
> **不改 proto、不改子模块**。见 ADR-016。

## 设计要点

- **复用 `DeployServerProbe`**：CP 把内嵌 jar（`internal/controlplane/embed/probe/`，go:embed）+ 重新生成的
  探针 config.yml（含 `metrics:` + `bridge:` 段，与建服一致）经 `DeployServerProbe(jar, config_yaml)`
  下发到实例 plugins 目录，覆盖在位 jar。jar 是 JVM 已加载的 class 来源，**热替换不生效**，
  故语义为「已就位，下次重启生效」。
- **版本/连接展示（简化可行）**：内嵌最新版本（构建期常量 + jar 字节 SHA-256 短指纹）+ 探针连接状态
  （复用 FR-065/066 插件桥会话 `connected`）+ 上次推送时间（内存态，CP 进程内按实例 UUID 记录）。
  在位版本无可靠来源（探针 `/metrics` 不报版本、jar 在 Worker 侧），故不强求在位版本号；
  以「连接状态 + 内嵌版本 + 上次推送时间」覆盖验收的「在位/连接/版本状态在实例详情可见」。
- **权限**：`instance:operate`（单实例经 `CanAccessInstance` 收敛；批量同 FR-058 资源级隔离，越权/不存在计入 skipped）。
- **审计**：单实例 `probe.update`、批量 `probe.update.batch`（危险/批量操作留痕，FR-015）。
- **未部署/未连入优雅提示**：探针未连入时仍可推送（jar 落盘即生效于下次重启），响应里 `probeConnected=false`
  让前端提示「探针当前未连入，推送将于下次重启后生效」；jar 未内嵌（未跑 make embed-probe）时单实例返回 422、
  批量整体 422（无可推送内容）。

## Endpoints

### GET /api/v1/instances/:id/probe/status

返回某实例的探针更新状态（供详情页展示「更新探针」区）。

- **权限**: `instance:read`（`CanAccessInstance`）
- **关联 FR**: FR-068

响应 200：

```json
{
  "instanceId": 12,
  "instanceUuid": "uuid-xxx",
  "probeConnected": true,
  "embeddedVersion": "0.1.0",
  "embeddedFingerprint": "a1b2c3d4",
  "embeddedAvailable": true,
  "lastPushedAt": "2026-06-22T10:00:00Z"
}
```

- `embeddedAvailable`：CP 是否内嵌了探针 jar（false=未跑 make embed-probe，无法推送）。
- `lastPushedAt`：上次经本端点推送的时间（CP 进程内内存态，未推送过为 null）。
- `probeConnected`：探针是否经插件桥反向 WS 连入（FR-065/066 名册存在即视为在线）。

### POST /api/v1/instances/:id/probe/update

把内嵌最新探针 jar 推到该实例 plugins 目录（下次重启生效）。

- **权限**: `instance:operate`（`CanAccessInstance`）
- **审计**: `probe.update`
- **关联 FR**: FR-068

请求体（可选）：

```json
{ "restart": false }
```

- `restart=true`：推送成功后复用实例重启逻辑，使新版本立即生效（异步重启，与单实例 `restart` 语义一致）。

响应 200：

```json
{
  "instanceId": 12,
  "deployed": true,
  "restarted": false,
  "probeConnected": true,
  "embeddedVersion": "0.1.0",
  "embeddedFingerprint": "a1b2c3d4",
  "message": "探针 jar 已就位，下次重启生效"
}
```

错误：

- 404 `NOT_FOUND`：实例不存在或越权（存在性隐藏）。
- 422 `PROBE_NOT_EMBEDDED`：CP 未内嵌探针 jar（无法推送）。
- 422 `BUSINESS_ERROR`：节点未连接 / 下发失败（`message` 含具体原因）。

### POST /api/v1/instances/probe/update

批量把内嵌探针 jar 推到多个实例（按 ids/filter）。CP 侧信号量分片有界并发 gRPC 扇出，
镜像 FR-058 `instances/batch`。

- **权限**: `instance:operate`（资源级隔离：仅推有权实例，越权/不存在计入 skipped）
- **审计**: `probe.update.batch`
- **关联 FR**: FR-068

请求体（目标由 ids 或 filter 二选一）：

```json
{
  "ids": [1, 2, 3],
  "filter": { "nodeId": 1, "status": "RUNNING", "role": "backend" },
  "restart": false
}
```

响应 200：

```json
{
  "requested": 3,
  "succeeded": 2,
  "failed": 1,
  "skipped": 0,
  "errors": [{ "instanceId": 3, "error": "Worker xxx 未连接" }]
}
```

错误：

- 400 `INVALID_REQUEST`：未指定 ids/filter，或目标数超上限。
- 422 `PROBE_NOT_EMBEDDED`：CP 未内嵌探针 jar（无可推送内容，整体拒绝）。

## 与 ARCHITECTURE 一致性

- 复用既有 gRPC `DeployServerProbe`（Control Plane → Worker），不新增 proto/RPC。
- 探针 config 复用建服时的 `buildServerProbeConfig` + `bridgeConfigBlock`（含实例级 plugin-bridge token）。
- 批量并发模型与 FR-058 `InstanceBatchService` 一致（信号量有界并发 + 资源隔离 + 计数）。
