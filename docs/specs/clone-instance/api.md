# API Spec — FR-036 一键复制子服 + 配置修正 + 注册

> 关联 FR: FR-036 | 优先级: P1 | 关联 ADR-007 | 状态: 🔨 in-progress

## 概述

把一个 backend 子服复制为**独立**新实例：系统分配新目录/端口，复制文件时排除运行态文件，
配置引擎自动修正身份配置（端口/rcon/motd/可选 level-name，保留 forwarding secret），
并按需注册进 0/1/多个代理。

## 权限

平台管理员（与 FR-034/035 一致）。

## 约束

- 源实例须 `role=backend` 且 `type=minecraft_java`，否则 `422 NOT_A_BACKEND`。
- 源实例须 STOPPED 或 CRASHED（一致性快照），运行中返回 `422 SOURCE_RUNNING`。
- **同节点复制**：克隆落在源实例所在节点（CloneWorkDir 为本机本地拷贝，跨节点为后续工作）。

## Endpoints

### POST /api/v1/instances/:id/clone
- 复制源 backend（`:id`）为新实例。`dryRun=true` 仅预检不落盘。
- **请求**:
  ```json
  {
    "name": "lobby-2",
    "motd": "Lobby 2",
    "levelName": "",
    "registerToProxyIds": [30],
    "dryRun": false
  }
  ```
- **响应** (201 / 200 dryRun):
  ```json
  {
    "instance": { "id": 31, "name": "lobby-2", "role": "backend" },
    "allocated": { "workDir": "var/servers/lobby-2-a1b2c3", "serverPort": 25566, "rconPort": 25576, "queryPort": 25566 },
    "excluded": ["session.lock", "logs", "cache", "crash-reports", "usercache.json", "*.pid", "libraries/.cache"],
    "registrations": [ { "id": 5, "alias": "lobby-2", "proxyId": 30 } ],
    "warnings": [],
    "dryRun": false
  }
  ```
- **错误**: `404 INSTANCE_NOT_FOUND`；`422 NOT_A_BACKEND`；`422 SOURCE_RUNNING`；`502 CLONE_FAILED`（含已创建实例供重试/删除）

## Worker gRPC（新增）

| RPC | 类型 | 说明 |
|---|---|---|
| `CloneWorkDir(src_uuid, dst_uuid, exclude[])` | Unary | 本机复制源工作目录到目标，按 exclude 排除运行态文件，返回复制文件数/字节/跳过项 |

## 排除规则（CP 下发的默认 exclude）

`session.lock`、`*.pid`、`logs`、`crash-reports`、`cache`、`usercache.json`、`libraries/.cache`
匹配语义：目录前缀 / 精确相对路径 / 不含 `/` 的 basename glob。

## 自动修正（config 引擎，复制后）

读取目标 `server.properties`（由源拷贝而来）并修正：
- `server-port`/`query.port`/`rcon.port` → 新分配端口
- `rcon.password` → 新随机（不复用源密码）
- `motd` → 入参（可选）
- `level-name` → 入参（可选；改名即新世界，默认保留拷贝来的世界）
- **保留 forwarding secret 不变**（`config/paper-global.yml` 整体随工作目录复制）

## 注册（可选）

`registerToProxyIds` 中每个代理调用 FR-032 注册（触发 FR-035 同步写代理 servers + 下发 secret）。

## 依赖

- FR-031 配置引擎、FR-032 资源分配与注册、FR-033 JDK、FR-034 backend、FR-035 代理。
