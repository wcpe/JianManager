# API Spec — FR-071 配置管理资源管理器化 + 自动发现全部配置 + 收藏

> 关联 FR: FR-071 | 优先级: P1 | 状态: 🔨 in-progress | 依赖: FR-070（共享资源管理器组件）、FR-031（配置引擎）

## 概述

FR-071 把「配置」段从独立三栏 `ConfigEditor` 改为**复用 FR-070 `ResourceExplorer`**（左树右内容/编辑器 + 交互全集），并叠加配置专属能力：
- **自动发现** server 目录下**全部**实际配置文件（不限内置 schema 那 6 个），递归呈现；
- schema 文件保留**表单/文本双模式**（FR-031），非 schema 纯文本+高亮；
- **Ctrl+S 保存 + 配置版本**（FR-031 的版本/diff/回滚，写入走配置端点而非文件端点）；
- **跨文件一致性校验**（FR-031）保留；
- **收藏**（书签）常用配置快速访问。

后端**仅新增一个加性端点**用于「递归发现全部配置」，其余复用 FR-031 既有配置端点与 FR-070 文件端点。**不改 proto**（递归发现在 CP 服务层经既有 `Worker.ListFiles` gRPC 走树遍历实现）。

## REST API

### GET /api/v1/instances/:id/configs/discover  （新增）
- **描述**: 递归发现实例 server 工作目录下**全部**配置文件（按扩展名识别：properties/yml/yaml/toml/json/txt/conf），返回相对路径扁平列表，供「已发现配置」快速面板与收藏解析使用。
- **权限**: `instance.file`（与既有配置端点一致）
- **Query**: 无（始终从工作目录根递归）
- **实现**: CP `ConfigService.Discover` 经既有 `Worker.ListFiles` 逐目录广度遍历（不新增 gRPC / 不改 proto），用既有 `isConfigFile` 过滤；限制最大遍历目录数/深度，避免超大目录拖垮（默认深度 8、目录上限 2000，超限截断并标记 `truncated`）。
- **响应**:
```json
{
  "files": [
    { "path": "server.properties", "format": "properties", "supported": true },
    { "path": "plugins/Essentials/config.yml", "format": "yaml", "supported": false },
    { "path": "config/paper-global.yml", "format": "yaml", "supported": true }
  ],
  "truncated": false
}
```
`supported=true` 表示命中内置 schema（可走表单模式），否则仅文本模式。

### 复用既有端点（不改）

| 端点 | 来源 | 本 FR 用途 |
|---|---|---|
| `GET /instances/:id/files` | FR-008 | `ResourceExplorer` 树/列表浏览（呈现全部文件，含非配置） |
| `GET /instances/:id/files/read` | FR-008 | 打开非配置/通用文件文本 |
| `POST /instances/:id/files/rename` | FR-020 | 重命名 / 拖拽移动 |
| `DELETE /instances/:id/files` | FR-008 | 删除 |
| `POST /instances/:id/files/upload` | FR-008 | 上传 |
| `POST /instances/:id/files/archive` | FR-070 | 批量 zip 下载 |
| `GET /instances/:id/configs/read` | FR-031 | 读配置（原文 + 字段 + schema + 校验） |
| `POST /instances/:id/configs/write` | FR-031 | 文本模式保存（生成**配置版本**） |
| `POST /instances/:id/configs/write-fields` | FR-031 | 表单模式字段级补丁保存（保留注释，生成配置版本） |
| `POST /instances/:id/configs/cross-check` | FR-031 | 跨文件/跨实例一致性校验 |
| `GET /instances/:id/configs/versions/*file` | FR-031 | 配置版本列表 |
| `GET /instances/:id/configs/diff/*file` | FR-031 | 配置版本 diff |
| `POST /instances/:id/configs/rollback/*file` | FR-031 | 配置版本回滚 |

## 收藏（书签）

- **存储**: 前端 `localStorage`，键 `jm:config-favorites:<instanceId>`，值为相对路径数组。
- **理由**: 书签为「快速访问」UI 便利项，非业务数据；CP DB 归一所有权但为单一便利项增表违反 YAGNI/范围纪律。如后续需跨设备同步，登记新 FR 升级为后端存储。
- **不暴露 REST 端点**（纯前端能力）。

## 错误处理

`/configs/discover` 错误语义与既有配置端点一致：

| 场景 | HTTP | 说明 |
|---|---:|---|
| 实例不存在或无权访问 | 404 | 避免泄露资源存在性 |
| 节点离线 / Worker 不可达 | 422 | `BUSINESS_ERROR`（与既有 List 一致） |
| 路径越界 | 由 Worker `validatePath` 拒绝 | 遍历仅基于 Worker 返回的目录名，天然限定工作目录内 |
