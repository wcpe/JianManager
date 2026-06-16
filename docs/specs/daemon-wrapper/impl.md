# 实施计划 — FR-006 守护进程（Daemon Wrapper）

> 关联 FR: FR-006 | 优先级: P0 | 关联 ADR: ADR-003 | 状态: 🔨 in-progress

## 背景

当前 `internal/worker/process/manager.go` 的 `Start()` 直接 `exec.Command` 起 Worker 直连子进程，无 `SysProcAttr` detach：Worker 挂则游戏服挂，违背 ADR-003「平台重启不杀游戏服」目标。`internal/worker/daemon/` 已有 `frame.go`（8 字节帧头编解码，已测）、`pid_file.go`（PID 文件 + `IsProcessAlive()`，未接入）作为死代码存在。

`model/instance.go` 有 `ProcessType`（direct/daemon/docker/rcon），`proto/worker.proto` 的 `CreateInstanceRequest.process_type` 字段已定义，但 `service/instance.go` 的 `registerOnWorker` 未传该字段，`manager` 也未按 `ProcessType` 路由。没有 `IProcessCommand` 策略接口。

**目标**: 按 ADR-003 真正实现 daemon wrapper — Worker spawn 独立 wrapper 子进程作 Java 进程父进程，进程组隔离；Worker 通过 Unix Socket（Linux/macOS）/ Named Pipe（Windows）+ 二进制帧协议与 wrapper 通信；PID 文件恢复；daemon 模式 Worker 优雅退出不杀游戏服。

## 范围

- 本批只做 daemon + direct（保留现有）+ docker stub（返回 `ErrNotImplemented`）。rcon 不在本批。
- 跨平台：Linux/macOS 用 Unix Socket，Windows 用 Named Pipe。当前平台 Windows 11，必须 `go build ./...` 通过。
- wrapper 复用 worker 二进制，通过 `daemon` 子命令模式启动。

## 任务拆解

### Phase 1: 策略接口与 direct 重构
- [x] 新建 `internal/worker/process/command.go`：定义 `IProcessCommand` 接口与 `CommandSpec` 配置、`ErrNotImplemented`。
- [x] 把现有 exec 逻辑提取为 `directStrategy` 实现 `IProcessCommand`。
- [x] `Manager` 改为持有 `map[string]IProcessCommand`，按 `ProcessType` 路由（direct/docker/daemon）。
- [x] `docker` 策略返回 `ErrNotImplemented`。
- [x] `manager_test.go` 回归全绿。

### Phase 2: daemon 策略与 wrapper
- [x] `cmd/worker/main.go` 增加 `daemon` 子命令分支，调用 `internal/worker/daemon/wrapper.go`。
- [x] 新建 `internal/worker/daemon/wrapper.go`：启动 Java 进程、指数退避崩溃重启、监听 socket/pipe、帧协议转发 stdio + 控制命令、写 PID 文件。
- [x] 新建 `internal/worker/process/daemon.go`：`daemonStrategy` spawn wrapper 子进程（`SysProcAttr` 脱离进程组），连接 socket/pipe，读写帧，输出桥接 `onOutput`。
- [x] 新建 `internal/worker/daemon/conn.go`：跨平台 socket/pipe 地址生成与监听/拨号（`runtime.GOOS` 分支）。

### Phase 3: PID 恢复与优雅退出
- [x] 扩展 `pid_file.go`：支持写入 wrapper pid + java pid + socket 地址（JSON 结构）。
- [x] `Manager` 启动时扫描 PID 文件，存活则 reconnect socket 恢复管理，否则清理。
- [x] daemon 模式 `StopAll` 只断开 wrapper 连接，不杀游戏服（区别于 direct）。

### Phase 4: 集成与文档
- [x] `service/instance.go` `registerOnWorker` 补传 `ProcessType`。
- [x] `go build ./...`（worker 侧）+ `go test ./internal/worker/...` 通过。
- [x] 同步 `docs/ARCHITECTURE.md` 守护进程章节。
- [x] PRD FR-006 验收勾选（仅真实做到的）。
- [x] 补 ADR-003 细化（跨平台 socket/pipe、wrapper 子命令、PID 文件 JSON 结构）。
- [x] 中文 commit，scope=worker，无 AI 署名。

## 测试（TDD 先行）
- daemon 帧协议 socket 层端到端（`conn_test.go`）。
- PID 文件恢复逻辑（`pid_file_test.go` 扩展）。
- 策略路由：direct/daemon/docker 分发（`command_test.go`）。
- direct 策略回归（现有 `manager_test.go` 不破）。
- 跨平台隔离：用 `runtime.GOOS` 分支 + 表驱动，真机验证由主控做。

## 验收标准（对齐 PRD FR-006）
- [x] 启动方式为 daemon 时，spawn 独立子进程管理游戏服
- [x] 二进制帧协议通信（Unix Socket / Named Pipe）
- [x] 平台重启后恢复守护进程连接（PID 文件 + reconnect）
- [x] 崩溃自动重启 + PID 文件恢复
- [ ] 真机验证「Worker 退出后游戏服存活」由主控执行（本批提供代码路径 + 单元测试）
