# 功能规格：压测命令模板与创建向导前端

> 状态：已审核（2026-07-20）　·　关联 PRD：FR-371　·　计划分支：feature/fr-371-bot-load-wizard
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 依赖：FR-362/363 开发中、FR-370 共享 API 契约已冻结但实现仍为计划；FR-371 实现须等待所需 API 可用　·　可与 FR-372 的非依赖部分并行

## 1. 背景与目标

当前 BotsPage 把舰队、压测会话、YAML textarea 和单 Bot 详情堆在一个页面。用户要“前端界面和功能完整”，且不应要求运营者手写 YAML 才能配置 500+ 分布式命令压测。

本 FR 重组 Bot 顶级信息架构，提供命令模板管理和五步创建向导；结构化表单是主入口，高级 YAML 只作为等价编辑方式。向导必须按目标节点、连接、命令编排、负载曲线、阈值预检五步组织，并在启动前展示节点分片、容量缺口、预计时长和调度风险。

## 2. 需求（要什么）

- `/bots` 增 URL 可寻址 tab：fleet/sessions/templates；旧链接默认 fleet。
- 模板页支持搜索、标签、创建、编辑、复制、删除和从预设创建。
- 创建/编辑模板使用结构化命令编排编辑器，可切高级 YAML；双向切换以服务端校验为准。
- 运行创建采用固定五步向导：目标节点、连接、命令编排、负载曲线、阈值预检。
- 支持命令增删/排序，并配置命令内容、发送间隔和执行顺序；默认预设为 `command-orchestration-v1`。
- 支持 stable/step/spike 配置和严格阈值默认值。
- 选择目标实例和发压节点后实时显示容量、分片和 blockers/warnings。
- 只有最近 preflight ready 且未过期才可启动。
- 支持“仅保存运行”“保存模板并运行”“从模板复制”。
- 5000 Mock Bot/大量模板下不全量铺开。
- 中英 i18n、暗亮主题、键盘、屏幕阅读器和错误恢复完整。

**范围内**：导航/tab/路由、模板 CRUD UI、五步向导、结构化命令编排编辑、YAML 高级模式、容量与调度预检、API hooks/devmock/tests/i18n/a11y。

**不做**：会话实时详情/图表/报告（FR-372）、后端语义改动、room/area/monster/tower 等玩法字段、ServerProbe 硬依赖、服务端权限/命令执行/业务效果验证、定时/CI 入口、在浏览器本地执行命令。

## 3. 设计（怎么做）

### 3.1 信息架构

保留 `BotsPage` 路由壳，内部按 URL `?tab=` 渲染：

- fleet：复用现有 Bot 聚合/分组/批量管理。
- sessions：运行列表、创建运行按钮、状态/类型/实例筛选；点击进入 `/bots/sessions/:id`（详情由 FR-372）。
- templates：模板列表和编辑。

`tab` 非法回退 fleet 并 replace URL；搜索/筛选也写 URL，刷新可恢复。

### 3.2 组件边界

建议新增：

```text
pages/bots/
  BotsShell.tsx
  BotFleetTab.tsx              # 从现 BotsPage 提取，不改行为
  BotLoadSessionsTab.tsx
  BotLoadTemplatesTab.tsx
components/bot-load/
  BotLoadWizard.tsx
  TemplateDialog.tsx
  CommandPlanEditor.tsx
  CommandStepEditor.tsx
  LoadProfileEditor.tsx
  ThresholdEditor.tsx
  CapacityPlan.tsx
  ScenarioYamlEditor.tsx
lib/bot-load/
  draft.ts
  validation.ts
  summaries.ts
  url-state.ts
```

不做上帝组件；每步独立组件，纯函数放 lib 并单测。

### 3.3 模板页

- TanStack Query：page/pageSize/q/tag 服务端分页。
- 列表显示名称、标签、命令计划摘要、profile 摘要、阈值摘要、更新时间。
- 创建：默认加载 `command-orchestration-v1` 客户端预设结构，再由后端保存。
- 复制：打开编辑 Dialog，名称默认“原名 - 副本”，提交 POST。
- 删除：DangerConfirm，scope=group 或按 bot:manage 既有门禁；后端仍最终鉴权。
- 编辑不影响历史运行，页面明确提示。

