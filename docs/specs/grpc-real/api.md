# API Spec — FR-023 gRPC 客户端真实实现

> 关联 FR: FR-023 | 优先级: P0

## 概述

当前 `proto/workerpb/service.go` 中的 `WorkerServiceClient` 和 `RegisterWorkerServiceServer` 均为手写桩代码。本 FR 通过 protoc 生成真实的 gRPC 代码，替换桩实现，并补全 Worker Node 启动时的注册/心跳客户端逻辑和双端 gRPC Server 的启动入口。

---

## gRPC 服务接口

Proto 定义位于 `proto/worker.proto`，服务名为 `worker.WorkerService`。

### 生命周期 RPC

| RPC | 类型 | 发起方 | 说明 |
|---|---|---|---|
| `Register` | Unary | Worker → CP | Worker 首次启动时注册，返回 `node_uuid` + `node_secret` |
| `Heartbeat` | Bidirectional Stream | Worker ↔ CP | Worker 每 30s 上报 CPU/内存/磁盘指标，CP 回复时间戳 |

### 实例操作 RPC

| RPC | 类型 | 发起方 | 说明 |
|---|---|---|---|
| `CreateInstance` | Unary | CP → Worker | 在指定 Worker 上创建实例 |
| `StartInstance` | Unary | CP → Worker | 启动实例 |
| `StopInstance` | Unary | CP → Worker | 停止实例 |
| `RestartInstance` | Unary | CP → Worker | 重启实例 |
| `KillInstance` | Unary | CP → Worker | 强制终止实例 |
| `SendCommand` | Unary | CP → Worker | 向实例 stdin 发送命令 |
| `GetInstanceStatus` | Unary | CP → Worker | 查询实例状态 |
| `ListInstances` | Unary | CP → Worker | 列出所有实例 |
| `StreamInstanceEvents` | Server Stream | CP → Worker | 订阅实例事件流 |

### 文件操作 RPC

| RPC | 类型 | 发起方 | 说明 |
|---|---|---|---|
| `ListFiles` | Unary | CP → Worker | 列出实例工作目录文件 |
| `ReadFile` | Unary | CP → Worker | 读取文件内容 |
| `WriteFile` | Unary | CP → Worker | 写入文件内容 |
| `DeleteFile` | Unary | CP → Worker | 删除文件 |

### 其他 RPC

| RPC | 类型 | 发起方 | 说明 |
|---|---|---|---|
| `IssueTerminalToken` | Unary | CP → Worker | 签发终端 token（由 CP 处理） |
| `GetNodeMetrics` | Unary | CP → Worker | 获取节点系统指标 |
| `GetInstanceMetrics` | Unary | CP → Worker | 获取实例指标（TPS/玩家/内存） |

---

## 注册流程

```
Worker Node 启动
  │
  ├─ 读取 worker.yaml（name, control_plane addr）
  ├─ 连接 Control Plane gRPC 端口
  ├─ 发送 Register RPC {name, host, grpc_port, ws_port, os, arch, cpu_cores, memory_mb, disk_total_mb}
  │
  ├─ CP 处理:
  │   ├─ 按 name 查找 nodes 表
  │   │   ├─ 不存在 → INSERT，生成 uuid + secret
  │   │   └─ 已存在 → UPDATE 节点信息
  │   └─ 返回 {node_uuid, node_secret}
  │
  ├─ Worker 保存 node_uuid，启动 Heartbeat 流
  └─ 启动 gRPC Server（监听 grpc_port）
```

## 心跳流程

```
Worker Node（每 30s）
  │
  ├─ 采集系统指标（CPU/内存/磁盘）
  ├─ 发送 HeartbeatRequest {node_uuid, cpu_usage, memory_usage, disk_usage, ...}
  │
  ├─ CP 处理:
  │   ├─ 更新 nodes 表的指标字段 + last_heartbeat
  │   └─ 回复 HeartbeatResponse {timestamp}
  │
  └─ Worker 收到回复，确认连接存活
```

## 离线检测

```
Control Plane（每 30s 后台检查）
  │
  ├─ 查询 nodes 表中 status=online 的节点
  ├─ 若 last_heartbeat 超过 90s → 标记为 offline
  └─ 断开该节点的 gRPC 连接池
```

---

## 错误处理

| 场景 | gRPC Status | 说明 |
|---|---|---|
| Register 失败 | `Internal` | CP 数据库错误 |
| Heartbeat 连接断开 | `Unavailable` | Worker 自动重连 |
| 实例操作超时 | `DeadlineExceeded` | CP 设置 30s 超时 |
| 实例不存在 | `NotFound` | Worker 上找不到该实例 |
| 文件路径非法 | `InvalidArgument` | 路径遍历检测 |

---

## gRPC 端口规划

| 进程 | 端口 | 说明 |
|---|---|---|
| Control Plane gRPC Server | 9100 | 接收 Worker 的 Register/Heartbeat |
| Worker gRPC Server | 9101+ | 接收 CP 的实例/文件/指标操作 |

Worker 端口默认 9101，多实例递增。
