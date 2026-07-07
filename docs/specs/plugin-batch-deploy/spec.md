# 功能规格：FR-053 插件批量部署多服

> 状态：✅ accepted · 关联 PRD：FR-053 · 分支：当前 FR-053 验收分支

## 1. 背景与目标

FR-053 属于 MC 群组服 V2 的批量运维能力：在 FR-052 单服插件/模组管理、FR-045 制品库、FR-058 实例批量操作已经存在的基础上，补齐“从制品库选择插件，一次部署到多个实例”的闭环。

本轮目标是让平台管理员或具备实例运维权限的用户，可以从全局制品库中选择一个或多个 `type=plugin` 资产，勾选或筛选目标实例后批量部署到这些实例的 `plugins/` 目录，并获得每个实例的成功/失败结果汇总。Control Plane 只做编排和权限收敛，实际文件写入仍由 Worker 通过 gRPC 完成，保持三进程边界。

## 2. 需求（要什么）

- 范围内：
  - 从制品库选择一个或多个 `type=plugin` 的插件 jar。
  - 通过显式勾选 `ids[]` 或批量筛选 `filter` 选择目标实例集。
  - Control Plane 按权限收敛目标实例，越权或不存在的显式 ID 计入 `skipped`，不泄露存在性。
  - Control Plane 经各目标实例所属 Worker 扇出写入 `plugins/<asset.filename>`。
  - 返回每实例成功/失败 + 总体汇总；任一插件写入失败时该实例计失败，失败明细包含插件文件名与错误原因。
  - 前端提供批量部署入口、目标实例选择、危险操作二次确认、结果汇总与失败明细。
  - 写操作必须落审计，动作名为 `plugin.batchDeploy`。
  - i18n、主题、mock 模式与 DOM 测试同步覆盖。
- 范围外：
  - 首期不部署到 `mods/` 目录；模组批量部署后续另行扩展。
  - 不新增插件资产使用关系表，不追踪“实例安装了哪些资产”的持久化关系。
  - 不实现批量卸载、批量升级、版本回滚或插件依赖解析。
  - 不新增 Worker RPC，不做 Worker 端跨实例原子事务。
  - 不改变 FR-052 单服上传/删除/启停插件接口。

## 3. 设计（怎么做）

### 3.1 API 与目标选择

- 新增 `POST /api/v1/plugins/batch-deploy`。
- 请求体包含：
  - `assetIds`：一个或多个插件资产 ID。
  - `ids`：显式目标实例 ID 列表；或 `filter`：批量目标筛选条件。
- `ids` 与 `filter` 至少提供一种；两者同时提供时返回 400，避免目标语义不明确。
- `filter` 首期支持 `nodeId`、`status`、`role`；后端按传入 filter 执行，不在 API 层隐式改写角色。

### 3.2 资产校验

- 通过 `AssetService.GetByID` 读取资产。
- 每个资产必须满足：
  - `type=plugin`。
  - `filename` 是安全的普通文件名，不能包含路径分隔符或 `..`。
  - 文件名以 `.jar` 结尾。
- 通过 `AssetService.AbsPath` 获取本地 CAS 文件路径，并读取内容作为 Worker `WriteFile` 请求内容。
- 任一资产不合法时，整个请求在扇出前失败，按错误类型返回 400/404/409；不产生部分写入。

### 3.3 权限与目标收敛

- 复用现有 `AuthzService.AccessibleInstanceIDs` / `CanManageInstance` 的权限模型。
- 平台管理员可部署到全部实例。
- 非管理员仅能部署到其可管理范围内的实例。
- 显式 ID 模式下：请求数与实际可管理实例数差额计入 `skipped`。
- filter 模式下：查询天然只返回可管理实例，`skipped=0`。
- 单次目标数上限沿用 FR-058 风格，避免单请求过载。

### 3.4 扇出与结果语义

- Control Plane 对目标实例有界并发扇出，默认沿用 FR-058 的并发风格。
- 每个实例依次写入所有插件资产：
  - 全部成功：该实例计 `success`。
  - 任一插件失败：该实例计 `failed`，记录首个失败插件与错误；已写入的插件不回滚。
