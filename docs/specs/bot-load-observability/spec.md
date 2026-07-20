# 功能规格：实时命令压测观测、故障诊断与报告前端

> 状态：已审核（2026-07-20）　·　关联 PRD：FR-357　·　计划分支：feature/fr-357-bot-load-observability
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 命令成功边界：`../../adr/075-bot-command-orchestration.md`
> 依赖：FR-355 API 已落　·　可与 FR-356 并行

## 1. 背景与目标

现有压测会话只在 BotsPage 中展示状态、connected/total 和 YAML，列表/详情不持续轮询，也没有负载阶段、命令计划进度、调度 lag、失败分类、阈值 verdict 或报告。500+ 压测不能依赖逐 Bot SSE 或人工刷新。

本 FR 建独立运行详情页，用一条会话级聚合 SSE 驱动实时概览，Bot/失败/历史指标按需分页；用户可从 verdict 下钻到发压 Worker、Bot、命令步骤和错误，并重试失败子集或导出报告。ServerProbe 等业务观测只能作为可选扩展，不进入通用失败链必需条件。

## 2. 需求（要什么）

- 新路由 `/bots/sessions/:id`，tab 可寻址：overview/bots/metrics/failures/events/config。
- 顶部持续显示运行名、状态、verdict、阶段、时长、目标实例和主要操作。
- 一条会话级 SSE；断线重连、Last-Event-ID 和快照补偿。
- 实时概览展示连接进度、命令计划发送成功/失败、节点分布、屏障、阈值判定和警告。
- 性能页展示连接延迟、命令调度 lag p50/p95/p99、命令发送结果和 Worker RSS/eventLoop/CPU/健康状态。
- TPS/MSPT/ServerProbe 数据仅作为 optional legacy 扩展，有值时独立展示，缺失不降级通用观测。
- Bot 明细服务端分页/虚拟化，支持状态、命令步骤、节点和错误筛选。
- 失败页展示分类、明细、通用关联事件链和只重试失败子集；失败链不要求 Probe 事件。
- events 页展示会话级关键事件，不铺全量普通命令发送事件。
- config 页只读展示运行快照、分片、命令计划 YAML/结构化摘要和阈值。
- 支持 stop/cancel、报告 JSON/CSV 下载，页面与报告持续显示 `bot.chat` 成功边界免责声明。
- 运行终态后停止实时连接，历史页面仍完整可用。
- 中英、双主题、响应式、键盘、屏幕阅读器完整。

**范围内**：详情路由/页面、SSE 管理、API hooks、图表、分页/虚拟化、失败重试、报告下载、devmock 动态运行、测试和文档。

**不做**：模板/创建向导（FR-356）、后端状态机/指标语义、浏览器端重新计算正式 verdict、为每 Bot 常驻 SSE、原始网络包/每次挥击可视化。

## 3. 设计（怎么做）

### 3.1 页面壳

`BotLoadSessionPage` 只负责：读取 route id/tab、加载 run 快照、挂 SessionEventProvider、渲染 header/tab。各 tab 独立懒挂载，避免一个上帝组件。

```text
components/bot-load/session/
  SessionHeader.tsx
  SessionOverview.tsx
  SessionBots.tsx
  SessionMetrics.tsx
  SessionFailures.tsx
  SessionEvents.tsx
  SessionConfig.tsx
  SessionEventProvider.tsx
  ThresholdVerdict.tsx
  ConnectionFunnel.tsx
  ExecutorDistribution.tsx
  FailureTraceDrawer.tsx
lib/bot-load/
  session-events.ts
  metrics.ts
  filters.ts
  report.ts
```

### 3.2 数据分层

- 快照：`GET /bots/stress-sessions/:id`，TanStack Query，页面进入必取。
- 实时：单条 SSE 更新小型 session store；不把每个 metric 事件都写全局 Zustand 持久化。
- 历史指标：metrics API 按可视时间范围/分辨率查询。
- Bot/失败：各自服务端分页，pageSize≤100。
- 报告：终态按需下载，不预取大 CSV。

SSE event 到达后：

- run-state/counts/stage 更新内存快照并 invalidate 相关轻查询。
- metric 只追加最近窗口（默认 15 分钟，最多 300 点）；完整历史仍查 HTTP。
- failure 增量更新摘要并 invalidate failures 首页面。
- complete 设置 reportReady、关闭 SSE、刷新详情/报告摘要。

单 run SSE、Bot/失败/事件分页与 report 接口保持现有契约；不得为 optional legacy 观测另建通用必需链路。

### 3.3 SSE 生命周期

实现独立 `BotLoadEventClient`，形态参考现有 useBotEvents 但会话级：

