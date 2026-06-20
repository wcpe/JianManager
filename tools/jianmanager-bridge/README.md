# JianManager 平台插件桥（FR-103 / ADR-012）

平台侧插件，经 WebSocket 连入 Worker Node 的 `/ws/plugin-bridge`：上报服务器/玩家事件
（在线/加入/退出/聊天）、执行平台下发的指令（踢/封/whitelist）、断线自动重连。

> 架构约束：插件**只与 Worker 通信**，不直连 Control Plane / 数据库 / gRPC。
> 事件经 Worker → CP gRPC 流 → 前端 SSE；指令经 CP → Worker gRPC → 插件。详见 `docs/adr/012-plugin-bridge-channel.md`。

## 模块

| 模块 | 产物 | 适用 |
|---|---|---|
| `common` | （内部库，被下面两个 shade） | 平台无关的 WS 连入/事件/指令/重连内核 |
| `bukkit` | `JianManagerBridge-Bukkit-<ver>.jar` | Paper / Spigot / Bukkit 后端子服 |
| `bungeecord` | `JianManagerBridge-BungeeCord-<ver>.jar` | BungeeCord / Waterfall 代理 |

## 构建

需要 JDK 17+ 与 Maven 3.8+：

```bash
cd tools/jianmanager-bridge
mvn -q clean package
# 产物：
#   bukkit/target/JianManagerBridge-Bukkit-0.1.0.jar
#   bungeecord/target/JianManagerBridge-BungeeCord-0.1.0.jar
```

shade 已重定位 `org.java_websocket` 与 `com.google.gson` 到 `com.jianmanager.bridge.libs.*`，避免与服务端/其它插件冲突。

## 安装与配置

1. 把对应 jar 放进服务端 `plugins/` 目录，启动一次生成默认配置。
2. 在 JianManager 面板对该实例「签发插件桥 token」（`POST /api/v1/instances/:id/plugin-token`），得到 `wsUrl` / `token` / `instanceUuid`。
3. 填入插件的 `config.yml`：

   ```yaml
   enabled: true
   wsUrl: "ws://<节点host>:<wsPort>/ws/plugin-bridge"
   token: "<平台签发的 token>"
   instanceUuid: "<实例 UUID>"
   reconnectDelayMillis: 5000
   ```

4. 重载/重启插件。连上后平台「已连插件列表」即显示该实例为已连接。

## 协议

WS JSON 行协议（与 Worker `/ws/plugin-bridge` 对接），详见 `docs/ARCHITECTURE.md` 6.2.1。

- 上行（插件 → Worker）：`hello` / `event`（`player_join`/`player_quit`/`player_chat`/`server_status`）/ `pong` / `command_result`
- 下行（Worker → 插件）：`command`（`kick`/`ban`/`unban`/`whitelist_add`/`whitelist_remove`）/ `ping`

## 平台能力差异

- Bukkit：kick/ban/unban/whitelist 均经 Bukkit 原生 API（封禁写 NAME 封禁表，白名单写 whitelist）。
- BungeeCord：核心仅原生支持 kick；ban/unban/whitelist 以「踢出 + 转发代理控制台命令」尽力而为，需网络装有对应命令的封禁/白名单插件方完整生效。
