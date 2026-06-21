# Spec — FR-065 实时插件桥通道地基（ServerProbe 反向 WS ↔ Worker）

> 关联 FR: FR-065 | 优先级: P0 | 状态: 🔨 in-progress | 关联 ADR: ADR-016（取代 ADR-014，复活并扩展 ADR-012）

## 概述

打通「ServerProbe fork 反向 WS 连入本机 Worker」的实时双向通道地基，为后续玩家事件（FR-066）/ 治理迁移（FR-067）/ 在线更新（FR-068）/ 全状态查询铺底。本 FR **只铺通道**：Worker 侧会话/握手/心跳/connected·disconnected 冒泡、CP 侧 token 签发与下发、探针侧连入+心跳+一个 connected 事件、proto 一次铺齐桥全面。不含玩家事件采集、治理执行、退 RCON（那是下游 FR）。

## 通道协议

### 端点与方向

- Worker 暴露 `GET /ws/plugin-bridge`（与 `/ws/terminal` 并列、同一 WS 监听端口，默认 9102）。
- **探针主动反向连入**：`ws://127.0.0.1:<wsPort>/ws/plugin-bridge?token=<jwt>&instance=<uuid>`。
- 探针只与本机 Worker 通信，绝不直连 CP/DB/gRPC（架构不变量，见 ADR-016）。

### 握手鉴权（实例级 token，复用 JWT secret）

- token 为 HS256 JWT，claims：`instanceId`=实例 UUID、`scope=plugin-bridge`、`exp`、`iat`。
- Worker 用 `JIANMANAGER_JWT_SECRET` 校验：签名有效 + `scope == "plugin-bridge"` + token 内 `instanceId == query.instance`。任一不满足 → HTTP 401/400，不升级。
- token 仅握手校验一次，连上后长期有效（与终端 token 一致）。TTL 取数分钟（阻断重放）；写入探针 config 后探针长期复用，过期由下次 config 下发/重启续期。

### 会话表（单活动会话顶替）

- Worker 维护「实例 UUID → 插件会话」表。同一实例同时仅一活动会话：**新连顶替旧连**（旧 conn 主动关闭）。
- 连接成功 → 冒泡 `connected` 事件；断开 → 冒泡 `disconnected` 事件（经 gRPC `StreamPluginEvents` 到 CP）。

### 消息帧（WS 文本，JSON 行）

探针 → Worker 与 Worker → 探针 均为 JSON 对象，字段 `type` 区分：

| 方向 | type | 说明 |
|---|---|---|
| 探针→Worker | `hello` | 连上后首帧，携带探针自报的 `instance`、`platform`(bukkit/bungee)、`version` |
| 探针→Worker | `ping` | 心跳，Worker 回 `pong` |
| 探针→Worker | `event` | 业务事件（地基阶段仅 demo `connected`；玩家事件留 FR-066），含 `event` 子类型 + 载荷 |
| Worker→探针 | `welcome` | 握手通过后回执，确认会话建立 |
| Worker→探针 | `pong` | 心跳回应 |
| Worker→探针 | `command` | 治理/查询指令下发（地基阶段不实际执行，仅通道；语义留 FR-067） |

- 心跳：探针每 `N` 秒发 `ping`；Worker 读超时（> 2N）判定断线、关闭会话、冒泡 `disconnected`。
- 探针断线后自身按指数退避重连（初始 ~1s，上限 ~30s）。

## gRPC（proto 一次铺齐，下游不再改 proto）

新增到 `proto/worker.proto`（加性）：

