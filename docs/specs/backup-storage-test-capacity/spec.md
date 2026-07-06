# 功能规格：备份存储测试连接与容量展示

> 状态：开发中　·　关联 PRD：FR-152

## 1. 背景与目标

备份存储后端当前可创建和删除，但运维无法在保存前/保存后确认连接是否可用，也无法看到每个后端已存备份数和占用容量。FR-152 补齐测试连接与容量展示。

## 2. 需求

- 创建/编辑表单可测试连接。
- 列表行可对已有后端测试连接。
- 列表展示每个存储后端备份数与已用空间。
- 测试失败展示明确原因，不泄露凭证明文。

范围外：

- 自动周期健康检查。
- 远程存储配额管理。
- 凭据明文展示。

## 3. 设计

### 3.1 API 草案

- `POST /api/v1/backup-storages/test`
  - 用未保存的表单内容测试。
  - 请求体同创建存储后端。
  - 响应：`{ "ok": true, "message": "连接成功", "latencyMs": 123 }`

- `POST /api/v1/backup-storages/:id/test`
  - 测试已保存后端。
  - 响应同上。
  - 成功或失败都更新该后端最近测试状态：`lastTestAt`、`lastTestOk`、`lastTestMessage`（错误消息需脱敏）。

- `GET /api/v1/backup-storages`
  - 响应加性字段：`backupCount`、`usedBytes`、`lastTestAt`、`lastTestOk`、`lastTestMessage`。

### 3.2 持久化语义

- `backupCount`、`usedBytes` 从 `backups` 表按 `storage_id` 聚合，不在 `backup_storages` 上冗余保存。
- `lastTestAt`、`lastTestOk`、`lastTestMessage` 需要持久化到 `backup_storages`，用于刷新后仍展示最近一次人工测试结果。
- `POST /backup-storages/test` 使用未保存表单内容，只返回临时测试结果，不创建存储后端，也不写任何 `lastTest*` 字段。
- `POST /backup-storages/:id/test` 使用已保存配置测试，并更新对应后端的 `lastTest*` 字段。

## 4. 任务拆分

- [x] BackupStorageService 测试连接抽象与单测（环境变量/类型校验、latencyMs、脱敏、S3 SigV4 `HEAD bucket`、WebDAV `OPTIONS`、SFTP SSH 握手短超时探测）。
- [x] `backup_storages` 新增最近测试状态字段、router 端点与错误码。
- [x] 列表容量聚合。
- [x] 前端表单/行测试按钮与容量列（DOM 覆盖未保存配置成功/失败反馈、行测试状态回写）。
- [~] 文档同步：API、CHANGELOG（API/PRD/spec/CHANGELOG 已同步短超时探测语义，真机验收后收口）。

## 5. 验收标准

- 单测覆盖凭据环境变量缺失、连接失败、容量聚合。
- 服务端测试连接需覆盖 S3、WebDAV、SFTP 的短超时探测，不泄露环境变量解析出的凭证明文。
- DOM 测试覆盖成功/失败反馈。
- 浏览器截图覆盖列表容量与测试连接状态：`.tmp/evidence/fr152-171-172/backup-storage-capacity-row-test.png`、`.tmp/evidence/fr152-171-172/backup-storage-draft-test.png`。

## 6. 风险 / 待定

- S3/SFTP/WebDAV 真机端点差异较大；单测用本地 HTTP/SSH 替身覆盖协议入口，真实 MinIO/SFTP/WebDAV 端点仍需在验收环境补截图与日志证据。
