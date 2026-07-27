# Spec: CP→Worker 指令反向流化——gRPC 反向隧道 + 终端中转（FR-281）

- **状态**: 🔨 开发中（**M1+M2 代码全落、自动化测试全绿；剩 NAT 真机验收**，见 §4）
- **关联**: ADR-081（仅反向隧道与节点认证，取代 ADR-066 的双模式决定）、ADR-039（节点 UUID 身份）、ADR-061（WS 令牌密钥）、ADR-050（重连 resync）、FR-278（内嵌 Worker 版本一致）
- **用户拍板**: 仅允许 Worker 主动建立的反向隧道；删除所有 CP→Worker 直拨路径。
- **里程碑**: M1 反向指令隧道（✅）→ M2 终端中转（✅）→ 仅隧道与节点身份收紧（开发中）
- **落地 commit**: M1 `80b9712`（CP 隧道注册+池两级）/ `9e98a89`（Worker 隧道 Runner）/ `29c6867`（隧道状态观测）；M2 `e98e882`（TerminalSession 回环桥）/ `23c329c`（CP 代理 gRPC 优先桥）

## 1. 目标与非目标

**目标**：公网 CP + NAT/内网 worker 场景下，节点全功能可用（指令/文件/部署/终端），worker 零入站端口要求（仅出站可达 CP）。

**非目标**：不改 Worker↔Daemon Unix Socket、Bot IPC、探针 plugin-bridge（均本机）；不做 CP 高可用下的隧道漂移。

## 2. M1 反向指令隧道 + 双模式

### 2.1 隧道建立（Worker 侧）

- 新增 `internal/worker/tunnel/`：以 `github.com/jhump/grpctunnel` 的 `ReverseTunnelServer`，把 worker 既有 `WorkerService` 实现挂到隧道上，经 Worker→CP 既有 gRPC 连接（与心跳同址 `ControlPlaneAddr`）开常驻反向隧道。
- 生命周期与心跳对齐：注册成功后建隧道；断连指数退避重连（复用心跳退避参数）；进程退出优雅关闭。
- 鉴权：隧道建立请求携带与心跳同源的节点身份元数据（UUID 锚定，ADR-039）；CP 校验失败即拒绝隧道。

### 2.2 隧道登记与仅隧道取连接（CP 侧）

- CP gRPC server 注册 `TunnelService` handler（库自带 `tunnel.proto`，不入本仓 proto），affinity key = 节点 UUID。
- `internal/controlplane/grpc/pool.go` 只在该节点存在活跃反向隧道时返回隧道 `ClientConnInterface`；无隧道即节点不可调用，不得拨号 `node.Host:GRPCPort`。
- 调用点零改动：pool 返回的接口签名不变，~70 个既有 RPC（含 `StreamInstanceEvents` / `DownloadArchive` 等流式）自动获得隧道承载。
- 观测面：节点心跳/详情暴露 `tunnelConnected: bool`（前端节点页显示「隧道已连 / 不可用」）。

### 2.3 边界语义

- **超时/取消**：gRPC deadline / context 取消经隧道原生传播，不自造。
- **背压**：库内置每虚拟流窗口控制；大流量（文件下载/反编译）经隧道的吞吐须以 ≥100MB 归档下载真机验收确认。
- **单节点单隧道**：同 UUID 重复建隧道，后建替换先建（与重注册语义一致，ADR-039）；替换瞬间在途 RPC 允许失败（上层既有重试/报错面兜底）。
- **msgsize**：隧道两端沿用既有 MaxRecvMsgSize 放宽值（64MB，探针部署已踩过 4MB 默认坑）。

## 3. M2 终端中转（实现修订：比草案更小——CP 中转本就存在）

> 实现时发现 **CP 已全量中转终端**（`TerminalProxy` 挂 `/ws/terminal`，浏览器 wsUrl 本就指向 CP；
> FR-276 诊断即在此层）。唯一 NAT 断点是 CP→Worker 段的 `websocket.Dial(worker:9102)`。
> 故 M2 收敛为「只换这一跳」，前端与浏览器侧端点零改动。以下为落地设计（取代草案 §3.1~3.3）。

### 3.1 proto（已落）

```proto
rpc TerminalSession(stream TerminalFrame) returns (stream TerminalFrame);

message TerminalFrame { oneof kind { TerminalOpen open = 1; TerminalWSFrame frame = 2; } }
message TerminalOpen { string token = 1; }        // CP→Worker 首帧携带一次性令牌；Worker→CP 空 open = 就绪 ack
message TerminalWSFrame { int32 msg_type = 1; bytes payload = 2; }  // 不透明 WS 帧原样搬运
```

