# API Spec — FR-036 一键复制子服 + 配置修正 + 注册

> 关联 FR: FR-036 | 优先级: P1 | 状态: 📋 todo

## 概述

FR-036 将一个 backend 子服复制为独立新实例：系统分配新目录和端口，复制文件时排除运行态文件，自动修正身份配置，并按需注册到 0/1/多个代理。

## REST API

### POST /api/v1/instances/:id/clone
- **描述**: 复制 backend 子服。
- **权限**: `instance.create`
- **请求**:
```json
{
  "name": "lobby-2",
  "nodeId": 1,
  "motd": "Lobby 2",
  "levelName": "world_lobby_2",
  "registerToProxyIds": [2, 3],
  "dryRun": false
}
```
- **响应**:
```json
{
  "instance": { "id": 31, "name": "lobby-2", "role": "backend" },
  "allocated": { "workDir": "servers/lobby-2-a1b2c3", "serverPort": 25566, "rconPort": 25576, "queryPort": 25586 },
  "registrations": [{ "proxyId": 2, "alias": "lobby-2" }]
}
```

### POST /api/v1/instances/:id/clone/preview
- **描述**: 复制预检，不落盘。
- **响应**: 分配结果、将排除的文件、潜在冲突和将修改的配置项。

## Worker gRPC

| RPC | 类型 | 说明 |
|---|---|---|
| `CloneInstance` | Unary | 复制工作目录并排除运行态文件 |

## 排除规则

- `session.lock`
- `logs/**`
- `cache/**`
- `usercache.json`
- `*.pid`
- `crash-reports/**`
- `libraries/.cache/**`

## 自动修正

- `server.properties`: server-port/rcon.port/query.port、motd、level-name（可选）
- 保留 forwarding secret 不变
- 注册进所选代理的 servers/priorities/forced-host

## 依赖

- FR-031 配置引擎
- FR-032 资源分配与注册关系
- FR-033 JDK 管理
- FR-034 Bukkit 子服
- FR-035 代理
