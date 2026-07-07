# 实现说明 — FR-053 插件批量部署多服

> 状态：已完成实现与验收证据采集；PRD 已按当前验收结论标记为 `✅ 已交付@v0.13.0`。

## 1. 实现范围

- 新增 `POST /api/v1/plugins/batch-deploy`。
- 从制品库读取 `type=plugin` 资产，校验安全文件名与 `.jar` 后缀。
- 按显式 `ids` 或 `filter` 解析目标实例，并按权限收敛。
- Control Plane 复用 Worker `WriteFile`，写入固定路径 `plugins/<filename>`，不新增 Worker RPC。
- 汇总按“实例”计数：`total/success/failed/skipped`。
- 审计中间件识别该接口为 `plugin.batchDeploy`，目标类型为 `plugin/batch-deploy`。
- 前端入口位于 `RuntimeAssetsPage` 的插件制品行，提供插件勾选、实例搜索/勾选、危险确认与结果汇总。
- mock 假后端支持 `/plugins/batch-deploy`，会把部署结果联动写入 mock `plugins` 集合。

## 2. 后端文件

- `internal/controlplane/service/plugin.go`
  - `PluginBatchDeployRequest`
  - `PluginBatchDeployResult`
  - `PluginBatchDeployInstanceResult`
  - `PluginService.BatchDeploy`
  - 插件资产加载、目标解析、有界并发扇出与逐实例结果聚合。
- `internal/controlplane/router/plugin.go`
  - 注册 `POST /plugins/batch-deploy`。
  - 映射自定义错误到 400/404/409/500 等状态。
- `internal/controlplane/middleware/audit.go`
  - `determineAction` 返回 `plugin.batchDeploy`。
  - `determineTargetType` 返回 `("plugin", "batch-deploy")`。
- `internal/controlplane/router/testhelper_test.go`
  - 测试路由注入 `PluginService` 与临时数据根上的 `AssetService`。

## 3. 前端文件

- `web/src/api/plugins.ts`
  - 新增 `PluginBatchDeployRequest/Result` 类型与 `useBatchDeployPlugins` mutation。
- `web/src/pages/RuntimeAssetsPage.tsx`
  - 在 `type=plugin` 制品行显示“批量部署”。
  - 弹窗支持插件选择、实例搜索、实例选择、二次确认与结果展示。
- `web/src/mocks/handlers/domains/plugin.ts`
  - 新增 mock `POST /plugins/batch-deploy`，校验资产类型与目标，联动写入 mock 插件集合。
- `web/src/i18n/zh.json`、`web/src/i18n/en.json`
  - 新增 `plugins.batchDeploy.*` 文案。
- `web/src/pages/RuntimeAssetsPage.dom.test.tsx`
  - 新增批量部署 DOM 用例，断言弹窗、二次确认、结果汇总与 mock 集合联动。

## 4. 验收命令

```bash
go test ./internal/controlplane/service ./internal/controlplane/router ./internal/controlplane/middleware
npm --prefix web run test:dom -- PluginManager.dom.test.tsx RuntimeAssetsPage.dom.test.tsx
npm --prefix web run build
```

日志位置：

- `.tmp/acceptance/fr053/go-test.log`
- `.tmp/acceptance/fr053/web-dom-test.log`
- `.tmp/acceptance/fr053/web-build.log`

## 5. 浏览器证据

真实浏览器 mock 环境完成端到端断言：

- `POST /api/v1/plugins/batch-deploy` 返回 200。
- 结果区展示 `结果：成功 2 / 跳过 0 / 失败 0`。
- 逐实例结果展示 `survival-1: 已部署` 与 `lobby-proxy: 已部署`。
- 文案明确只写 `plugins/`，不部署 `mods/`。

证据位置：`.tmp/e2e/fr053/browser-evidence.md` 与同目录截图。

## 6. 已知边界

- 首期不持久化“制品已部署到哪些实例”的资产引用关系。
- 同一实例多插件部署不是 Worker 端原子事务；单插件失败时按实例计失败，不回滚已经成功写入的插件。
- 前端首期提供显式实例选择，不在 UI 首屏暴露 `filter` 批量筛选提交；API 与后端已支持 `filter`。
