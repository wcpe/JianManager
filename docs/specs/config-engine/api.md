# API Spec — FR-031 配置文件管理引擎

> 关联 FR: FR-031 | 优先级: P0 | 状态: 📋 todo

## 概述

FR-031 为 V2 群组服运维提供配置文件管理底座：Control Plane 提供配置专用 REST API，配置实际读写委托 Worker 在实例 `workDir` 内完成；Control Plane 持久化每次保存的版本记录，用于 diff 与回滚。

## REST API

### GET /api/v1/instances/:id/configs
- **描述**: 列出实例工作目录内可管理的配置文件。
- **权限**: `instance.file`
- **Query**: `?path=` 可选子目录，默认根目录。
- **响应**:
```json
[
  { "path": "server.properties", "format": "properties", "size": 512, "updatedAt": "datetime", "supported": true },
  { "path": "plugins/Example/config.yml", "format": "yaml", "size": 2048, "updatedAt": "datetime", "supported": true }
]
```

### GET /api/v1/instances/:id/configs/read
- **描述**: 读取单个配置文件，返回原始文本、解析结构、schema 与校验结果。
- **权限**: `instance.file`
- **Query**: `?path=server.properties`
- **响应**:
```json
{
  "path": "server.properties",
  "format": "properties",
  "content": "server-port=25565\n",
  "fields": [
    { "key": "server-port", "value": 25565, "type": "int", "description": "服务端监听端口" }
  ],
  "schema": { "known": true, "name": "server.properties" },
  "validation": { "valid": true, "issues": [] }
}
```

### POST /api/v1/instances/:id/configs/write
- **描述**: 写入配置文件。支持原始文本模式与结构化字段模式；保存成功后生成版本记录。
- **权限**: `instance.file`
- **请求**:
```json
{
  "path": "server.properties",
  "content": "server-port=25566\n",
  "fields": null,
  "message": "修改子服端口"
}
```
- **响应**:
```json
{ "versionId": 12, "validation": { "valid": true, "issues": [] } }
```

### GET /api/v1/instances/:id/configs/:file/versions
- **描述**: 查询配置文件历史版本。
- **权限**: `instance.file`
- **响应**:
```json
[
  { "id": 12, "filePath": "server.properties", "message": "修改子服端口", "createdAt": "datetime", "authorId": 1 }
]
```

### GET /api/v1/instances/:id/configs/:file/versions/:versionId/diff
- **描述**: 查询指定版本与当前文件的文本 diff。
- **权限**: `instance.file`

### POST /api/v1/instances/:id/configs/:file/rollback
- **描述**: 回滚到指定版本，并生成新的版本记录。
- **权限**: `instance.file`
- **请求**: `{ "versionId": 12, "message": "回滚端口修改" }`

## Worker gRPC

在 `worker.WorkerService` 中新增配置专用 RPC：

| RPC | 类型 | 说明 |
|---|---|---|
| `ListConfigFiles` | Unary | 列出可管理配置文件 |
| `ReadConfig` | Unary | 读取配置并解析 |
| `WriteConfig` | Unary | 按文本或字段写回配置 |
| `ValidateConfig` | Unary | 校验单文件/跨文件一致性 |

版本记录由 Control Plane 数据库管理，Worker 不持久化版本。

## 支持格式

| 格式 | 扩展名 | MVP 行为 | 后续增强 |
|---|---|---|---|
| properties | `.properties` | 保留注释/顺序的行级解析与回写 | schema 全覆盖 |
| yaml | `.yml`, `.yaml` | 原文保存 + 基础语法校验 | round-trip AST |
| toml | `.toml` | 原文保存 + 基础语法校验 | round-trip AST |
| json | `.json` | 格式化/校验 | schema 表单 |
| txt | `.txt`, `.conf` | 原文编辑 | 按已知文件补 schema |

## 校验规则 MVP

- 同一节点内 `server-port` / `rcon.port` / `query.port` 不重复。
- `online-mode=false` 与代理转发配置配套提示。
- Velocity `forwarding-secret` 与后端 Paper 配置一致性提示。

## 错误处理

| 场景 | HTTP | 说明 |
|---|---:|---|
| 实例不存在或无权访问 | 404 | 避免泄露资源存在性 |
| 节点离线 | 503 | Worker 不可达 |
| 路径非法 | 400 | 禁止绝对路径和 `..` |
| 格式解析失败 | 422 | 返回具体行列 |
| 校验失败但可保存 | 200 | `validation.valid=false` |
