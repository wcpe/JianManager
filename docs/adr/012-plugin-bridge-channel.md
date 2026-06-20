# ADR-012: 插件桥——平台插件经 Worker WebSocket 连入的通信通道

- **日期**: 2026-06-20
- **状态**: superseded-by [ADR-014](014-monitoring-via-serverprobe.md)（2026-06-21 改用 ServerProbe 作监控探针、退役自写插件桥；玩家治理走 RCON）
- **上下文**: 玩家管理等运维能力此前只能经 RCON 文本协议实现（FR-054），RCON 无实时事件、跨服感知靠文本解析、能力受限。要拿到「玩家加入/退出/聊天」的实时精确事件并精确执行踢/封/白名单（FR-055 依赖），需要在游戏服 JVM 内运行平台侧插件（Bukkit/BungeeCord），由它主动把事件推给平台、并接收平台指令。问题是：插件运行在游戏服 JVM（与 Worker 同机），它该接入哪一层、走什么协议，且不得破坏三进程模型与既有通信边界。
- **决策**:
  1. **插件经 WebSocket 连入 Worker Node**：Worker 新增 WS 端点 `/ws/plugin-bridge`，复用 Worker 既有 WS 服务体系（与终端 WS `/ws/terminal` 并列，同一 WS 监听端口）。插件**只与 Worker 通信**，绝不直连 Control Plane / 数据库 / gRPC。游戏服与 Worker 同机，走本机回环 WS，零额外网络面。
  2. **token 鉴权（实例级，类比终端一次性 token）**：CP 为某实例签发插件桥连接 token（HS256 JWT，claims 含 `instanceId` + `scope=plugin-bridge`，TTL 数分钟），由运维写入该实例的插件配置。插件握手时携带 `?token=...&instance=<uuid>`，Worker 用同一 `JIANMANAGER_JWT_SECRET` 校验签名、scope 与 instance 一致后建会话。token 仅握手时校验一次，连上后长期有效（与终端 token 一致）。
  3. **事件上行链路**：插件 →(WS) Worker →(gRPC server stream `StreamPluginEvents`) CP →(SSE `/api/v1/plugins/events`) 浏览器。完全复刻实例事件链路（`StreamInstanceEvents` → `EventService` → SSE），不引入新代理形态。
  4. **指令下行链路**：浏览器 →(HTTP) CP →(gRPC `SendPluginCommand`) Worker →(WS) 插件 →(执行平台 API：踢/封/whitelist…)。CP 按实例 UUID 解析所属节点、取 gRPC 客户端下发，复刻 Bot 指令下发（`SetBotBehavior` 等）。
  5. **会话生命周期与连接状态**：Worker 维护「实例 UUID → 插件会话」表（同一实例同时仅一活动会话，新连顶替旧连）；连接/断开作为 `connected`/`disconnected` 事件经 `StreamPluginEvents` 冒泡到 CP，前端据此显示「已连接插件列表/连接状态」。插件断线后自身负责退避重连。
- **理由**:
  - 复用 Worker 既有 WS 通道与一次性 token 鉴权，**不新增进程边界、不新增对外网络入口**，严格守住「插件只与 Worker 通信」。
  - 事件/指令完全复刻已验证的实例事件 SSE 与 Bot 指令链路，CP 仍是唯一 DB 入口与唯一面向浏览器入口，不破坏依赖方向。
  - 相比「插件直连 CP」（破坏三进程模型、给 CP 增暴露面）与「RCON 增强」（无实时事件、文本脆弱），WS 连入 Worker 是唯一同时满足实时性与架构不变量的方案。
- **后果**:
  - 通信不变量清单新增一类：**「插件 ↔ Worker WS（token 鉴权）」**，与「浏览器 ↔ Worker WS（仅终端/日志）」并列。ARCHITECTURE.md 通信协议表与 `.claude/rules/architecture-invariants.md` 同步加性追加一行。
  - proto 加性新增 `StreamPluginEvents`（server stream）+ `SendPluginCommand` + 相关 message；`make proto` 重新生成 workerpb。
  - 平台插件本体（Bukkit + BungeeCord 两个最小 Java 插件）随仓库提供（`tools/jianmanager-bridge/`），含 `plugin.yml` + WS 连入 + 事件上报 + 指令执行 + 断线重连。
  - 关联 FR-103（建桥/通道，本 ADR）、FR-055（消费方：玩家管理插件桥增强，后续）。
- **替代方案**:
  - **插件直连 Control Plane（HTTP/WS）** — 破坏三进程模型（游戏服侧组件直连面向浏览器入口）、给 CP 增非浏览器暴露面，否决。
  - **插件直连 CP gRPC** — 违反「仅 Worker↔CP 用 gRPC」「插件不直连 gRPC」，且要给游戏服侧发 proto 客户端，过重，否决。
  - **仅强化 RCON（不引插件）** — 无实时 join/quit/chat 事件、跨服感知靠文本解析、能力上限低，满足不了 FR-055，作为未装插件时的回落路径保留（FR-054）而非替代。
  - **插件 ↔ Worker 复用守护进程二进制帧协议（Unix Socket/Named Pipe）** — 该通道是 Worker↔daemon wrapper 专用、面向 Java 子进程 stdio 转发；让任意第三方插件接入它需重实现帧编解码且语义不符，WS + JSON 对插件作者更友好，否决。
