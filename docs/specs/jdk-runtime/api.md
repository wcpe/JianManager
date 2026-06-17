# API Spec — FR-033 JDK 与运行时管理

> 关联 FR: FR-033 | 优先级: P0 | 状态: 📋 todo

## 概述

FR-033 为节点托管多 JDK，并允许实例绑定具体 JDK 或 Java 大版本。启动实例时 Worker 注入 `JAVA_HOME` 和 `PATH`，再叠加实例自定义环境变量。

## REST API

### GET /api/v1/nodes/:id/jdks
- **描述**: 查询节点 JDK 注册表。
- **权限**: `node.read`
- **响应**:
```json
[
  { "id": 1, "nodeId": 1, "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4", "arch": "x64", "path": "C:/jm/jdks/temurin-21", "managed": true, "createdAt": "datetime" }
]
```

### POST /api/v1/nodes/:id/jdks
- **描述**: 登记系统已有 JDK 或创建待安装任务。
- **权限**: `node.write`
- **请求**:
```json
{ "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4", "arch": "x64", "path": "C:/Java/jdk-21", "managed": false }
```

### POST /api/v1/nodes/:id/jdks/install
- **描述**: 请求 Worker 下载并安装指定 JDK 到节点托管目录。
- **权限**: `node.write`
- **请求**: `{ "vendor": "Temurin", "majorVersion": 21, "arch": "x64" }`

### DELETE /api/v1/nodes/:id/jdks/:jid
- **描述**: 删除 JDK。若有实例占用则拒绝。
- **权限**: `node.write`
- **错误**: 409 返回占用实例列表。

## 实例 API 扩展

`POST /api/v1/instances` 和 `PUT /api/v1/instances/:id` 增加：
```json
{
  "jdkId": 1,
  "javaMajorVersion": 21,
  "envVars": { "ENABLE_RCON": "true" },
  "launchSpec": {
    "jarPath": "server.jar",
    "minMemoryMb": 1024,
    "maxMemoryMb": 4096,
    "jvmArgs": ["-XX:+UseG1GC"],
    "programArgs": ["nogui"]
  }
}
```

兼容策略：`generic` / `universal` 实例可继续使用自由文本 `startCommand`；`minecraft_java` 优先使用结构化 `launchSpec`。

## Worker gRPC

| RPC | 类型 | 说明 |
|---|---|---|
| `ListJDKs` | Unary | 查询 Worker 本地 JDK 注册表/探测结果 |
| `InstallJDK` | Unary | 下载并安装 JDK |
| `RemoveJDK` | Unary | 删除托管 JDK |

实例创建 RPC 扩展 JDK/运行时字段，并最终进入 `process.CommandSpec`。

## 错误处理

| 场景 | HTTP | 说明 |
|---|---:|---|
| JDK 不存在 | 404 | 节点未登记该 JDK |
| JDK 被占用 | 409 | 返回占用实例 |
| Worker 离线 | 503 | 无法安装/探测 |
| 下载失败 | 502 | 下载源不可用 |
