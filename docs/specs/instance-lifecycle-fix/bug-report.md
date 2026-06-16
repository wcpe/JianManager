# Bug 报告 — 实例创建-启动-终端全链路断裂

> 关联 FR: FR-005, FR-018, FR-019 | 优先级: P0 | 状态: ✅ done

## 现象

用户在前端创建 minecraft_java 实例后点击「启动」，实例状态变为 CRASHED，无任何错误提示。终端页面无法显示进程输出，命令输入不生效。

## 根因分析

Control Plane ↔ Worker Node 之间的 gRPC 链路存在 5 个断点，导致实例生命周期操作全部失败。

### Bug 1: Control Plane 从不连接 Worker（FR-018）

- **文件**: `internal/controlplane/grpc/handler.go`
- **现象**: Worker 注册后，Control Plane 不建立反向 gRPC 连接
- **根因**: `ControlPlaneHandler.Register()` 只写 DB 记录，未调用 `pool.Connect(nodeUUID, addr)`。`pool` 未注入到 handler
- **影响**: `delegateToWorker` 中 `pool.Get()` 永远返回 false，所有启停操作失败
- **附带**: 新节点未生成 `Secret`（NOT NULL 约束报错）

### Bug 2: 实例创建未同步到 Worker（FR-018）

- **文件**: `internal/controlplane/service/instance.go`
- **现象**: Worker 进程管理器里没有实例记录
- **根因**: `InstanceService.Create()` 只写 DB，未调用 `Worker.CreateInstance` gRPC
- **影响**: `StartInstance` 时 Worker 找不到实例，返回 "instance not found"

### Bug 3: stdin 管道生命周期错误（FR-019）

- **文件**: `internal/worker/process/manager.go:269`
- **现象**: `SendCommand` 调用失败
- **根因**: `StdinPipe()` 在 `Start()` 之后调用，但 `exec.Cmd.StdinPipe()` 必须在 `Start()` 前且只能调一次
- **影响**: 终端输入命令无法发送到进程

### Bug 4: 进程输出未接入终端（FR-019）

- **文件**: `internal/worker/process/manager.go:122-123`
- **现象**: WebSocket 终端不显示任何进程输出
- **根因**: `cmd.Stdout = os.Stdout` / `cmd.Stderr = os.Stderr` 写到 Worker 自己的 stdout，未桥接 `TerminalServer.Broadcast`
- **影响**: 终端页面空白，无法看到服务器输出

### Bug 5: 注册请求未携带 gRPC 端口（FR-018）

- **文件**: `internal/worker/register/register.go`
- **现象**: Control Plane 存储的 Worker gRPC 端口为 0
- **根因**: `collectSystemInfo()` 未设置 `RegisterRequest.GrpcPort`
- **影响**: 即使 Bug 1 修复，连接地址也是 `host:0`（无效）

## 依赖关系

```
Bug 5 (gRPC 端口) ──→ Bug 1 (pool 连接) ──→ Bug 2 (CreateInstance)
                                              ↑
Bug 3 (stdin 管道) ──→ Bug 4 (stdout/终端) ──┘
```
