# 实施计划 — FR-023 gRPC 客户端真实实现

> 关联 FR: FR-023 | 优先级: P0 | 状态: 🔨 in-progress

## 背景

当前 `proto/workerpb/service.go` 是手写的桩代码，`RegisterWorkerServiceServer()` 为空函数，`NewWorkerServiceClient()` 返回的客户端全部方法都返回 `fmt.Errorf("桩代码")`。Control Plane 的连接池和 Worker 的 gRPC Server Handler 虽然已有完整业务逻辑，但因桩代码阻塞无法真正运行。

**目标**: 替换桩代码为 protoc 生成的真实 gRPC 实现，补全 Worker 注册/心跳客户端，双端启动 gRPC Server。

---

## 任务拆解

### Phase 1: Protoc 代码生成

- [ ] 安装 protoc + protoc-gen-go + protoc-gen-go-grpc
- [ ] 创建 `scripts/proto-gen.sh` 脚本，从 `proto/worker.proto` 生成 Go 代码到 `proto/workerpb/`
- [ ] 生成文件：`worker.pb.go`（消息类型）+ `worker_grpc.pb.go`（gRPC 服务）
- [ ] 删除手写的 `proto/workerpb/types.go` 和 `proto/workerpb/service.go`
- [ ] 确保 `go build ./...` 通过（适配生成代码的类型签名）

### Phase 2: Worker 注册/心跳客户端

- [ ] 新建 `internal/worker/register.go`
  - `RegisterToControlPlane(cfg config.Config) (nodeUUID string, conn *grpc.ClientConn, err error)`
  - 连接 CP gRPC 端口，发送 Register RPC
  - 保存 node_uuid 到 Worker 全局状态
- [ ] 新建 `internal/worker/heartbeat.go`
  - `StartHeartbeat(ctx context.Context, client workerpb.WorkerServiceClient, nodeUUID string, interval time.Duration)`
  - 建立双向 Heartbeat 流
  - 每 `interval` 秒采集系统指标并发送
  - 连接断开时自动重连（指数退避）

### Phase 3: 双端 gRPC Server 启动

- [ ] 修改 `cmd/control-plane/main.go`
  - 创建 gRPC Server（`grpc.NewServer()`）
  - 注册 `ControlPlaneHandler` 到 gRPC Server（调用生成的 `RegisterWorkerServiceServer()`）
  - 启动 gRPC Listener（端口从 config 读取，默认 9100）
  - 启动离线检测 goroutine
- [ ] 修改 `cmd/worker/main.go`
  - 启动时调用 `RegisterToControlPlane()` 获取 nodeUUID
  - 启动 `StartHeartbeat()` 后台 goroutine
  - 创建 Worker gRPC Server
  - 注册 `workergrpc.Server` 到 gRPC Server
  - 启动 gRPC Listener（端口从 config 读取，默认 9101）

### Phase 4: 适配与验证

- [ ] 适配现有代码中使用手写桩类型的引用（pool.go, handler.go, instance service 等）
- [ ] 确保 `go build ./...` 通过
- [ ] 确保 `go vet ./...` 无警告

---

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `proto/worker.proto` | 不变 | 已有完整定义 |
| `proto/workerpb/types.go` | 删除 | 被 protoc 生成代码替代 |
| `proto/workerpb/service.go` | 删除 | 被 protoc 生成代码替代 |
| `proto/workerpb/worker.pb.go` | 新增 | protoc 生成 |
| `proto/workerpb/worker_grpc.pb.go` | 新增 | protoc-gen-go-grpc 生成 |
| `scripts/proto-gen.sh` | 新增 | protoc 生成脚本 |
| `internal/worker/register.go` | 新增 | Worker 注册客户端 |
| `internal/worker/heartbeat.go` | 新增 | Worker 心跳客户端 |
| `cmd/control-plane/main.go` | 修改 | 启动 gRPC Server |
| `cmd/worker/main.go` | 修改 | 启动注册/心跳 + gRPC Server |
| `internal/controlplane/grpc/pool.go` | 修改 | 适配生成代码的类型签名 |
| `internal/controlplane/grpc/handler.go` | 修改 | 适配生成代码的类型签名 |
| `internal/worker/grpc/server.go` | 修改 | 适配生成代码的类型签名 |
| `internal/worker/grpc/file_ops.go` | 修改 | 适配生成代码的类型签名 |
| `internal/controlplane/service/instance.go` | 修改 | 适配 gRPC Client 调用签名 |

---

## 依赖

- FR-004（节点注册与心跳）— 已完成，数据库模型已有
- FR-029（Worker 注册心跳集成）— in-progress，本 FR 为其提供底层 gRPC 实现
- protoc + protoc-gen-go + protoc-gen-go-grpc 工具链

---

## 风险

| 风险 | 应对方案 |
|---|---|
| protoc 生成代码与现有手写代码签名不一致 | Phase 4 专门做适配，逐个修复编译错误 |
| 流式 RPC（Heartbeat）实现复杂 | 参考 gRPC 官方 bidirectional streaming 示例 |
| Worker 重连逻辑导致连接风暴 | 指数退避 + 最大重试间隔 60s |