- 汇总按“实例”计数，不按“实例 × 插件”计数。
- 错误明细最多返回固定上限，避免超大响应。

### 3.5 Worker 交互

- 不新增 Worker RPC。
- 复用现有 `WriteFile`：
  - `InstanceUuid`：目标实例 UUID。
  - `Path`：`plugins/<filename>`。
  - `Content`：资产内容。
- Worker 仍负责实例工作目录内路径校验与实际写入。
- Control Plane 不直接访问实例工作目录，不绕过 Worker。

### 3.6 审计与危险操作保护

- 审计中间件新增 `plugin.batchDeploy` 的优先匹配，必须排在泛化 `POST && contains("/plugins") => plugin.deploy` 之前。
- 审计 detail 记录请求体，包含资产 ID 与目标选择条件；中间件仍按现有规则截断长 detail。
- 前端提交前使用危险操作确认：文案明确“将插件部署到 N 个实例”，避免误操作。

### 3.7 前端交互

- 入口放在运行时资产页 `RuntimeAssetsPage` 的插件资产展示处：源是全局制品库中的插件资产。
- 新增 `PluginBatchDeployDialog`：
  - 展示待部署插件名称。
  - 展示可选目标实例列表，用户显式搜索并勾选目标实例。
  - 支持二次确认。
  - 成功后展示 `success/failed/skipped` 汇总。
  - 失败时在逐实例结果中展示 `error` 明细。
- 前端 API hook 放在 `web/src/api/plugins.ts`。
- Mock handler 放在 `web/src/mocks/handlers/domains/plugin.ts`，与实例和资产 seed 联动。

## 4. 任务拆分

- [x] 文档合同：新增本 spec 与 API spec，经审核后同步 PRD / ARCHITECTURE / API / CHANGELOG。
- [x] 后端测试先行：补服务层批量部署、路由、审计测试。
- [x] 后端实现：资产校验、目标收敛、有界并发扇出、结果汇总、路由与审计动作。
- [x] 前端 API 与 mock：新增 hook、mock handler、错误注入支持。
- [x] 前端 UI：运行时资产页入口、批量部署对话框、目标选择、二次确认、结果展示。
- [x] 前端 DOM 测试：覆盖成功、二次确认、payload 与 mock 联动。
- [x] 证据链：单测、集测、单机断言截图、真实浏览器断言截图与验收报告。

## 5. 验收标准

- 单测：服务层能验证资产合法性、非 plugin 资产拒绝、越权/不存在目标计 `skipped`、Worker 未连接/写入失败计 `failed`、多插件按实例汇总。
- 集测：路由 `POST /api/v1/plugins/batch-deploy` 能绑定请求、执行权限收敛、返回汇总；审计动作记录为 `plugin.batchDeploy` 而不是 `plugin.deploy`。
- 单机断言：在本地 mock/测试环境执行批量部署，断言 mock 实例插件集合发生联动，保存结果页或断言输出截图。
- 真实浏览器断言：用真实浏览器打开本地 mock 前端，从运行时资产页选择插件，打开批量部署对话框，勾选多个实例，完成二次确认并看到成功/部分失败汇总，保存截图。
- 文档：`docs/specs/plugin-batch-deploy/spec.md`、`api.md`、`docs/API.md`、`docs/ARCHITECTURE.md` 与实现一致；PRD FR-053 仅在完整证据链通过后进入后续 release 标记流程。

## 6. 风险 / 待定

- 首期不做持久化安装关系，因此部署后只能通过实例插件目录读取实际状态，不能在资产页精确展示“已部署到哪些实例”。
- `WriteFile` 当前不是多文件原子事务；同一实例多插件部署中途失败不会回滚已写入插件。首期以失败明细暴露，后续如需强一致性可新增 Worker 原子部署能力。
- 大批量部署会产生较高网络与 Worker IO 压力，需要有目标数上限、有界并发和错误明细截断。
- 如果后续要支持 `mods/`、插件版本治理或批量回滚，需要扩展 API 与数据模型，不在本 FR 首期范围内。
