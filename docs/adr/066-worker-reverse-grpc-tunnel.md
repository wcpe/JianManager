# ADR-066: CP→Worker 指令经 Worker 发起的 gRPC 反向隧道（免 NAT / 零入站）

- **日期**: 2026-07-12
- **状态**: accepted
- **取代关系**: **修订 ADR-002**（gRPC 节点通信）——gRPC 仍是节点间唯一 RPC 协议，本 ADR 只改「CP→Worker 调用的承载方向」：由 CP 直拨 worker `node.Host:GRPCPort` 改为**优先经 Worker 主动建立的反向隧道**；直拨降级为回退路径。**修订架构不变量「浏览器 ↔ Worker WS」**：浏览器终端改经 CP 中转，Worker WS 端口（9102）收敛为仅服务本机探针 plugin-bridge（ADR-012/016 探针通道不变）。
- **关联**: FR-281（本 ADR 落地）、ADR-039（节点 UUID 身份——隧道归属锚定）、ADR-061（WS 令牌密钥——终端令牌校验语义保留）、ADR-050（重连 resync——隧道断连窗口语义对齐）、FR-278（CP 内嵌 Worker——版本强一致为双模式过渡兜底）。

## 上下文

现状 CP→Worker 指令派发要求 CP 能直连 worker 的 gRPC 端口（9101），浏览器终端要求能直连 worker WS（9102）。真机复现：公网 CP 无法回拨家用 NAT 后的 Windows worker——节点能注册、能心跳（Worker→CP 出站正常），但一切指令（启停/文件/部署）与终端全部不可用。家用宽带 / 内网 / 云混合部署是目标用户常态，「worker 必须可入站」是产品级障碍。

已有关键事实：`WorkerService.Heartbeat` 已是 **Worker 发起的常驻双向流**（`stream HeartbeatRequest ⇄ stream HeartbeatResponse`），Worker→CP 出站通道（注册/心跳/密钥下发）成熟在运行；CP 侧 `internal/controlplane/grpc/pool.go` 以连接池直拨 worker 承载全部 ~70 个 RPC。

## 决策

### 1. 反向承载 = gRPC 隧道，不是指令信封

在 CP 既有 gRPC 端口上新增隧道服务：Worker 主动开常驻双向流，流内多路复用出**虚拟 gRPC 通道**；CP 侧把该虚拟通道当作指向该节点的 `grpc.ClientConnInterface` 使用——**全部既有 WorkerService RPC（含服务端流如 `StreamInstanceEvents`、`DownloadArchive`）零 proto 改动、零调用点改动地跑在隧道上**。

实现采用 `github.com/jhump/grpctunnel`（Apache-2.0，gRPC 官方生态的反向隧道库）：CP 注册 `TunnelService` handler（affinity key = 节点 UUID），Worker 以 `ReverseTunnelServer` 把自己的 WorkerService 实现挂到隧道上。**明确否决**「为每个指令定义 oneof 信封 + 手写分发」：~70 个 RPC 的信封化不可维护，且丢失 gRPC 原生的流语义与 deadline/取消传播。

鉴权：隧道建立复用心跳既有节点身份鉴权语义（UUID 锚定，ADR-039）；CP 按身份把隧道登记到节点，杜绝「节点 A 冒领节点 B 的指令」。

### 2. 双模式：隧道优先，直拨回退（用户拍板）

`pool.go` 取连接改为两级：**该节点存在活跃隧道 → 返回隧道通道；否则回退现状直拨**。老版本 worker（未建隧道）、隧道断连重建窗口内，行为与今天完全一致——升级不断链（CP 仍可对老 worker 下发升级指令）。Worker 侧隧道随心跳生命周期维护（指数退避重连）。节点观测面新增「隧道已连/直拨回退」状态。后续版本再评估移除直拨路径（届时另立 ADR）。

### 3. 终端经 CP 中转（用户拍板：本批完整做）

新增 `WorkerService.TerminalSession`（双向流）RPC：worker 侧桥接到既有终端会话层（与 WS 服务同源）；CP 新增面向浏览器的终端 WS 中转端点，浏览器 ⇄ CP WS ⇄（隧道或直拨）⇄ worker 终端会话。一次性终端令牌语义保留（ADR-061 密钥校验方仍是 worker——令牌随 `TerminalSession` 首帧传递，信任模型不变）。浏览器**一律走 CP 中转**（不做「可直连时直连」的双路径——单一路径心智 + 彻底移除「浏览器须可达 worker」前提）；9102 保留仅服务本机探针 plugin-bridge。浏览器↔Worker WS 的其余用途（日志流等）随实现盘点一并中转化。

### 4. 端口暴露面收敛

NAT 后 worker 零入站端口要求：9101/9102 仍监听（本机/回退用）但不再要求公网可达；`install-worker` 文档与防火墙指引同步（引导语从「放行 9101/9102」改为「worker 仅需出站可达 CP」）。

## 理由

- **零信封**：隧道复用 gRPC 原生分发，~70 RPC 与未来新增 RPC 自动获得免 NAT 能力，无第二套指令编解码真源。
- **双模式护升级**：FR-278 已给「CP 内嵌同版 worker」，但存量节点升级窗口内必须仍可被指挥；回退直拨保住这条命脉。
- **终端一律中转**：文件下载（`DownloadArchive`）等重流量本就经 CP gRPC 代理，终端并入同构；消除「浏览器与 worker 网络可达性」这一整类支持问题。
- **与 ADR-002 精神一致**：协议仍是 gRPC，改的只是「谁拨号」；不引入第二 RPC 协议。

## 后果

- 新依赖 `github.com/jhump/grpctunnel`（Apache-2.0，`gen-licenses` 自动纳管）。
- proto 新增 `TerminalSession`（终端桥接）；隧道服务由库的 `tunnel.proto` 提供，不入本仓 proto。
- `pool.go` 两级取连接；隧道多虚拟流共享单 HTTP/2 流，库内置每虚拟流窗口控制，重流量（文件/反编译）经隧道的吞吐上限低于直拨——回退路径与「同机房部署走直拨」缓解，真机验收含大文件场景。
- 架构不变量文档需改两处：「CP↔Worker gRPC」补方向性修订；「浏览器 ↔ Worker WS」改为「浏览器不直连 Worker；终端经 CP 中转；Worker WS 仅本机探针」。
- 前端终端接线从 worker WS 端点改为 CP 中转端点（一次性令牌流程保留）。

## 替代方案

- **oneof 指令信封 + 手写分发**：~70 RPC 信封化，每加一个 RPC 改三处，流式 RPC 语义要自造。否决（见决策 1）。
- **yamux/TCP 或 WebSocket 隧道 + 自定义拨号器**：可行但引入 gRPC 之外的第二传输层与自管多路复用，违 ADR-002 精神。否决。
- **frp/chisel 等外置隧道**：引入运维侧第三方组件，违「单二进制零外部依赖」立场。否决。
- **只做指令流、终端后续**：免入站目标不完整（NAT 下终端仍死），用户已拍板本批完整做。否决。
- **终端双路径（可直连则直连）**：省一跳延迟，但保留「浏览器须可达 worker」的支持负担与两套接线。否决，一律中转。