### 3.4 五步向导

#### Step 1 目标节点

- 目标实例 Combobox（服务端搜索/虚拟化复用现有组件）。
- 目标 Bot 数从 profile 派生并只读摘要。
- 发压节点默认自动选择，可切手动多选。
- 容量列表显示 ready/legacy/max/active/reserved/available/unavailableReason。
- 不展示节点 secret/内部地址。

#### Step 2 连接配置

- server、port、auth=offline、可选 MC version、namePrefix。
- offline 固定为首版可选；若 API 返回 microsoft 旧配置只读兼容，不在 V2 向导创建账号池。
- namePrefix 实时预览首/末 Bot 名，校验最终名称长度和唯一前缀风险。

#### Step 3 命令编排

- 默认加载 `command-orchestration-v1`，展示有序命令步骤列表。
- 每步配置命令内容、发送间隔与顺序；支持增删、复制和排序。
- 命令内容按 API 约束做必填、长度与类型校验，不解析具体服务器业务语义。
- 排序使用项目已有能力；若无现成拖拽依赖，用上下移动按钮，不引入新依赖。
- 未知 legacy action 只读 JSON，不允许在新模板中创建；room/area/monster/tower 等字段不进入通用结构化表单。
- 高级 YAML：复用 CodeMirror YAML；切回结构化前调用服务端校验，或本地仅做无损结构转换，不能静默丢字段。

#### Step 4 负载曲线

- stable：target/ramp/duration。
- step：可增删 stage，target 严格递增，图形预览使用现有 Recharts。
- spike：target/connect window/barrier/release window/hold。
- 显示预计总时长和连接速率摘要。

#### Step 5 阈值预检

- 默认严格阈值一键恢复，并显式编辑在线率、命令发送率、调度完成率、Worker 健康率、仅适用时的屏障到达率、schedule lag p95 与 crash 数；`minWorkerHealthRate` 默认 0.99。
- verdict 与 safety stop 分区，危险阈值给解释。
- 点击预检调用 API，展示 allocations 表、总容量、命令计划结构、warnings/blockers 和计划过期倒计时。
- 预检只确认目标实例作用域、执行节点容量、命令计划、负载曲线和阈值是否可调度；连接配置在创建运行快照时校验，不由 preflight 重复验证；也不验证命令执行结果或预期业务效果。
- ServerProbe 或可选业务观测缺失不得使通用预检失败。
- 任何前面字段变化立即作废 planToken。
- ready=false 时启动按钮禁用并聚焦 blockers。
- 启动成功跳 `/bots/sessions/:id?tab=overview`。

### 3.5 草稿状态

- 向导状态用局部 reducer/Zustand 临时 store，禁止每字段全局持久化。
- 未提交草稿可存 sessionStorage，key 按 new/templateId；关闭向导询问丢弃（共享 Dialog，不用 window.confirm）。
- planToken 不持久化到 localStorage，刷新后必须重预检。
- 模板编辑成功后清草稿。

### 3.6 API hooks

FR-370 共享 API 契约已冻结，但 `api/botLoad.ts` 的共享类型和运行基础 hooks 尚未实现；本 FR 在依赖可用后消费并追加模板/向导 hooks，不在页面内另定义一套类型：

- useBotLoadNodes
- useBotLoadTemplates/useBotLoadTemplate
- useCreate/Update/DeleteBotLoadTemplate
- useCreateBotLoadRun
- usePreflightBotLoadRun
- useStartBotLoadRun

Mutation 成功精确 invalidate；容量 query 仅向导打开时 5 秒轮询，关闭停。

### 3.7 错误与可访问性

- path 级 422 错误映射到字段/命令步骤卡并自动滚到首错。
- API 整体失败显示 ErrorState+重试，不伪装空列表。
- Stepper 使用 `aria-current=step`；命令步骤折叠有 aria-expanded；容量与计划变化用礼貌 aria-live。
- 所有 Label htmlFor/id 对齐；颜色状态同时有文字/图标。
- Dialog 使用共享 ScrollableDialog 壳，移动端可滚动。
- 键盘可完成新增命令步骤、排序、切步骤、预检和启动。

### 3.8 i18n 与主题

