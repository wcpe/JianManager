# 功能规格：实例列表页重设计（1000+ 实例）

> 状态：开发中（已通过审核，首批实现落地）　·　关联 PRD：FR-133、FR-137、FR-235、FR-244　·　依赖：FR-247

## 1. 背景与目标

FR-247 已提供实例服务端分页搜索与聚合地基，但前端实例页仍主要依赖 `GET /instances` 全量数组。FR-235 要面向 1000+ 实例重设计列表页；FR-137 要补搜索/排序/筛选吸顶/分组单表；FR-133 要处理实例导航搜索与 a11y；FR-244 要统一动画体验。

## 2. 需求

- `/instances` 首屏不再全量拉取 1000+ 实例。
- 搜索、筛选、排序、分页状态进入 URL。
- 卡片视图与列表视图都使用服务端分页数据。
- 分组视图采用单表或分组虚拟渲染，不重复整套表头。
- 筛选工具条 sticky 且可折叠。
- 返回/前进恢复筛选、排序、视图与滚动位置。
- 动画使用统一 token，不造成侧栏或主工作区掉帧。
- 移动端不横向翻屏。

范围外：

- 恢复 1000+ 常驻侧栏实例树。
- 前端假分页。
- 新增虚拟列表第三方依赖。

## 3. 设计

### 3.1 数据源

实例列表页使用：

- `GET /api/v1/instances/search`
- `GET /api/v1/instances/aggregate`

旧 `useInstances()` 保留给尚未迁移的页面，但 `/instances` 页面不再依赖它作为主数据源。

mock 模式必须同步补齐同名 MSW 端点：

- `GET /instances/search`：支持 `q/status/nodeId/role/networkId/env/tag/sort/order/page/pageSize`，返回 `{items,total,page,pageSize}`。
- `GET /instances/aggregate`：与后端聚合结构一致，至少返回 `total/byStatus/byNode/byRole`。
- `/instances` 页面 Playwright 验收需拦截网络请求，断言首屏不再请求裸数组 `GET /instances`。

### 3.2 URL 状态

URL query 草案：

```text
?q=hub&status=RUNNING&nodeId=1&role=backend&networkId=2&env=prod&tag=survival&sort=name&order=asc&page=1&pageSize=50&view=card&groupBy=node
```

### 3.3 FR-133 边界

根据 ADR-055，侧栏不承载 1000+ 常驻实例树。FR-133 调整为：

- 提供轻量实例搜索入口或当前实例导航辅助。
- 保留空态 CTA 和 a11y。
- 不在侧栏渲染全量实例树。

### 3.4 动画

FR-244 增量目标：

- 统一 `--motion-*` token。
- 路由切换、筛选折叠、Dialog、侧栏、移动抽屉使用同一缓动/时长。
- benchmark 断言动画不触发主内容逐帧 resize。

## 4. 任务拆分

- [x] 新增实例 search/aggregate 前端 hook 与单测。
- [x] 补齐 MSW `instances/search`、`instances/aggregate` 假后端和 mock 单测。
- [x] `InstancesPage` URL 状态编解码测试（q/status/view/groupBy/sort/order/page/pageSize 已覆盖，page 深链会下发到分页搜索请求）。
- [x] 迁移列表主数据源到服务端分页。
- [x] 分组/卡片/表格虚拟渲染修正（卡片、平铺表格与分组表格均已虚拟化；分组列表使用单表 + sticky 组头，不再重复整套表头）。
- [x] 滚动位置恢复。
- [x] 轻量实例搜索入口与 a11y（实例分组树搜索/键盘/a11y/虚拟渲染已落地；空态 CTA 与侧栏服务器分组激活态已补）。
- [x] 动画 token 收敛与 benchmark（motion token + 路由/侧栏/进度条/抽屉主链路已接入；Playwright benchmark 与截图验收已覆盖）。
- [x] 文档同步：PRD、ARCHITECTURE、CHANGELOG 与 console/instance-list spec 已同步。

## 5. 验收标准

- 1200 mock 实例下 `/instances` 首屏只请求分页数据。
- 1200 mock 实例下 `/instances` 首屏不请求裸数组 `GET /instances`，只请求 `GET /instances/search` 与聚合端点。
- DOM 渲染项数量低于可视窗口 + overscan。
- URL 深链刷新后恢复筛选/排序/视图。
- 鼠标侧键从详情回列表恢复滚动位置。
- Playwright benchmark 截图覆盖移动端和桌面端，无横向翻屏。
- 亮/暗色动画与布局正常。

## 6. 风险 / 待定

- FR-133 的调整范围需要用户审核。
- 分组视图与服务端分页存在语义冲突：全量分组需要聚合接口，当前页分组只代表当前页；实现时需在 UI 明示或补后端分组分页。
