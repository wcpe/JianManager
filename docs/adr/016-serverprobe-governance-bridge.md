# ADR-016: ServerProbe 治理桥——探针经反向 WebSocket 连入 Worker 的实时双向通道

- **日期**: 2026-06-22
- **状态**: accepted
- **取代**: [ADR-014](014-monitoring-via-serverprobe.md)（部分取代：保留「以 ServerProbe 作监控探针、内嵌 jar、建服自动部署」，**推翻其「探针只读 + 玩家治理走 RCON」的决策**，改由探针经反向 WS 承载治理/实时事件/全状态查询）
- **复活并扩展**: [ADR-012](012-plugin-bridge-channel.md)（复活其「平台插件经 WS 连入 Worker」的通道设计；**载体由自写 Bukkit/BC 插件改为 ServerProbe 探针**，能力由「玩家事件 + 治理」扩展到「治理 + 在线更新 + 全状态查询」）

## 上下文

ADR-012 当初设计了「平台插件经 Worker WS 连入」的实时双向通道，但要自写并维护 Bukkit/BungeeCord 双端 Java 插件；ADR-014 出于「监控指标才是当下主要痛点、治理已由 RCON 覆盖」的判断，退役了自写插件桥，改用开源只读监控探针 ServerProbe（TabooLib，单 jar 多端），仅经 HTTP `/metrics` 抓指标。

随着运营底座推进（ServerProbe 治理桥 epic），ADR-014「探针只读 + RCON 治理」的局限暴露：

- **RCON 无实时事件**：玩家 join/quit/chat、跨服路由（BC 端）无法实时感知，只能轮询或文本解析，时延高且脆弱。
- **RCON 治理能力受限且为额外暴露面**：踢/封/白名单经 RCON 文本协议，跨服在线列表聚合困难；RCON 端口本身是一类需要鉴权与端口分配的网络面。
- **探针已是同机进程**：ServerProbe 运行在游戏服 JVM、与 Worker 同机，且已是项目自有 fork（上游即本项目所有者），具备「主动反向连入 Worker」的天然条件——不必再自写第二个插件。

因此：把 ADR-012 验证过的「WS 连入 Worker」通道**复活**，载体换成已经在用的 ServerProbe 探针；把 ADR-014 的「探针只读」**升级**为「探针双向」，治理/事件/在线更新/全状态查询都走这一条实时通道。**本 ADR 只确立通道地基（FR-065）**；玩家事件落地（FR-066）、治理迁移 + 退役 RCON（FR-067）、在线更新（FR-068）为后续 FR，各自复用本通道、不再改通道协议与 proto 面。

## 决策

1. **探针经反向 WebSocket 连入 Worker Node**：Worker（重新）暴露 WS 端点 `/ws/plugin-bridge`，与终端 WS `/ws/terminal` 并列、同一 WS 监听端口（默认 9102）。ServerProbe fork 内新增反向 WS 客户端模块，插件启用后**主动**以 `ws://127.0.0.1:<wsPort>/ws/plugin-bridge?token=<jwt>&instance=<uuid>` 连入本机 Worker。探针**只与本机 Worker 通信**，绝不直连 Control Plane / 数据库 / gRPC；游戏服与 Worker 同机，走本机回环，零额外对外网络面。

2. **token 鉴权（实例级，复用 JWT secret，类比终端 token）**：CP 为某实例签发插件桥连接 token（HS256 JWT，claims 含 `instanceId`=实例 UUID + `scope=plugin-bridge`，TTL 数分钟），由建服 provision / 探针 config 下发时写入该实例的探针 `config.yml`。探针握手时携带 `?token=...&instance=<uuid>`，Worker 用同一 `JIANMANAGER_JWT_SECRET` 校验**签名 + `scope=plugin-bridge` + token 内 instanceId 与 query 的 instance 一致**后建会话。token 仅握手时校验一次，连上后长期有效（与终端 token 一致）；TTL 须明显大于探针重连窗口，过期由探针下次 config 拉取/重启续期（地基阶段：token 写入配置即长期可用，TTL 取数分钟用于阻断重放，连上后不再校验）。

3. **会话表与生命周期（单活动会话顶替）**：Worker 维护「实例 UUID → 插件会话」表，**同一实例同时仅一活动会话，新连顶替旧连**（旧连接被主动关闭）。连接/断开作为 `connected` / `disconnected` 事件经 gRPC `StreamPluginEvents` 冒泡到 CP，前端据此显示探针连接状态。心跳：探针周期性发 `ping`、Worker 回 `pong`（或反之），任一端超时即判定断线；探针断线后自身负责指数退避重连。

4. **proto 一次铺齐桥的全面（下游不再改 proto）**：`proto/worker.proto` 加性新增：
   - `rpc StreamPluginEvents(StreamPluginEventsRequest) returns (stream PluginEvent)`——CP 订阅某实例（或全部）的插件事件流；
   - `rpc SendPluginCommand(SendPluginCommandRequest) returns (SendPluginCommandResponse)`——CP 经 Worker 向探针下发治理指令；
   - `message PluginEvent`：`type` 枚举 connected / disconnected / heartbeat / player_join / player_quit / chat / cross_server / command_result / state（含足量字段：instance_uuid、player_name/uuid、message、server（子服名）、from_server/to_server、raw_json 等），覆盖 FR-066/067/068；
   - `message PluginCommand` + `SendPluginCommandRequest/Response`：`action` 枚举 kick / ban / unban / whitelist_add / whitelist_remove / list / query_state（含 target、reason、args、request_id 等）；
   - `message QueryServerStateRequest/Response` 等全状态查询骨架。
   本 FR 仅铺 message 骨架与 connected/disconnected/heartbeat 真实使用，其余事件/命令字段留给 FR-066/067/068 填充语义。**经 protoc 重新生成 workerpb（`make proto`），严禁 sed 改 `worker.pb.go`**（见 commit c1cb5af 教训）。