- 新 key 全量中英；行为名/错误码/状态不直接显示后端原始值，未知值回退“未知（raw）”。
- 图表/状态色使用语义 token，不硬编码只适合亮色的颜色。
- YAML/代码编辑器暗色跟主题切换。

### 3.9 devmock

扩展 bots handler：

- 模板 CRUD 有状态。
- load-nodes 至少 12 节点，含 ready/legacy/offline/capacity不足。
- preflight 可按目标数生成分片；支持注入 capacity/command-plan/422/503 错误，不以业务效果或 ServerProbe 状态作为 ready 条件。
- start 返回运行并交 FR-372 动态模拟。
- 提供 5000 Bot seed 生成器，但默认测试只生成按需数量，避免每次 E2E 变慢。

## 4. 任务拆分

- [ ] 测试先行：URL tab/filter 状态、draft reducer、命令计划/profile/阈值纯校验。
- [ ] 提取现有 fleet UI，保持现有测试/E2E 不回归。
- [ ] API 类型/hooks 与 devmock 模板/容量/预检。
- [ ] 模板列表和 TemplateDialog/复制/删除，默认预设使用 `command-orchestration-v1`。
- [ ] 实现目标节点/连接/命令编排/负载曲线/阈值预检五步 BotLoadWizard。
- [ ] CommandPlanEditor/CommandStepEditor/YAML 双模式与无损切换。
- [ ] CapacityPlan、planToken 失效/倒计时、blocker 导航，覆盖无 ServerProbe 环境。
- [ ] 中英 i18n、双主题、响应式、a11y。
- [ ] Vitest DOM + Playwright + 5000 Mock 性能断言。
- [ ] 文档同步：ARCHITECTURE 前端 IA、API 类型、PRD 本 FR 状态、CHANGELOG。

## 5. 验收标准

### 自动化/浏览器

- [ ] `/bots?tab=fleet|sessions|templates` 可深链、刷新恢复、非法值回退。
- [ ] 既有 Bot 舰队聚合、单 Bot详情、批量操作 E2E 全绿。
- [ ] 无需 YAML 可从 `command-orchestration-v1` 配置命令内容、发送间隔和执行顺序。
- [ ] YAML→结构化→YAML 往返不丢支持字段；非法 path 错误准确定位。
- [ ] stable/step/spike 均可配置，预计时长和目标数准确。
- [ ] 容量不足、legacy/offline 节点和非法命令计划在启动前明确阻断；无 ServerProbe 环境仍可通过通用预检。
- [ ] 预检不请求或推断服务端权限、命令执行结果及业务效果。
- [ ] 任一字段变化后旧 planToken 失效，必须重新预检。
- [ ] 5000 Mock Bot/100模板下模板/Bot请求 pageSize≤100、首屏数据 DOM 行≤120、节点容量轮询仅向导打开时存在，关闭向导后请求停止。
- [ ] 中英切换、暗亮主题、移动端、键盘和 aria 检查全绿。
- [ ] `pnpm` 相关 typecheck/lint/vitest/Playwright 全绿，不新增依赖。

### 真机

- [ ] 真实目标节点、执行节点容量和分片与 API/Worker 一致。
- [ ] 从 `command-orchestration-v1` 创建模板→创建运行→预检 500→启动成功跳详情。
- [ ] 在未部署 ServerProbe 的真实环境中，合法命令计划可完成预检与启动。
- [ ] 容量或命令计划变化后 plan 过期提示准确，预检结果不宣称命令已被服务器执行。
- [ ] 真浏览器按目标节点/连接/命令编排/负载曲线/阈值预检完成全流程，用户确认字段、文案和风险提示可理解。

## 6. 风险 / 待定

- 现 BotsPage 体量大，提取只为本 FR IA，不顺手重构无关 Bot UI。
- 若项目无拖拽依赖，使用上下移动按钮，禁止新增依赖。
- YAML 服务端校验需要临时 validate API 时，应先回共享 API 审核；首选创建/预检返回 path 错误，不临时发明端点。
- 预检结果只表示计划可调度；不得将命令发送成功或任何可选业务观测提前解释为业务效果验证。
- 大数据性能以 DOM/请求数量和 Playwright 时序为证，不用主观截图代替。
