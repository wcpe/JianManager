# API Spec — FR-035 搭建代理（BungeeCord/Waterfall/Velocity）

> 关联 FR: FR-035 | 优先级: P1 | 关联 ADR-007/008 | 状态: 🔨 in-progress

## 概述

向导式创建 BungeeCord/Waterfall/Velocity 代理实例：下载核心、系统分配目录与监听端口、
生成转发配置与 secret，并把已有 backend 注册进代理（写代理 servers/priorities/forced-host）。
Velocity modern 转发的 secret 自动下发到所注册后端的 paper 配置，并校验跨代理一致。

## 权限

平台管理员（与 FR-034 一致）。

## 数据模型增量

- `Instance.forwarding_secret`（新增，`json:"-"`）：Velocity modern 转发的 forwarding secret，
  代理 provision 时生成；下发到所注册后端 + 跨代理一致校验复用。BungeeCord/Waterfall 不使用。

## 核心下载源（CoreService 扩展）

| proxyType | 下载源 | 版本 |
|---|---|---|
| `velocity` | PaperMC API（project=velocity） | 列出可用版本，build<=0 取最新 |
| `waterfall` | PaperMC API（project=waterfall） | 同上（已 EOL，仍可下载） |
| `bungeecord` | md-5 Jenkins `lastSuccessfulBuild` 单一 jar | 仅 `latest`，无 sha256 校验 |

`GET /cores?type=velocity|waterfall|bungeecord[&mcVersion=&build=]` 复用 FR-034 端点。

## Endpoints

### POST /api/v1/instances/provision/proxy
- 向导创建代理实例（role=proxy）。
- **请求**:
  ```json
  {
    "nodeId": 1,
    "name": "velocity-main",
    "proxyType": "velocity",
    "version": "3.3.0-SNAPSHOT",
    "jdkId": 1,
    "memoryMb": 1024,
    "jvmArgs": ["-XX:+UseG1GC"],
    "groupId": 0,
    "backendRegistrations": [
      { "backendId": 21, "alias": "lobby", "priority": 0, "forcedHost": "", "restricted": false }
    ]
  }
  ```
- **响应** (201):
  ```json
  {
    "instance": { "id": 30, "name": "velocity-main", "role": "proxy", "serverPort": 25565 },
    "forwardingSecret": "<仅 velocity 返回一次>",
    "registrations": [ { "id": 1, "alias": "lobby", "backendId": 21 } ],
    "warnings": ["backend 21 当前离线，secret 已写入配置，重启后生效"]
  }
  ```
- **错误**: `502 PROVISION_FAILED`（含已创建实例供重试/删除）；`422 INVALID_REQUEST`

### POST /api/v1/proxies/:id/registrations  （复用 FR-032）
- 落库 + **同步写代理配置**（servers/priorities/try/forced-host）+ Velocity secret 下发后端。
- 同步失败时仍返回 201，附 `warning`（关系是事实来源，可 resync）。

### GET / PATCH / DELETE /api/v1/proxies/:id/registrations[/:rid]  （复用 FR-032）
- 变更后同步代理配置（DELETE 从 servers 移除）。

### POST /api/v1/proxies/:id/resync
- 重新把注册关系与 secret 推到代理配置与各后端（代理离线/重启后手动重推）。
- **响应** (200): `{ synced: true, secretConsistent: bool, warnings: [...] }`

## 自动配置生成

### Velocity（velocity.toml + forwarding.secret）
- `player-info-forwarding-mode = "modern"`，`forwarding-secret-file = "forwarding.secret"`
- `bind = "0.0.0.0:<listenPort>"`
- `[servers]`：`alias = "<host>:<backendServerPort>"`（同节点用 127.0.0.1，跨节点用后端节点 host）
- `try = [...]`（按 priority 升序的 alias）
- `[forced-hosts]`：`"<domain>" = ["<alias>"]`
- `forwarding.secret` 文件写入生成的 secret

### 后端 paper 配置（secret 下发）
- 读取后端 `config/paper-global.yml`（不存在则新建最小档，Paper 启动时补全默认）
- 设 `proxies.velocity.enabled=true`、`proxies.velocity.online-mode=true`、`proxies.velocity.secret=<secret>`
- 跨代理一致校验：某后端注册进的所有 velocity 代理 secret 必须一致，否则 warning

### BungeeCord / Waterfall（config.yml）
- `ip_forward: true`
- `listeners[0].host = "0.0.0.0:<listenPort>"`，`listeners[0].priorities = [alias...]`，`listeners[0].forced_hosts`
- `servers.<alias>.address = "<host>:<backendServerPort>"`、`restricted`
- 后端侧：`spigot.yml settings.bungeecord=true` + `paper-global.yml proxies.bungee-cord.online-mode=false`

## 通信

- 配置读写复用既有 gRPC `WriteConfig` / `ReadConfig`（写 velocity.toml/config.yml/forwarding.secret/后端 paper-global.yml），**无需新增 worker RPC**。
- 代理实例创建复用 `InstanceService.Create`（role=proxy）+ 结构化启动（LaunchSpec，OmitNogui=true，代理不接受 nogui）。

## 依赖

- FR-031 配置引擎（YAML/TOML 读写）、FR-032 注册关系模型、FR-033 JDK、FR-034 provision 管线。