- 建连前 `ensureFreshToken`。
- 使用 fetch ReadableStream 解析 SSE，支持 event/id/data/retry。
- 保存内存 lastEventId；重连带 `Last-Event-ID` header（fetch 支持）。
- 退避 1s→2s→5s→10s，最大10s；页面 hidden 超60秒可断开，visible 时先刷新快照再连。
- 401 先刷新 token；404 停止；503 展示可重试状态。
- 组件卸载/登出 abort；禁止多个 tab 重复连接同一 run，可用模块级按 runId 单例订阅管理器，引用计数归零即关。
- 收到 complete 后永久关闭，除非详情快照显示又回非终态（正常不应发生）。

### 3.4 Header 操作

- running/degraded：显示“有序停止”和“立即取消”；二者用共享确认 Dialog，说明差异。
- stopping/cancelling：按钮 loading/禁重复。
- completed/failed/cancelled：下载 JSON/CSV、复制为新运行（跳 FR-356 向导，带 template/run snapshot 参数）。
- verdict 使用文字+图标+语义色，不只颜色。
- 运行时长前端按 startedAt 轻量 tick，但不写全局状态；终态用 endedAt 固定。

### 3.5 Overview

- KPI：目标、已接受、在线、失败、在线率、命令发送成功/失败、当前阶段、运行时长。
- ConnectionFunnel：planned→accepted→connecting→connected，显示每层转化率。
- CommandPlanProgress：按命令步骤显示 planned/sent/failed，明确 sent 只表示 `bot.chat` 未同步抛错。
- BarrierStatus：显示屏障等待、到达、释放和超时状态。
- ThresholdVerdict：逐指标 expected/actual/pass/pending；正式结果完全使用后端 reasons，不在前端另算。
- ExecutorDistribution：每节点 planned/active/error/health/RSS/eventLoop，点击带 node filter 跳 Bots/Failures。
- 最近 warnings/failures 最多 10 条，并常驻展示命令发送成功免责声明。

### 3.6 Metrics

图表复用 `@jianmanager/ui`/Recharts：

- 连接：online rate、connect latency p50/p95/p99。
- 命令：调度 lag p50/p95/p99、发送成功/失败率；成功文案持续链接或展开 `bot.chat` 成功边界免责声明。
- 屏障：等待数、到达数、释放/超时状态与阶段时间线。
- 发压端：按节点 health/RSS/eventLoop/CPU/activeBots，可多选最多 6 节点，超过提示。
- optional legacy：TPS、MSPT、在线玩家及 ServerProbe 扩展指标仅在有值时置于独立区域，不参与通用命令成功判定。
- 时间范围：最近15m/1h/全程；分辨率由服务端返回。
- null 显“无数据”；optional legacy 缺失不显示错误，也不画0。
- tooltip 数值按量纲格式化；图例可键盘切换。

### 3.7 Bot 明细

- 过滤写 URL：q/status/node/step/error/page。
- 表格列：选择、名称、状态、执行节点、当前命令步骤、重连、最近状态、错误、操作。
- 全选当前页和“按当前筛选重试失败”语义分开，明确影响范围。
- 每页≤100；如果行组件复杂，使用项目已有虚拟化方式；不得新增依赖。
- 点击 Bot 打开详情 Drawer，可按需复用单 Bot SSE，但关闭即断，不同时开多条。

### 3.8 失败诊断

- 顶部分类卡 target/executor/network/command-plan/worker/internal；probe 仅作为 optional legacy 分类。
- 明细服务端分页；支持 errorCode/node/step/time。
- FailureTraceDrawer 显示：Bot→Worker→命令步骤→调度/发送结果→错误→是否可重试；有可选业务观测时才追加 Probe event。
- “重试当前项/选中项/当前筛选失败”调用 retry-failed；显示 requested/accepted/skipped/errors 明细，不只 toast 数量。
- 重试成功跳 Bot tab 过滤相应 Bot；历史 failure 不删除。

### 3.9 Events

只显示会话关键事件：run-state/stage/barrier/command-send/worker health/executor crash/safety stop。历史使用共享 API `GET /bots/stress-sessions/:id/events` 服务端分页，支持类型/Bot/节点/步骤/matchState/时间筛选；SSE 仅把实时新增插入首屏。高频命令发送事件显示聚合计数，不逐条刷屏；optional legacy Probe 事件独立标记。

FailureTraceDrawer 以 actionRunId/eventId 查询同一历史投影，运行结束、刷新或 SSE 重连后仍可还原 Bot→Worker→命令步骤→调度/发送错误链，不要求 Probe Event。

### 3.10 Config

- 只读显示 target、executor allocations、profile、command plan、thresholds 和连接摘要。
- YAML 使用 CodeMirror readonly；提供复制，复制失败有降级提示。
- 显示模板来源和“运行快照不随模板修改”说明。
- 敏感连接配置只显示 server/port/auth，不显示任何 token。

### 3.11 报告下载

- JSON：通过 API 下载 blob，文件名 `bot-load-<runUuid>.json`。
- CSV：同名 `.csv`，保持 UTF-8 BOM。
- 页面和 JSON/CSV 报告持续包含免责声明：命令发送成功仅表示 Bot Worker 调用 `bot.chat` 时未同步抛错，不证明服务器接受、权限通过或产生业务效果。
- 下载中 loading，失败显示后端 message；未终态按钮禁用并说明。
- 不把报告内容塞 localStorage。

