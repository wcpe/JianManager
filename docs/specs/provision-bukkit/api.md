# API Spec — FR-034 搭建 Bukkit 子服

> 关联 FR: FR-034 | 优先级: P1 | 状态: 📋 todo

## 概述

FR-034 提供向导式创建 Paper/Spigot/Purpur 后端子服：自动下载核心、分配目录与端口、写入基础配置、绑定 JDK，并可选注册进代理。

## REST API

### GET /api/v1/cores
- **描述**: 查询可下载核心类型与版本。
- **Query**: `?type=paper&mcVersion=1.21.1`
- **响应**:
```json
[{ "type": "paper", "mcVersion": "1.21.1", "build": "latest", "downloadUrl": "https://.../paper.jar" }]
```

### POST /api/v1/instances/provision/bukkit
- **描述**: 向导创建 Bukkit 后端子服。
- **请求**:
```json
{
  "nodeId": 1,
  "name": "lobby",
  "coreType": "paper",
  "mcVersion": "1.21.1",
  "jdkId": 1,
  "memoryMb": 4096,
  "jvmArgs": ["-XX:+UseG1GC"],
  "registerToProxyIds": [2]
}
```
- **响应**: 创建后的 Instance。

## Worker gRPC

| RPC | 类型 | 说明 |
|---|---|---|
| `DownloadCore` | Unary | 下载 Paper/Spigot/Purpur 核心 jar |
| `ProvisionServer` | Unary | 创建工作目录、写 eula/config、生成启动配置 |

## 自动配置

- `eula.txt`: `eula=true`
- `server.properties`: 分配 `server-port`，代理模式下 `online-mode=false`
- `spigot.yml`: `settings.bungeecord=true`（Bungee/Waterfall）
- Paper 配置：启用 Velocity modern forwarding 并写入 secret（Velocity 场景）

## 依赖

- FR-031 配置引擎
- FR-032 资源分配与注册关系
- FR-033 JDK 管理
