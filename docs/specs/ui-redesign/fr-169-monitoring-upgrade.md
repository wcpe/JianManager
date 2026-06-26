# 功能规格：FR-169 监控页升级（仅前端）

> 状态：草拟（待审核）　·　关联 PRD：FR-169　·　依赖：FR-163（已落 master）、FR-060/061（已交付）　·　批 2 / worktree W2

## 1. 背景与目标
把平台/节点/实例监控页升级为统一「监控仪表骨架」（design §4.2）：6 指标 + 每图独立时间筛选 + brush 拖拽轴 + hover 浮窗 + 实时。**仅前端**——用现有 FR-060/061 时序指标数据，不改后端。P1。

## 2. 需求（要什么）
### 范围内
- **监控骨架组件**（平台/节点/实例共用）：6 指标图 = 资源使用率、负载(1/5/15)、CPU、内存、磁盘 IO、网络 IO。
- **每图独立时间筛选**（昨天/今天/最近七天/自定义）+ 底部 **brush 拖拽轴**（拖选时间窗，联动该图）。
- **hover 浮窗**：该时刻各指标值（多序列）。靛蓝圆角风 + 双主题（FR-164 落地后自动跟色，本 FR 只用 token）。
- **历史 + 实时并存**：实时用现有指标流（SSE/轮询，复用既有）。
- 换指标维度：节点监控复用 `NodeInstanceCompare`；实例监控 = TPS/MSPT/堆/线程/玩家/区块（均现有 FR-060 指标）。

### 不做（范围外，明确推迟）
- **hover 进程 TOP10 tooltip（PID/进程名/占用/启动用户/cmd）**：需后端**进程粒度采集**（Worker 采每进程 + CP 存储/接口），属跨模块后端工作 → **拆为独立后端 FR（backlog，待登记，暂记 FR-170 候选）**，本 FR 不做、不留占位字段。
- 「按时间点查进程明细」底部面板：同上，依赖进程粒度后端，推迟。
- 任何后端 / proto / 数据模型改动。

## 3. 设计（怎么做）
- 复用 `components/charts/TimeSeriesChart` + `RangePicker`；新增 **brush 轴**（recharts Brush 或自绘拖拽选区）与 **hover 浮窗**（最近点 lookup）。
- 监控页（平台/节点/实例）套统一骨架组件 `MonitorSkeleton`（6 图网格 + per-chart 时间筛选 + brush + hover）。
- 纯逻辑下沉可测：brush 选区 → 时间 range（`lib/brush.ts`）、hover x → 最近样本点 lookup（`lib/chart-hover.ts`）、6 指标定义表。
- 实时：复用既有指标 SSE/轮询 hook；历史/实时切换为视图状态。
- 视觉：靛蓝圆角 + 双主题（仅用 `--chart-*`/`--primary`/`--status-*` token，不硬编码色）。

## 4. 任务拆分
- [ ] 测试先行：`lib/brush.ts`（选区→range）、`lib/chart-hover.ts`（x→最近点）纯函数 红→绿
- [ ] `MonitorSkeleton` 6 指标网格 + per-chart RangePicker
- [ ] brush 拖拽轴（联动该图时间窗）
- [ ] hover 浮窗（该时刻各指标值）
- [ ] 实时/历史并存（复用现有指标流）
- [ ] 平台/节点/实例三页套骨架
- [ ] PRD FR-169 → 开发中；CHANGELOG 追加；doc-sync
- [ ] tsc/lint/vitest/build 全绿 + 真机（6 图/筛选/brush/hover/实时）

## 5. 验收标准
- 平台/节点/实例监控页 6 指标图齐全；每图独立时间筛选生效；底部 brush 拖拽改时间窗联动该图。
- hover 浮窗显该时刻各指标值；历史 + 实时并存可切。
- 双主题下图表跟主色变；i18n 中/英。
- 自动化：brush/hover 纯函数 vitest 绿 + tsc/lint/build 绿。**真机由用户确认**。
- **进程 TOP10 明确不在本 FR**（已记 backlog 待登记后端 FR）。

## 6. 风险 / 待定
- 进程粒度推迟需用户认可（PRD FR-169 原文含「进程 TOP10」；本 FR 按用户「仅前端」决定收窄，进程粒度另立后端 FR）。**落地前在 PRD FR-169 备注「进程粒度拆 FR-170」或经用户确认调整 FR-169 描述。**
- brush 实现选型（recharts Brush vs 自绘）：优先 recharts 内置，过重再自绘。
