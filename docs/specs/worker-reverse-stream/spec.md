# Spec: CP→Worker 指令反向流化——gRPC 反向隧道 + 终端中转（FR-281）

- **状态**: 🔨 开发中
- **关联**: ADR-066（accepted，修订 ADR-002 方向性 + 浏览器↔Worker WS 不变量）、ADR-039（节点 UUID 身份）、ADR-061（WS 令牌密钥）、ADR-050（重连 resync）、FR-278（内嵌 Worker 版本一致）
- **用户拍板**: 双模式（隧道优先+直拨回退）；本批完整做（指令隧道 + 终端中转）
- **里程碑**: M1 反向指令隧道 + 双模式池 → M2 终端（及浏览器↔Worker WS 其余用途）中转化

## 1. 目标与非目标

**目标**：公网 CP + NAT/内网 worker 场景下，节点全功能可用（指令/文件/部署/终端），worker 零入站端口要求（仅出站可达 CP）。

**非目标**：不移除直拨路径（后续版本另评估）；不改 Worker↔Daemon Unix Socket、Bot IPC、探针 plugin-bridge（均本机）；不做 CP 高可用下的隧道漂移。

## 2. M1 反向指令隧道 + 双模式

### 2.1 隧道建立（Worker 侧）

- 新增 `internal/worker/tunnel/`：以 `github.com/jhump/grpctunnel` 的 `ReverseTunnelServer`，把 worker 既有 `WorkerService` 实现挂到隧道上，经 Worker→CP 既有 gRPC 连接（与心跳同址 `ControlPlaneAddr`）开常驻反向隧道。
- 生命周期与心跳对齐：注册成功后建隧道；断连指数退避重连（复用心跳退避参数）；进程退出优雅关闭。
- 鉴权：隧道建立请求携带与心跳同源的节点身份元数据（UUID 锚定，ADR-039）；CP 校验失败即拒绝隧道。

### 2.2 隧道登记与双模式取连接（CP 侧）

- CP gRPC server 注册 `TunnelService` handler（库自带 `tunnel.proto`，不入本仓 proto），affinity key = 节点 UUID。
- `internal/controlplane/grpc/pool.go` 取连接改两级：
  1. 该节点存在活跃反向隧道 → 返回隧道 `ClientConnInterface`；
  2. 否则回退现状直拨 `node.Host:GRPCPort`（含老 worker、隧道重建窗口）。
- 调用点零改动：pool 返回的接口签名不变，~70 个既有 RPC（含 `StreamInstanceEvents` / `DownloadArchive` 等流式）自动获得隧道承载。
- 观测面：节点心跳/详情暴露 `tunnelConnected: bool`（前端节点页显示「隧道已连 / 直拨回退」徽标，属本 FR 前端小改）。

### 2.3 边界语义

- **超时/取消**：gRPC deadline / context 取消经隧道原生传播，不自造。
- **背压**：库内置每虚拟流窗口控制；大流量（文件下载/反编译）经隧道吞吐低于直拨——验收含 ≥100MB 归档下载真机场景，不达标则该类 RPC 评估仍走直拨的定向豁免（记录到 ADR-066 后果）。
- **单节点单隧道**：同 UUID 重复建隧道，后建替换先建（与重注册语义一致，ADR-039）；替换瞬间在途 RPC 允许失败（上层既有重试/报错面兜底）。
- **msgsize**：隧道两端沿用既有 MaxRecvMsgSize 放宽值（64MB，探针部署已踩过 4MB 默认坑）。

## 3. M2 终端中转

### 3.1 proto（gate-api）