5. **事件上行 / 指令下行链路（复刻已验证形态，本 FR 仅打通通道层）**：
   - 上行：探针 →(WS) Worker →(gRPC server stream `StreamPluginEvents`) CP →(后续 SSE) 浏览器。复刻 `StreamInstanceEvents` → `EventService` 形态。
   - 下行：浏览器 →(HTTP) CP →(gRPC `SendPluginCommand`) Worker →(WS) 探针 →(执行平台 API)。复刻 Bot 指令下发形态。
   - 本 FR（地基）只保证 Worker 侧会话/握手/心跳/connected·disconnected 冒泡与 proto 面就位；CP→SSE→前端与具体治理执行分别在 FR-066/067 落地。

6. **保留 ADR-014 的监控部署链路不动**：ServerProbe 仍作 git 子模块、仍经 `go:embed` 内嵌 jar、仍由 provision 经 `DeployServerProbe(jar, config_yaml)` 部署。本 ADR 只在其 `config.yml` 内**加性追加 `bridge:` 段**（worker WS 地址 + 实例级 token + 开关），不改 `metrics:` 段。`/metrics` 抓取（实时面板拉取 + 心跳自采历史时序）与反向 WS 桥**并存、互补**：前者只读拉指标，后者双向承载事件/治理；FR-067 才把指标也并入纯探针、退役 RCON。

## 理由

- **复用而非重造**：复活 ADR-012 已论证过的「WS 连入 Worker」通道（不破进程边界、不新增对外入口、严守「插件只与 Worker 通信」），但把载体换成已经在用的 ServerProbe，省掉自写第二个 Java 插件的全部成本。
- **探针是项目自有 fork**：上游即本项目所有者，可直接在 fork 内加反向 WS 客户端并提交回去，无第三方协作摩擦。
- **一次铺全 proto 面**：把事件/命令/状态查询 message 一次性铺齐，下游 FR-066/067/068/076 只填语义、不再改 proto，避免反复 `make proto` 与 pb 重生成风险。
- **与监控并存**：`/metrics` 只读链路成熟稳定，治理/事件走新通道，互不耦合；RCON 退役（FR-067）可独立推进，本 FR 不动 RCON。

## 后果

- **架构不变量**：通信不变量清单的「插件桥」一行**指向 ADR-016**，载体明确为 **ServerProbe 探针**，方向为**探针主动反向连入 Worker**（`/ws/plugin-bridge`，实例级 token）。`.claude/rules/architecture-invariants.md` 与 `docs/ARCHITECTURE.md` 通信协议表同步更新（原 ADR-014 收回的「插件 ↔ Worker WS」一行复活并改写为「探针 ↔ Worker 反向 WS（token）」）。
- **proto**：加性新增 `StreamPluginEvents` / `SendPluginCommand` / `PluginEvent` / `PluginCommand` / `QueryServerState*` 等 message 与 rpc；`make proto` 重新生成 workerpb。加性，不破坏既有 RPC。
- **Worker**：新增 `internal/worker/ws/bridge.go`（`/ws/plugin-bridge` 会话表 + token 校验 + 心跳 + connected/disconnected），WS mux 加挂端点；gRPC server 新增 `StreamPluginEvents` / `SendPluginCommand` 实现（事件经独立订阅者扇出，复刻 instanceEvent 总线）。
- **CP**：新增插件桥 token 签发能力（实例级 HS256，TTL 数分钟，类比终端 token）；建服 provision / 探针 config 下发时把 token + worker WS 地址写入探针 `config.yml` 的 `bridge:` 段。
- **ServerProbe fork**：core 模块新增反向 WS 客户端（JDK 8 兼容、零三方依赖的最小 RFC 6455 客户端：HTTP Upgrade 握手 + 帧编解码 + 掩码），作 IOC `@Service`，`@PostEnable` 起、`@PreDestroy` 停，断线指数退避重连、心跳、connected/disconnected 上报；`config.yml` 加 `bridge:` 段。fork 改动在子模块仓库提交，父仓库更新子模块指针。
- **ADR-014 状态**：标记为「被 ADR-016 部分取代」——监控部署链路保留，「探针只读 + RCON 治理」推翻。
- **关联 FR**：FR-065（本 ADR，通道地基）；FR-066（玩家事件）、FR-067（治理迁移 + 退 RCON）、FR-068（在线更新）为消费方，复用本通道。

## 替代方案

- **维持 ADR-014（探针只读 + RCON 治理）** — 无实时事件、跨服感知与治理受限、RCON 为额外暴露面，满足不了 epic 的实时治理需求，否决。
- **自写第二个 Bukkit/BC 插件（回到 ADR-012 原载体）** — 已有 ServerProbe 在用且为自有 fork，再写一个插件是重复维护，否决。
- **探针直连 CP（HTTP/WS/gRPC）** — 破坏三进程模型、给 CP 增非浏览器暴露面、违反「插件不直连 CP/gRPC」，否决（与 ADR-012 同理）。
- **探针复用守护进程二进制帧协议** — 该通道是 Worker↔daemon wrapper 专用、语义不符；WS + JSON 对探针实现更友好，否决。
- **引入第三方 WS 库到探针** — 探针锁 JDK 8、追求零运行时三方依赖（与 `/metrics` 用 JDK 自带 HttpServer 一致），故自写最小 WS 客户端而非引库，否决引库。
