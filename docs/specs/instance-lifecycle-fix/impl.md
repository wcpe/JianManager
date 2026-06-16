# 修复计划 — 实例创建-启动-终端全链路

> 关联 FR: FR-005, FR-018, FR-019 | 优先级: P0 | 状态: ✅ done

## 任务拆解

### Phase 1: gRPC 连接链路修复

- [ ] **Fix 5**: `register.go` Config 加 `GrpcPort`，`collectSystemInfo` 填入 `RegisterRequest`，worker `main.go` 传参
- [ ] **Fix 1**: `handler.go` 加 `pool` 字段，`Register()` 中调 `pool.Connect()`，新节点生成 `Secret`。CP `main.go` 传 pool

### Phase 2: 进程管理器 I/O 修复

- [ ] **Fix 3**: `manager.go` Instance 加 `Stdin io.WriteCloser` 字段，`Start()` 前创建管道存到实例，`SendCommand` 用存储管道
- [ ] **Fix 4**: `manager.go` Manager 加 `onOutput` 回调 + `instanceWriter` 适配器，替换 `os.Stdout`/`os.Stderr`。Worker `main.go` 桥接 TerminalServer

### Phase 3: 实例创建 Worker 注册

- [ ] **Fix 2**: `instance.go` 新增 `registerOnWorker()` 方法，Create 事务成功后调 `Worker.CreateInstance`

### Phase 4: 验证

- [ ] `go build ./...` 编译通过
- [ ] `go test ./internal/...` 全部通过
- [ ] 手动验证: CP + Worker 启动 → 创建实例 → 启动 → 终端连接 → 发送命令 → 查看输出

## 关键文件

| 文件 | 变更内容 |
|---|---|
| `internal/worker/register/register.go` | Config 加 GrpcPort |
| `internal/controlplane/grpc/handler.go` | 加 pool 注入 + Register 连接 |
| `internal/controlplane/service/instance.go` | Create 后 registerOnWorker |
| `internal/worker/process/manager.go` | stdin 管道 + stdout 回调 |
| `cmd/control-plane/main.go` | 传 pool 给 handler |
| `cmd/worker/main.go` | 传 grpcPort + 桥接 terminal |

## 依赖

- FR-005（实例生命周期管理）— 已有代码有缺陷
- FR-018（实例 gRPC 生命周期操作）— gRPC 链路断裂
- FR-019（终端 WebSocket 全链路）— 输出未接入终端

## 风险

- 修复 2 引入了 Create 时的 gRPC 调用，Worker 离线时实例仍创建成功（降级为 warning），用户可重试
- 现有测试不受影响：测试环境 pool 无连接，registerOnWorker 记 warning 不阻断
