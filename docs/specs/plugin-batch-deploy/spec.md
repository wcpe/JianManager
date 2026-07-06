# 功能规格：插件批量部署多服

> 状态：实现中　·　关联 PRD：FR-053　·　关联 ADR：ADR-011

## 1. 背景与目标

FR-052 已有单服插件管理，FR-058 已有实例批量操作。FR-053 需要从制品库选择一个或多个插件，批量部署到多个实例，并返回逐实例成功/失败汇总。

## 2. 需求

- 从制品库选择 `type=plugin` 的一个或多个资产。
- 目标实例可由显式 id 列表或筛选条件确定。
- CP 按用户权限过滤目标实例。
- 经 Worker 文件能力把插件写入目标实例 `plugins/` 或 `mods/`。
- 返回每实例、每插件的结果和汇总。
- 危险操作二次确认，写审计。

范围外：

- 插件依赖解析。
- 自动热重载。
- 部署失败自动回滚。
- 批任务中心持久化进度。

## 3. 设计

### 3.1 API 草案

`POST /api/v1/plugins/batch-deploy`

请求：

```json
{
  "assetIds": [1, 2],
  "target": { "ids": [10, 11], "filter": { "status": "STOPPED", "role": "backend" } },
  "destination": "plugins",
  "overwrite": false
}
```

响应：

```json
{
  "requestedInstances": 2,
  "requestedAssets": 2,
  "succeeded": 3,
  "failed": 1,
  "results": [
    { "instanceId": 10, "assetId": 1, "ok": true },
    { "instanceId": 11, "assetId": 2, "ok": false, "error": "文件已存在" }
  ]
}
```

### 3.2 并发与权限

- 首版单次目标上限默认 500 个实例；超过上限直接拒绝并提示拆分批次。
- CP 有界并发扇出，默认复用 FR-058 的并发上限。
- 单次请求同步返回最终汇总，不持久化批任务进度；异步任务中心接入留到后续扩展。
- 越权或不可见实例不报存在性，计入 skipped。

## 4. 任务拆分

- [x] 后端目标解析和权限过滤测试。
- [x] 目标上限、同步超时和 skipped 语义测试。
- [x] 插件资产读取与 Worker 写入编排。
- [x] router 与审计动作。
- [x] 前端批量部署对话框。
- [x] DOM/e2e 验收结果汇总（DOM 已覆盖选择插件、目标实例和结果汇总；真浏览器截图见 `.tmp/evidence/fr053-172/`）。
- [x] 文档同步：API。

## 5. 验收标准

- 单测覆盖部分失败、越权 skipped、同名文件拒绝/覆盖。
- 集测覆盖 fake Worker 写入成功和失败。
- 浏览器截图覆盖选择插件、选择目标实例、结果汇总。

## 6. 风险 / 待定

- `mods/` 与 `plugins/` 目标如何自动判断需审核；草案默认用户显式选择。