### 3.12 devmock 动态模拟

- 启动后按虚拟时钟推进 pending→starting→running→completed。
- 逐步增加 connecting/connected，插入一个可配置失败峰。
- 生成 metric/failure/SSE；支持断流、401、503、Last-Event-ID 重连。
- 500/1000/5000 Bot 明细按查询即时生成，不预存巨大数组。

### 3.13 可访问性与主题

- tabs 使用正确 tablist/tab/tabpanel；URL切换后焦点管理。
- 图表提供可读摘要/数据表 fallback，不能只靠 SVG。
- 实时更新区域 `aria-live=polite` 只播报状态/完成，不播每秒计数。
- 错误/警告不只颜色；暗色对比满足现有 token 基线。
- 移动端表格横向滚动，Header 操作收进菜单但停止按钮可达。

## 4. 任务拆分

- [ ] 测试先行：SSE parser/client、重连、Last-Event-ID、单例引用计数。
- [ ] 路由/SessionPage 壳和 URL tab/filter。
- [ ] API hooks：详情、metrics、bots、failures、events历史、retry、report、`/stream` SSE。
- [ ] SessionHeader/Overview/Threshold/Funnel/命令计划/屏障/Executor。
- [ ] Metrics 图表覆盖连接、命令发送、调度 lag p50/p95/p99、Worker 健康及 optional legacy 区域。
- [ ] Bot 明细分页/选择/详情 Drawer。
- [ ] Failure 分类/Trace/重试结果明细，验证无 Probe 事件仍可还原失败链。
- [ ] Events/Config/报告下载，页面与报告持续显示 `bot.chat` 成功边界免责声明。
- [ ] devmock 动态运行和错误注入，覆盖无 ServerProbe 与可选 legacy 数据两种模式。
- [ ] 中英 i18n、双主题、响应式、键盘、a11y。
- [ ] Vitest DOM + Playwright 真流模拟 + 5000 数据性能断言。
- [ ] 文档同步：ARCHITECTURE 前端运行页、API、PRD 本 FR 状态、CHANGELOG。

## 5. 验收标准

### 自动化/浏览器

- [ ] 一页只建立一条 run SSE；切 tab 不重复，离页/登出关闭。
- [ ] SSE 断流/401/503 后按规则恢复，Last-Event-ID 或 init 快照补齐状态。
- [ ] 页面无需手刷持续更新连接、命令计划发送成功/失败、调度 lag、屏障、Worker 健康和阶段。
- [ ] 后端 complete 后 SSE 关闭、报告按钮可用、历史 tabs 正常。
- [ ] Overview verdict reasons 与 API逐项一致，前端不另算出冲突结论。
- [ ] Bot/Failure 5000 数据下每次请求 pageSize≤100、首屏 DOM 数据行≤120、切换 tab 不额外创建 SSE，跨页筛选和重试范围准确。
- [ ] retry-failed 展示逐项 errors，历史失败不被删除；失败链在无 Probe 事件时仍完整。
- [ ] 图表单序列点数≤1200、null 不画0，连接与调度 lag p50/p95/p99 单位正确；TPS/MSPT/ServerProbe 缺失不报错，有值时仅显示在 optional legacy 区域。
- [ ] 页面及 JSON/CSV 报告持续显示 `bot.chat` 成功边界免责声明，文件名、内容类型、loading/error 正确。
- [ ] 中英、暗亮、移动端、键盘、图表文字摘要和 aria 全绿。
- [ ] 相关 typecheck/lint/vitest/Playwright 全绿，不新增依赖。

### 真机

- [ ] 500 Bot 运行中页面持续更新连接、命令发送、调度 lag、屏障和 Worker 健康，浏览器资源稳定，无500条 SSE/无明显卡顿。
- [ ] 能从失败分类下钻到具体 Worker、Bot、命令步骤和调度/发送错误，不部署 ServerProbe 时链路仍完整。
- [ ] 发压 Worker 故障、网络连接失败、命令调度或发送失败在 UI 分类正确；Probe 超时仅在启用 optional legacy 时展示。
- [ ] 固定/阶梯/洪峰运行终态均可查看并导出完整报告，页面与报告均持续展示 `bot.chat` 成功边界免责声明。
- [ ] 用户确认创建后跳转、停止/取消、失败重试和报告流程可用。

## 6. 风险 / 待定

- 事件历史 API 未单列；首版 events tab 使用 SSE ring + 已持久化关键结果组合，禁止另发明接口。
- 浏览器性能瓶颈主要是图表和大表；限制点数/节点选择/DOM 行数，不用提高轮询频率解决。
- 现有单 Bot SSE 可按需复用，但 Drawer 关闭必须释放。
- 不新增虚拟列表、状态管理或图表依赖。
