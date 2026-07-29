# FR-406 / FR-407 / FR-408 API 契约

## 1. HTTP：进程详情

### GET /api/v1/instances/:id/processes/:pid

- 关联 FR：FR-407
- 鉴权：JWT；必须可读取该实例（复用实例可见性规则）
- 路径参数：
  - `id`：实例 ID
  - `pid`：目标进程 PID，正整数
- 失败：
  - `400 INVALID_PID`：PID 非法
  - `403 FORBIDDEN`：无权访问该实例
  - `404 NOT_FOUND`：实例不存在或按权限隐藏
  - `409 INSTANCE_NOT_RUNNING`：实例未运行
  - `409 PID_NOT_MANAGED`：PID 不属于该实例当前受管进程树
  - `503 NODE_OFFLINE`：节点离线或 Worker 连接不可用

响应：

```json
{
  "instance": { "id": 2, "uuid": "inst-uuid", "name": "lobby", "nodeId": 1, "nodeUuid": "node-uuid", "nodeName": "node-a" },
  "rootPid": 1200,
  "target": {
    "pid": 1234,
    "parentPid": 1200,
    "name": "java",
    "isRoot": false,
    "cpuPercent": 83.5,
    "rssBytes": 1073741824,
    "readBytesPerSec": 1024,
    "writeBytesPerSec": 2048,
    "user": "minecraft",
    "commandSummary": "java -Xmx4G -jar server.jar ...",
    "uptimeSeconds": 3600,
    "threadCount": 48,
    "sampledAt": "2026-07-28T12:00:00Z",
    "unavailableReason": ""
  },
  "ancestors": [{ "pid": 1200, "parentPid": 0, "name": "wrapper", "isRoot": true }],
  "children": [{ "pid": 1250, "parentPid": 1234, "name": "helper", "isRoot": false }],
  "diagnostics": [
    {
      "code": "cpu_sustained_high",
      "severity": "warning",
      "title": "CPU 持续高占用",
      "evidence": "最近 5 个样本平均 CPU 86.2%，窗口 30 分钟",
      "suggestion": "优先查看插件任务、循环脚本或异常线程；必要时先温和终止子进程。"
    }
  ],
  "history": {
    "windowSeconds": 1800,
    "sampleCount": 12,
    "latestSampledAt": "2026-07-28T12:00:00Z",
    "rssDeltaBytes": 268435456,
    "avgCpuPercent": 86.2,
    "avgWriteBytesPerSec": 1048576
  }
}
```

说明：

- `commandSummary` 必须截断和脱敏；响应不得包含完整命令行、环境变量或密钥。
- `ancestors` 与 `children` 只包含同一受管实例树内进程。
- `diagnostics` 仅基于已有采样，不做 JVM attach 或 heap dump。

## 2. HTTP：PID 级处置

### POST /api/v1/instances/:id/processes/:pid/actions

- 关联 FR：FR-408
- 鉴权：JWT；必须可操作该实例
- 请求：

```json
{ "action": "terminate", "confirm": true }
```

`action` 可选：

| action | 语义 |
|---|---|
| `terminate` | 温和终止目标非根进程 |
| `kill_tree` | 强制终止目标非根进程及其子进程树 |

失败：

- `400 INVALID_REQUEST`：请求体非法或 action 非法
- `409 CONFIRM_REQUIRED`：缺少 `confirm=true`
- `409 ROOT_PROCESS_ACTION_DENIED`：目标是实例根进程，必须走实例 stop/restart/kill
- `409 PID_NOT_MANAGED`：PID 不属于实例当前进程树
- `409 INSTANCE_NOT_RUNNING`：实例未运行
- `422 UNSUPPORTED`：当前平台无法安全执行该 action
- `503 NODE_OFFLINE`：节点离线或 Worker 连接不可用

响应：

```json
{
  "success": true,
  "action": "kill_tree",
  "pid": 1234,
  "affectedPids": [1234, 1250],
  "message": "已终止受管子进程树"
}
```

审计要求：

- action：`process.terminate` 或 `process.kill_tree`
- detail：`instanceId`、`nodeId`、`pid`、`action`、`affectedPids`、`success`、`errorCode`
- 禁止记录完整命令行、环境变量、令牌或密钥。

## 3. gRPC：Worker 受管进程探查

### rpc InspectManagedProcess(ManagedProcessInspectRequest) returns (ManagedProcessInspectResponse)

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `instance_uuid` | string | 目标实例 UUID |
| `pid` | int32 | 目标 PID |

响应字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `success` | bool | 是否成功 |
| `error` | string | 失败原因，中文可读 |
| `code` | string | 稳定错误码 |
| `root_pid` | int64 | 实例当前根 PID |
| `target` | ManagedProcessInfo | 目标进程 |
| `ancestors` | repeated ManagedProcessInfo | 树内祖先进程 |
| `children` | repeated ManagedProcessInfo | 树内直接/后代子进程，按树遍历顺序 |
| `sampled_at_unix_ms` | int64 | Worker 采样时刻 |
| `unavailable_reason` | string | 局部指标不可用原因 |

## 4. gRPC：Worker 受管进程处置

### rpc TerminateManagedProcess(ManagedProcessActionRequest) returns (ManagedProcessActionResponse)

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `instance_uuid` | string | 目标实例 UUID |
| `pid` | int32 | 目标 PID |
| `mode` | string | `terminate` 或 `kill_tree` |

响应字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `success` | bool | 是否成功 |
| `error` | string | 失败原因，中文可读 |
| `code` | string | 稳定错误码 |
| `pid` | int32 | 目标 PID |
| `mode` | string | 实际执行模式 |
| `affected_pids` | repeated int32 | 实际影响的树内 PID |

Worker 必须在每次 RPC 内重新读取实例状态、根 PID 与当前进程树；不得信任 CP 或前端传来的历史采样。
