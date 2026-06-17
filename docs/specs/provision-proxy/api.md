# API Spec — FR-035 搭建代理（BungeeCord/Velocity）

> 关联 FR: FR-035 | 优先级: P1 | 状态: 📋 todo

## 概述

FR-035 提供向导式创建 BungeeCord/Waterfall/Velocity 代理实例：自动下载核心、分配目录与监听端口、生成转发配置与 secret，并将 backend 注册进代理。

## REST API

### POST /api/v1/instances/provision/proxy
- **描述**: 向导创建代理实例。
- **请求**:
```json
{
  "nodeId": 1,
  "name": "velocity-main",
  "proxyType": "velocity",
  "mcVersion": "1.21.1",
  "jdkId": 1,
  "listenPort": 25565,
  "backendRegistrations": [
    { "backendInstanceId": 21, "alias": "lobby", "priority": 0, "forcedHost": "play.example.com" }
  ]
}
```
- **响应**: 创建后的代理 Instance 与注册结果。

复用 FR-032 的：
- `POST /api/v1/proxies/:id/registrations`
- `GET /api/v1/proxies/:id/registrations`
- `DELETE /api/v1/proxies/:id/registrations/:registrationId`

## Worker gRPC

| RPC | 类型 | 说明 |
|---|---|---|
| `ProvisionProxy` | Unary | 创建代理目录、下载 jar、生成配置 |

## 自动配置

### BungeeCord/Waterfall
- `config.yml`: `ip_forward=true`
- `servers`: 写入 backend alias/address/port
- `priorities`: 写入默认优先级
- `forced_hosts`: 按注册关系生成

### Velocity
- `velocity.toml`: `player-info-forwarding-mode=modern`
- 生成并保存 `forwarding-secret`
- 将 secret 下发到所注册 backend 的 Paper 配置

## 依赖

- FR-031 配置引擎
- FR-032 注册关系模型
- FR-033 JDK 管理