```proto
// WorkerService 新增（worker 侧实现，经隧道或直拨可达）：
rpc TerminalSession(stream TerminalClientFrame) returns (stream TerminalServerFrame);

message TerminalClientFrame {
  oneof payload {
    TerminalOpen open = 1;      // 首帧：instance_id + 一次性终端令牌（worker 用 WS 令牌密钥校验，ADR-061 信任模型不变）
    bytes stdin = 2;            // 终端输入
    TerminalResize resize = 3;  // 行列变更
  }
}
message TerminalServerFrame {
  oneof payload {
    bytes output = 1;           // 终端输出（stdout/stderr 合流，与既有 WS 帧语义一致）
    TerminalClosed closed = 2;  // 会话结束 + 原因
  }
}
```

- worker 侧 `TerminalSession` 桥接到既有终端会话层（与 9102 WS 服务同源实现，不复制会话逻辑）。

### 3.2 CP 中转端点（gate-api）

- `GET /api/nodes/:id/instances/:iid/terminal/relay`（WS upgrade）：鉴权沿用既有一次性终端令牌流程——前端先经既有 HTTP 端点取令牌，连本端点时以 query 携带；CP 透传令牌进 `TerminalSession` 首帧，**校验方仍是 worker**。CP 侧仅校验用户会话有权访问该实例（既有 permission 语义）+ 泵字节。
- 错误面：worker 拒令牌 → 结构化诊断透传（FR-276 语义保留）；隧道/直拨均不可达 → 既有离线短路语义。

### 3.3 前端切线

- 终端接线（`web/src/api/terminal.ts` 及消费组件）从 worker WS URL 改为 CP relay URL；**一律中转**（ADR-066 拍板，不做双路径）。
- 浏览器↔Worker WS 其余用途实施时盘点 9102 现役消费方（终端/日志/其他），逐一切到 CP 中转或确认本就走 CP；plugin-bridge（本机探针）保留在 9102。

### 3.4 文档与不变量同步（doc-sync）

- `.claude/rules/architecture-invariants.md`：「CP↔Worker gRPC」补方向性修订（隧道优先/直拨回退，见 ADR-066）；「浏览器 ↔ Worker WS」改为「浏览器不直连 Worker，终端经 CP 中转；Worker WS 仅本机探针 plugin-bridge」。
- `docs/ARCHITECTURE.md` 通信协议图与端口表；`docs/API.md` 新增 relay 端点；install-worker/防火墙指引改「worker 仅需出站可达 CP」。

## 4. 测试与验收

### 自动化
- 隧道单测：建立/鉴权拒绝/断连重建/同 UUID 替换。
- pool 双模式单测：有隧道走隧道、无隧道回退直拨、隧道消失中途回退。
- `TerminalSession` 单测：令牌校验（有效/无效/过期）、stdin/output 往返、resize、关闭语义。
- 集成：进程内 CP+Worker 经真隧道跑 `StartInstance`/`ListFiles`/`StreamInstanceEvents` happy path；e2e 全链路（`internal/e2e`）在隧道模式下回归。

### 真机（必验，测试绿 ≠ 真能用）
- **NAT 场景主验**：公网 CP（FR-277 部署主机可用）+ 家用 NAT Windows worker（9101/9102 不放行）：节点上线 → 指令（启停/文件/部署探针）→ **浏览器终端全链路** → ≥100MB 归档下载。
- **回退场景**：老版本 worker（未建隧道）对新 CP：全功能仍走直拨如常；升级指令可下发。
- **LAN 回归**：既有直连部署升级后无行为回归。

## 5. 交付物触点

| 类别 | 触点 |
|---|---|
| proto | `proto/worker.proto` + `make proto` 再生成 |
| Go 依赖 | `github.com/jhump/grpctunnel`（go.mod；gen-licenses 自动纳管） |
| Worker | `internal/worker/tunnel/`（新）、终端会话层桥接、main 装配 |
| CP | gRPC server 注册 TunnelService、`pool.go` 两级取连接、relay WS 端点（router）、节点观测字段 |
| 前端 | 终端接线切 relay、节点隧道状态徽标 |
| 文档 | architecture-invariants / ARCHITECTURE / API / OPERATIONS（防火墙指引）/ CHANGELOG |