- **不透明帧搬运**（取代草案的 stdin/resize 结构化帧）：对既有终端 WS 协议零侵入，resize/stdin/stdout/state 全部无感透传。
- **worker 侧 = 本机回环桥**：`TerminalSession` 校验首帧后回环拨 `ws://127.0.0.1:<wsPort>/ws/terminal?token=…`——令牌校验与会话层的**单一真源仍是 `ws.TerminalServer`**（零复制）；本机 401/403 以 `PermissionDenied` 透传。
- **就绪 ack**：回环拨通即回空 open，CP 据此确定性区分就绪、令牌被拒（FR-276 诊断）和隧道不可用；`Unimplemented` 不回退直拨 WS。

### 3.2 CP 中转（已落，无新增 HTTP 端点）

- 复用既有 `/ws/terminal`（`TerminalProxy`）：`bridgeViaGRPC` 通过隧道取 `WorkerServiceClient` 开 `TerminalSession` 流双向泵帧；无隧道或 `Unimplemented` 明确报告节点不可用，不回退 Worker WS。
- 一次性令牌全流程保留（CP 消费 used-set + Worker 校验签名）；FR-276 `WORKER_TOKEN_REJECTED` 定向诊断在 gRPC 桥路同语义成立。

### 3.3 前端切线（零改动）

- 前端 `wsUrl` 本就指向 CP `/ws/terminal`，无需切线。已核查 `web/src` 无任何直连 worker WS 的用途（`wsPort` 仅作展示字段）；plugin-bridge（本机探针）保留在 9102。

### 3.4 文档与不变量同步（doc-sync）

- `.claude/rules/architecture-invariants.md`：「CP↔Worker gRPC」仅允许反向隧道（ADR-081）；浏览器不直连 Worker，终端经 CP 中转；Worker WS 仅本机回环终端桥与本机探针 plugin-bridge。
- `docs/ARCHITECTURE.md` 通信协议图与端口表；`docs/API.md` 新增 relay 端点；install-worker/防火墙指引改「worker 仅需出站可达 CP」。

## 4. 测试与验收

### 自动化（✅ 全绿）
- [x] 隧道集成（bufconn 真隧道）：建立→登记→pool 取隧道→RPC 达→断开→登记消失；鉴权三拒绝（secret 错/节点不存在/缺身份）；无隧道不直拨（`internal/controlplane/grpc/tunnel_test.go`）
- [x] Worker Runner：建立→RPC 经隧道达→CP 侧踢断→退避自动重连→Stop 后不再重连（`internal/worker/tunnel/tunnel_test.go`）
- [x] `TerminalSession`：就绪 ack + echo 往返 / 令牌拒绝 `PermissionDenied` / 缺 open `InvalidArgument` / 未装配 `Unavailable`（`internal/worker/grpc/terminal_session_test.go`）
- [x] CP 终端桥全真链路（真 `workerws.TerminalServer`）：直拨黑洞下 welcome 经 gRPC 桥到达（证明桥路生效）/ 老 Worker `Unimplemented` 不直拨 / 密钥不一致 `WORKER_TOKEN_REJECTED` 诊断（`internal/controlplane/service/terminal_proxy_grpc_test.go`）。

### 真机（必验，测试绿 ≠ 真能用）
- **NAT 场景主验**：公网 CP（FR-277 部署主机可用）+ 家用 NAT Windows worker（9101/9102 不放行）：节点上线 → 指令（启停/文件/部署探针）→ **浏览器终端全链路** → ≥100MB 归档下载。
- **升级场景**：老版本 Worker 不具备反向隧道时应明确不可用；升级到支持反向隧道和节点身份的版本后恢复服务。

## 5. 交付物触点

| 类别 | 触点 |
|---|---|
| proto | `proto/worker.proto` + `make proto` 再生成 |
| Go 依赖 | `github.com/jhump/grpctunnel`（go.mod；gen-licenses 自动纳管） |
| Worker | `internal/worker/tunnel/`（新）、终端会话层桥接、main 装配 |
| CP | gRPC server 注册 TunnelService、`pool.go` 仅隧道取连接、relay WS 端点（router）、节点观测字段 |
| 前端 | 终端接线切 relay、节点隧道状态徽标 |
| 文档 | architecture-invariants / ARCHITECTURE / API / OPERATIONS（防火墙指引）/ CHANGELOG |