```proto
// 插件桥事件流：CP 订阅某实例（或全部）的探针事件（connected/disconnected/心跳/玩家事件…）。
rpc StreamPluginEvents(StreamPluginEventsRequest) returns (stream PluginEvent);
// 插件桥指令下发：CP 经 Worker 向探针下发治理/查询指令。
rpc SendPluginCommand(SendPluginCommandRequest) returns (SendPluginCommandResponse);
// 查询子服全状态（在线列表/世界/TPS 等聚合），骨架，语义留下游。
rpc QueryServerState(QueryServerStateRequest) returns (QueryServerStateResponse);

message StreamPluginEventsRequest { string instance_uuid = 1; } // 空=所有实例

message PluginEvent {
  string instance_uuid = 1;
  // type: connected | disconnected | heartbeat | player_join | player_quit | chat | cross_server | command_result | state
  string type = 2;
  int64 timestamp = 3;
  string player_name = 4;
  string player_uuid = 5;
  string message = 6;       // chat 内容 / 描述
  string server = 7;        // 子服名（玩家所在/事件发生）
  string from_server = 8;   // cross_server：来源子服
  string to_server = 9;     // cross_server：目标子服
  string platform = 10;     // bukkit | bungee（来自探针 hello）
  string version = 11;      // 探针版本
  string request_id = 12;   // command_result 对应请求
  string raw_json = 13;     // 透传原始载荷（下游解析）
}

message PluginCommand {
  // action: kick | ban | unban | whitelist_add | whitelist_remove | list | query_state
  string action = 1;
  string target = 2;        // 目标玩家名/UUID
  string reason = 3;        // 踢/封原因
  repeated string args = 4; // 透传额外参数
  string request_id = 5;    // 关联 command_result
}

message SendPluginCommandRequest { string instance_uuid = 1; PluginCommand command = 2; }
message SendPluginCommandResponse { bool success = 1; string error = 2; string request_id = 3; }

message QueryServerStateRequest { string instance_uuid = 1; }
message QueryServerStateResponse {
  bool success = 1;
  string error = 2;
  bool connected = 3;       // 探针是否在线
  string state_json = 4;    // 聚合状态（下游填充）
}
```

- 本 FR 真实使用：`StreamPluginEvents`（connected/disconnected/heartbeat）。其余 message 字段铺骨架，FR-066/067/068 填语义。
- **workerpb 经 `make proto` 重新生成，严禁 sed 改 `worker.pb.go`**。

## CP 侧

- 新增插件桥 token 签发：实例级 HS256，claims `instanceId`+`scope=plugin-bridge`，TTL 数分钟。类比 `TerminalService.IssueToken`，但 scope 不同、不区分 read/write。
- 建服 provision / 探针 config 下发时把 token + worker WS 地址写入探针 `config.yml` 的 `bridge:` 段。

## ServerProbe fork 侧（submodule）

- core 模块新增反向 WS 客户端 `BridgeClient`（IOC `@Service`，`@PostEnable` 起 `@PreDestroy` 停，mirror `PrometheusExporter`）。
- JDK 8 兼容、零三方依赖：自写最小 RFC 6455 客户端（HTTP Upgrade 握手 + Sec-WebSocket-Key + 帧编解码 + 客户端掩码）。
- 行为：读 `bridge:` 配置（enabled/url/token/instance）→ 连入 → 发 `hello` → 周期 `ping` 心跳 → 发一个 demo `connected` 事件 → 断线指数退避重连。
- `config.yml` 加 `bridge:` 段（默认 enabled=false，由 CP 下发覆盖）。

## 验收对齐（PRD FR-065）

- [ ] ADR-016 写就，取代 ADR-014「只读+RCON」、复活 ADR-012 WS 通道；invariants 指向 ADR-016。
- [ ] 探针 fork 反向 WS 客户端：实例级 token 连入、HS256 握手、断线指数退避重连。
- [ ] Worker `/ws/plugin-bridge` + 会话表（单活动顶替）+ token 校验（复用 JWT secret）。
- [ ] CP 签发插件桥 token；探针 config 下发携带 token+ws 地址。
- [ ] proto 一次铺齐桥全面，workerpb 经 protoc 重新生成（禁 sed）。
- [ ] 真机：真 Paper + 探针 fork 连入真 Worker，日志见会话+心跳+重连（本环境无 JDK21/真 Paper 时如实标「待真机验」）。
