# 功能规格：备份校验和与审计分页导出

> 状态：开发中　·　关联 PRD：FR-171、FR-172

## 1. 背景与目标

FR-171 要为备份归档增加完整性校验和，FR-172 要让审计日志支持服务端分页和导出。两者都是后端契约补齐，前端页面后续消费。

## 2. 需求

### FR-171

- 创建备份时计算归档 sha256。
- `model.Backup` 保存 `checksum` 和 `checksumAlgo`。
- 备份列表与详情展示 checksum。
- 恢复前如能读取归档，应校验 checksum，不一致则拒绝恢复。

### FR-172

- `GET /audit` 在传入 `page` 或 `pageSize` 时进入分页模式，固定返回 `{items,total,page,pageSize}`。
- 兼容旧调用：未传 `page/pageSize` 时继续返回旧数组，`limit` 仅在旧数组模式生效；新前端必须总是传 `page/pageSize`，不得依赖旧数组模式。
- 新增导出端点，按相同过滤条件导出 NDJSON。
- 导出受权限限制，仅平台管理员可导出全量审计。

范围外：

- 增量备份链内容级校验。
- 审计日志异步大任务导出。
- 审计日志长期归档策略变更。

## 3. 设计

### 3.1 备份校验和

Worker 在备份归档完成后返回：

```json
{ "checksumAlgo": "sha256", "checksum": "<hex>" }
```

CP 保存到 `backups` 表。历史备份 checksum 为空时前端展示“未记录”。

### 3.2 审计分页

审计筛选参数沿用现有：

- `userId`
- `action`
- `targetType`
- `from`
- `to`

新增：

- `page` 默认 1
- `pageSize` 默认 50，上限 200

分页响应（仅当请求包含 `page` 或 `pageSize` 时返回）：

```json
{
  "items": [],
  "total": 123,
  "page": 1,
  "pageSize": 50
}
```

旧数组模式只为现有调用保留：请求不含 `page/pageSize` 时，响应仍是 `AuditLogInfo[]`；本 FR 的前端迁移完成后，页面侧一律使用分页模式。

### 3.3 审计导出

- `GET /api/v1/audit/export?format=ndjson`
- 每行一条审计日志 JSON。
- `format` 当前仅支持 `ndjson`，其它值返回 `400 UNSUPPORTED_FORMAT`。
- 与分页查询使用同一过滤器，但不受 `page/pageSize/limit` 限制；服务端按批次流式写出 NDJSON，避免大结果集一次性加载。
- 导出字段使用白名单；当前模型首批导出 `id`、`createdAt`、`userId`、`username`、`action`、`targetType`、`targetId`、`ip`，不导出 `detail`、`uuid`。
- `metadata` 默认不导出；如后续需要导出，必须按字段白名单脱敏密码、令牌、密钥、Cookie、Authorization、完整文件内容。
- 导出行为本身必须写入审计，记录导出人、过滤条件摘要、导出格式和成功/失败状态，不记录完整结果内容。

## 4. 任务拆分

- [x] 备份 proto/model/service/router 测试先行（Worker digest、CP 落库、恢复传参与 router 列表 checksum 字段均已覆盖；备份列表 checksum 浏览器截图已补）。
- [x] Worker 归档 sha256 计算。
- [~] CP 保存、恢复前校验和 API 展示（CP 保存与恢复传参已落地；远程恢复前校验真机待补）。
- [x] AuditService 分页查询和 total。
- [x] 前端 `useAuditLogs` 兼容旧数组并迁移审计页到分页模式（页面显示真实 total，导出请求剔除 page/pageSize/limit 的 DOM 验收已补）。
- [x] Audit export router、字段白名单、脱敏与导出审计测试（字段白名单、忽略分页、大结果流式、成功/失败 `audit.export` 审计均已覆盖）。
- [x] 前端 Backups/Audit 页面消费（备份 checksum 展示、审计分页/导出 DOM 验收与浏览器截图已补；截图见 `.tmp/evidence/fr152-171-172/backup-checksum-list.png`、`.tmp/evidence/fr152-171-172/audit-pagination-export.png`）。
- [~] 文档同步：API、ARCHITECTURE、CHANGELOG（API/PRD/spec/CHANGELOG/ARCHITECTURE 首批同步，远程恢复真机验收后最终收口）。

## 5. 验收标准

- 单测覆盖 checksum 计算、保存、恢复校验失败。
- 单测覆盖审计分页 total、pageSize 上限、过滤条件一致。
- 单测覆盖审计导出字段白名单、敏感字段脱敏和导出行为入审计。
- 集测覆盖 `/audit/export` NDJSON。
- 浏览器截图覆盖备份 checksum 展示和审计分页/导出按钮。

## 6. 风险 / 待定

- 旧数组模式为兼容路径，新增 UI 只走分页 envelope；后续版本可另立 FR 移除旧数组返回。
- 远程备份恢复前校验需要先拉回本地，需避免大文件双读。
