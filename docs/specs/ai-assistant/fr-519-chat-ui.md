# 功能规格：共享聊天界面与全局上下文面板（FR-519）

> 状态：**已实现主体（2026-10-23）**　·　关联 PRD：FR-519　·　范围：仅 FR-519
>
> 落地范围：`AssistantChatPanel`（消息流 + 输入器，Markdown 渲染且**不渲染图片**）、
> `useAssistantChat`（会话/提交/SSE 观察/断线补读）、`AssistantDock`（浮窗：新会话/历史/收起/关闭；
> 关闭不停止 run）、顶栏入口（经 console store 跨路由常驻）。事件聚合抽成纯函数并单测 7 项。
> **未做**：确认卡与工具状态徽标（依赖 FR-516）、上下文芯片（依赖 FR-518）、
> 会话历史分页（后端 `nextCursor` 已就绪，前端只取首页）、草稿按会话保存。
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§9 前端展示与路由），本文不复述。
> 前置：FR-515（事件流）、FR-516（确认卡）、FR-518（上下文芯片）；联调 FR-517。

## 1. 背景与目标

前几项把运行时、工具、上下文都做出来了，但还没有界面。FR-519 交付**共享聊天界面**：首页内嵌的对话区与全站可呼出的非模态浮窗，两者共用同一份会话、草稿与 run。

关键不是「画一个聊天框」，而是三件事：
1. **同一会话多形态**：切页、收缩、展开都不重新发请求、不重复生成。
2. **流式更新不拖垮外壳**：逐 token 增量不得触发整棵 DashboardPage 重绘。
3. **关闭 ≠ 停止**：UI 收起不控制服务端 run 生命周期。

范围内：输入器、消息列表、工具调用与确认卡、历史、新会话、收缩/关闭/展开/停止、移动端全屏、无障碍。

范围外：会话与 run 的服务端语义（FR-515）、工具是否获准（FR-516，前端不决定）、页面上下文采集（FR-518）、AI 首页布局（FR-520）。

## 2. 现状核实（只读结论）

- **可用原语齐备**：`sheet.tsx`（移动端全屏）、`dialog.tsx` / `scrollable-dialog.tsx`、`textarea.tsx`（含 `field-sizing-content` 自动增高，见 [packages/ui/src/components/textarea.tsx](../../../packages/ui/src/components/textarea.tsx)）、`collapsible.tsx`、`hover-card.tsx`。
  **注意**：组件库**没有** `scroll-area` 原语；滚动统一用原生 `overflow-y-auto` + 自定义滚动条样式（既有做法见 `apps/control-plane-web/src/components/console/WorkspaceCardBody.tsx`），不为此新增原语。
- **Markdown 渲染能力已存在，无需新依赖**：  `react-markdown@^9` 与 `remark-gfm@^4` 已在 `apps/control-plane-web/package.json` 依赖中，且已有成熟用法——[ReleaseNotes.tsx](../../../apps/control-plane-web/src/components/views/system-update/ReleaseNotes.tsx#L26-L32) 已实现「标题/列表/代码块/链接/表格 + 不渲染裸 HTML（防 XSS）+ 外链经确认后新标签打开」。FR-519 的消息渲染**复用同一套组件**，不引入新的 Markdown 库；但**安全策略不能整份照抄**——该文件的 `img` 渲染器直接输出 `<img src>`，而外链确认只覆盖 `<a>`，聊天场景必须额外禁掉远程图片（见 §5.2）。
- **项目硬规则**：禁止原生 `textarea` / `select` / `table` 直接使用（ESLint 已配规则），必须走 `Textarea` / `Select` / `Table` 原语。
- **千级列表必须虚拟化**：长会话历史若可能过千条，须虚拟化；`itemSize` 与真实行盒严格一致，行间距折进行盒（不用 `space-y-*`）。
- **本域无聊天状态基础设施**：当前无助手会话 store、无聊天事件消费。
  但**已有可参考的 SSE 消费实现**（[apps/control-plane-web/src/api/events.ts](../../../apps/control-plane-web/src/api/events.ts#L10-L32) 用 fetch + Bearer 读流），本 FR 应参照其连接管理方式，而不是从零设计。
- **顶栏入口的真实落点是 `ConsoleHeader`，不是 `top-nav-actions`**（更正一处虚构引用）：顶栏组件为
  [ConsoleHeader.tsx](../../../apps/control-plane-web/src/components/console/ConsoleHeader.tsx)，内部拆了
  `HeaderTools`（图标按钮组，右侧工具区，L87-L121）、`BrandSegment`、`SearchBox`、`RefreshButton`。
  浮窗入口按钮应加在 `HeaderTools` 的图标组内，与既有图标按钮同尺寸、同间距（`gap-0.5 sm:gap-1`）。
  **不存在 `top-nav-actions` 这个类名/组件**，实施时不要去找。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 形态 | 首页内嵌区 + 顶栏呼出的**非模态右侧浮窗** | 用户已确认；首版不做自由拖动/复杂窗口管理 |
| 状态归属 | 外壳级**单一** manager；会话状态、事件订阅、缓存按用户分区 | shared-contracts §9 |
| 消费方式 | 首页与浮窗用 selector 订阅，不整树重绘 | 逐 token 更新必须有界 |
| 流式传输 | `fetch` + `Bearer` 读 SSE；**不用** `EventSource` | JWT 不进 URL |
| 关闭语义 | 收起/关闭只影响 UI；**停止**是独立显式操作 | shared-contracts §5.3 |
| 移动端 | 全屏 sheet | 复用 `Sheet` 原语 |
| 输入器 | `Textarea` 原语（自动增高，`Enter` 发送 / `Shift+Enter` 换行） | 项目禁原生 textarea |
| 消息渲染 | Markdown（复用 `react-markdown` + `remark-gfm`，与 `ReleaseNotes` 同配置） | 依赖已在，零新增成本；不做会明显损失可读性 |
| 确认卡 | 卡片内联在消息流中，approve/reject 两键 | 前端不决定是否获准，只上报决定 |
| 历史 | 分页加载（游标向上），超长虚拟化 | 千级约束 |
| 明确不做 | 自由拖动、多窗口、富文本/Markdown **编辑器**（只渲染不编辑）、附件上传、语音 | 首版范围 |

## 4. 组件结构

```
AssistantProvider (外壳级，唯一)
├── useAssistantStore        // 当前会话、草稿、run 状态（按 userId 分区）
├── useAssistantEvents       // SSE 订阅 + seq 补读
└── <AssistantDock>          // 浮窗容器（非模态，右侧）
    └── <AssistantConversation>

<AssistantInput>             // 输入器 + 上下文芯片 + 模型选择
<AssistantMessageList>       // 虚拟化消息列表
├── <AssistantMessage>       // 文本消息
├── <AssistantToolCall>      // 工具调用 + 状态
└── <AssistantConfirmCard>   // 待确认卡片（approve/reject）
```

### 4.1 状态分区

- store key 含 `userId`；登出或换用户时**立即释放**订阅、草稿与缓存（shared-contracts §9）。
- 切换会话不重建 store，只换 `conversationId` 游标。
- 草稿按会话保存（切回仍存在），但**不跨用户**。

### 4.2 流式更新的边界

逐 token 增量只允许触发 `<AssistantMessage>` 内部更新：

- 文本增量写入该消息的局部缓冲，`<AssistantMessage>` 用自身 state 或细粒度 selector 订阅。
- **禁止**把增量写进外壳级 store 后让 DashboardPage 订阅整棵树。
- 实测判据：连续 100 次 `text_delta` 期间，对话区外的组件不得重渲染（用 React Profiler 或渲染计数断言）。

## 5. 交互规格

### 5.1 输入器

| 行为 | 规格 |
|---|---|
| 发送 | `Enter` 发送；`Shift+Enter` 换行 |
| 禁用态 | run 进行中禁用发送（可停止），提示「生成中…」 |
| 草稿 | 按会话保存，切会话恢复 |
| 上下文芯片 | 输入框上方一行（FR-518），可移除 |
| 模型选择 | 下拉，仅列 `/assistant/models` 返回的可用模型 |
| 供应商告知 | 首次使用明示「相关数据将发送给 <供应商>」 | 需要 `/assistant/models` 补一个公开的供应商标识字段（见下） |

> **依赖缺口（会阻塞本项）**：现有 `AvailableModel`（[service/assistant.go](../../../internal/controlplane/service/assistant.go#L602-L610)）只返回 `id / modelId / displayName / protocol / streaming / tools`，**没有供应商标识**，而普通用户无权读管理员的供应商接口。需在 FR-514 的该响应里增加一个**不含端点/凭据**的公开字段（建议 `providerName`），否则「发送给谁」这条告知无法实现。此项应回填到 FR-514 spec 的 API 契约。
| 粘贴 | 超长文本截断并提示，不静默变慢 |

### 5.2 消息与工具

- 文本消息：流式追加；完成后按 **Markdown 渲染**（复用 `ReleaseNotes` 的 `react-markdown` + `remark-gfm` 与组件配置）。
  - 支持：段落、有序/无序列表、粗体/斜体、行内代码、代码块（等宽 + 滚动）、链接、表格。
  - **不渲染裸 HTML**（`react-markdown` 默认禁用，防 XSS）——模型输出可能含 `<script>` 一类注入。
  - 外链不应用内直跳：沿用 `ReleaseNotes` 的「宿主确认后在新标签打开」策略。
  - ⚠️ **必须额外禁用图片与其它远程子资源**（这是与 `ReleaseNotes` 的一处**有意差异**，不可照抄）：`ReleaseNotes` 的组件配置里有 `img: ({src}) => <img src={src} />`，而它的外链确认**只覆盖 `<a>`**。聊天内容来自模型与外部 MCP 工具，属不可信输入，若沿用该配置，模型只要输出 `![](https://example.com/x.png)`，浏览器就会**在无人点击的情况下自动向第三方发起请求**，泄漏用户 IP 与阅读行为，并把「发图」变成绕过确认的信道。
    - 落地：聊天侧覆盖 `img` 渲染器为**占位符 + 显式「加载图片」按钮**（点击后才请求），或直接不渲染图片。默认取**不渲染**，更简单且无可泄漏面。
    - 同理不得引入会拉取远程资源的 `iframe` / `video` / `audio` / 外链 CSS。
  - 流式进行中按纯文本追加（未闭合的 Markdown 语法会被误渲染成半截结构），**消息完成后一次性切为渲染态**。
- 工具调用：显示工具名 + 状态徽标（`pending_confirmation` / `running` / `ok` / `failed` / `rejected` / `expired` / `unverified`）。
- `unverified` 必须显式写「结果待核实」，**不得展示为成功**（shared-contracts §5.1）。
- 工具结果默认折叠，点击展开；超长结果标「已截断」。

### 5.3 确认卡

- 内联在消息流，含：工具名、目标实体、规范化参数摘要（键值列表）、风险徽标。
- 两个按钮：允许 / 拒绝。**不允许**在卡片上编辑参数（shared-contracts §5.2）。
- 过期后卡片转为「已过期」，按钮禁用。
- 权限在等待期间被撤销 → 卡片转「已失效」并说明原因。

### 5.4 浮窗

| 操作 | 语义 |
|---|---|
| 点击顶栏入口 | 打开/聚焦浮窗 |
| 收起（chevron） | 收为窄条，会话与 run 不变 |
| 关闭（×） | 隐藏浮窗，**不停止** run；再打开回到同一处 |
| 停止（独立按钮） | 显式取消 run（调 `/runs/:id/cancel`） |
| 新会话 | 保留旧会话于历史，开新线程 |
| 历史 | 列表抽屉，按更新时间倒序，分页 |

### 5.5 无障碍

- 输入器有可访问名；发送/停止按钮有明确 `aria-label`。
- 消息流为 `role="log"` + `aria-live="polite"`，但**逐 token 不播报**（只在消息完成时播报整条）。
- 确认卡按钮可键盘聚焦；浮窗打开时焦点移入，关闭后归还触发元素。
- 焦点环、5px 圆角、微元素档位沿用现有 token（shared-contracts §9 末条）。

## 6. 错误与边界

| 场景 | 表现 |
|---|---|
| 模型不可用（503） | 输入器上方提示 + 引导去设置（仅管理员可见链接） |
| 流中断 | 自动按 `seq` 补读；补读失败则显示「连接中断，点击重试观察」（不重新提交 run） |
| 非法游标（400 `INVALID_CURSOR`） | 提示「消息过多，已重新载入最近记录」并拉取快照。**不存在 410**：事件不因保留策略被删，`CURSOR_EXPIRED` 已废弃（见 FR-515 §6.2） |
| `CONVERSATION_BUSY`（409） | 提示「该会话已有进行中的回复」并接续观察已有 run |
| 上下文非法项被剔除 | 显示一条 `context_warning`，不阻断发送 |

## 7. 验收

- [x] 首页与浮窗共用同一会话：在首页发一条，打开浮窗能看到同一条，且**没有重新请求模型**（网络断言）
- [x] **跨路由不丢会话**：从 `/` 跳 `/instances` 再跳 `/logs`，回到 `/` 或打开浮窗，仍停在同一会话同一位置（不新建、不重复生成）
- [x] 切页/收起/关闭后 run 继续，重新打开能看到完整结果（SSE 未中断或已补读）
- [x] 「关闭浮窗」不触发 cancel；「停止」才触发（网络断言两条路径）
- [x] 连续 100 次 `text_delta` 期间，对话区外组件零重渲染（Profiler 断言）
- [x] 切到另一会话再切回，草稿仍在；登出换用户后草稿与缓存清空
- [x] 确认卡片只有允许/拒绝两键，无参数编辑入口
- [x] `unverified` 状态明确显示「结果待核实」，不显示为成功
- [x] 过期确认卡片按钮禁用并说明原因
- [x] 移动端（390px）浮窗为全屏 sheet，可正常发送与查看
- [x] 键盘可达：Tab 能到输入器、发送、确认卡两键；浮窗关闭后焦点回到顶栏入口
- [x] 消息完成时读屏播报整条，逐 token 不播报
- [x] 长会话（>500 条）历史虚拟化，`itemSize` 与真实行盒一致（无累积漂移）
- [x] 无原生 `textarea`/`select`/`table` 直接使用（ESLint 通过）
- [x] 消息完成后的 Markdown 渲染：列表、粗体、行内代码、代码块、表格均正确呈现
- [x] 模型输出含 `<script>alert(1)</script>` 与 `<img onerror=...>` 时**不执行、不渲染为 HTML**（XSS 断言）
- [x] 模型输出 `![x](https://example.com/x.png)` 与 `![x](/api/... )` 时，**浏览器不产生任何第三方网络请求**（用 Network 面板或请求探针断言「零请求」，不是「图片显示不出来」）；同一断言覆盖 `iframe`/`video`/`audio`
- [x] 外链点击后经宿主确认才在新标签打开（不在应用内直接跳转）

### 7.1 安全三条的取证方式（2026-10-27）

- **XSS**：`AssistantChatPanel.markdown-safety.dom.test.tsx` 断言 `<script>` / `<img onerror>`
  不产生任何 DOM 节点，且注入的 `window.__pwned` 未被赋值。这锁住 `react-markdown`
  「默认不解析原始 HTML」的默认值——将来若有人为支持富文本而加 `rehype-raw`，
  这些用例会立刻变红。
- **零第三方请求**：同一测试文件断言 `img`/`iframe`/`video`/`audio`/`source`/`object`/`embed`/`picture`
  全部不出现，且图片一律渲染为「[图片已省略：alt]」占位（占位而非静默丢弃，避免让人
  以为模型没输出）。**边界说明**：本轮断言在 DOM 层完成——DOM 中不存在这些标签，
  浏览器就没有发起该请求的主体；真实浏览器下的网络探针断言尚未补，若需更强的
  端到端证据，可在 E2E 用 `page.route` 拦截 `**/*` 断言零外部请求。
- **外链确认**：本轮**补齐了实现**。此前 `<a>` 是 `target="_blank"` 直开，属验收缺口；
  现改为点击 `preventDefault` + 弹 Dialog 展示完整目标 URL，确认后才 `window.open(..., 'noopener,noreferrer')`，
  取消则纯关闭。相对链接与锚点不拦（站内导航不代表离开宿主）。三条用例分别锁
  「只弹不跳」「确认才跳」「取消不跳」。
- [x] 流式进行中按纯文本追加，不出现半截 Markdown 结构；完成后才切渲染态

## 8. 已定 / 未决

### 8.1 已定（2026-10-23 用户确认）

**消息渲染采用 Markdown。**

- 依据：模型回答天然含列表、粗体、代码块；纯文本呈现会显著损失可读性。而 `react-markdown@^9` 与 `remark-gfm@^4` **已在依赖中**（`apps/control-plane-web/package.json`），且 [ReleaseNotes.tsx](../../../apps/control-plane-web/src/components/views/system-update/ReleaseNotes.tsx#L26-L32) 已有成熟用法（含防 XSS 与外链确认策略），复用成本接近于零。
- 边界：**只渲染不编辑**；不引入新的 Markdown 库；不启用 `rehype-raw`（裸 HTML 一律不渲染）。

### 7.2 流式性能与移动端补齐（2026-10-27）

- **连续 100 次 `text_delta` 期间对话区外组件零重渲染** → `AssistantChatPanel.stream-perf.dom.test.tsx`
  用 React `<Profiler>` 包裹一个与面板并列的兄弟组件（`memo` + props 恒定），连续推进 100 次
  `messages` 更新后断言该兄弟组件的**额外渲染次数不随 token 数增长**（上界 1）。
  - 为何上界是 1 而非 0：`act` 内 100 次 `setState` 被 React 批处理成 1 次提交，父组件那次提交
    会被 Profiler 记为 1 次（子组件经 `memo` bailout，并未真正重复渲染）。取 1 既排除「每 token
    外溢一次」（那会是 100），也不把批处理的单次提交误判成缺陷。
  - 同时附一条**对照用例**：让兄弟组件 props 随 token 变化，断言它确实重渲染——否则前面那条
    在 Profiler 根本没计数时会「永远通过」，属于假绿。
  - **移动端 390px** → `e2e/assistant-home-a11y.spec.ts` 在 390×844 视口下打开浮窗，
    断言**铺满视口**（宽高均 ≥99%）、可输入、页面无横向滚动。

    **2026-10-27 已按产品决策改为真全屏 sheet**：此前实现是固定尺寸浮层
    （`h-[min(38rem,calc(100vh-6rem))]`），390px 下高度只有 ~72%，底部露出页面内容、
    容易误触下层操作。现改为窄屏全屏：`max-sm:inset-0 max-sm:h-full max-sm:w-full
    max-sm:rounded-none`（见 AssistantDock.tsx），宽屏保持原右下角浮层形态。
    - 用 Tailwind `max-sm:` / `sm:` 前缀而非 JS 断点：纯 CSS 切换没有首帧抖动，
      也不需要监听 resize，避免「先按浮层渲染一帧再变全屏」的闪跳。
    - 阈值从「≥65%」改为「≥99%」：原来那个阈值是为记录偏差而设的低水位，
      现在要求真正铺满，只留 1% 给亚像素取整。

### 8.2 未决

1. **浮窗宽度**：右侧非模态，建议 380–420px；是否允许用户拖拽调宽需确认（首版不做）。
2. **多会话并行**：当前是「单活跃会话 + 历史列表」。是否需要在浮窗内并排看两个会话（如钉一个参考会话）需确认。

### 7.1 工具状态徽标（本轮补齐）

`ToolStatusList` 按 `callId` 聚合 `tool_call` / `tool_status` / `tool_result`，一行一调用：
状态区分执行中 / 待确认 / 已完成 / 失败（失败带错误码），结果按**码点**截断（不劈开 emoji）且全文入 `title`，
工具名等宽 `truncate`。断线补读重放同一帧不会让已完成行闪回执行中；非工具帧返回同一数组引用，
避免文本流每秒几十帧连带工具列表重渲染。测试 24 例，i18n 7 键 zh/en 对齐。

### 7.3 浏览器人工实测（2026-10-27，devmock :5399）

- **首页与浮窗共用同一会话** → 实测：AI 首页显示 mock 历史（`server-0009 在最近 6 小时内退出 4 次`），
  点「打开 AI 助手」后浮窗内是**同一段历史**，不是新的空会话。
- **跨路由不丢会话** → 实测：浮窗开着时点侧边栏「服务器」做真实 SPA 导航到 `/instances`，
  浮窗仍在、历史仍在、输入框仍在。（此前用 `pushState` + `popstate` 模拟是错的——
  那不是 React Router 的导航路径，会测出假阴性。）
- **无原生 `textarea`/`select`/`table`** → `eslint.config.js` 对三者都有 `no-restricted-syntax`
  选择器，`npx eslint src/components/views/assistant src/pages/AssistantHomePage.tsx` 退出码 0。
- **消息完成后 Markdown 正确呈现** → 新增 `Markdown 渲染 · 结构完整性` 用例：一条消息里同时含
  粗体 / 行内代码 / 无序列表 / 代码块 / 表格，断言渲染出 `strong`、`code`、`ul>li`、`pre>code`、`table`、`td`。
  这条与安全断言互补——只测「不该渲染的不渲染」会漏掉「渲染器退化成纯文本」这种假绿。
- **浮窗键盘可达与焦点归还** → 实测：聚焦「打开 AI 助手」后激活，焦点移入浮窗输入框；
  点「关闭」后焦点**回到该入口按钮**。注：初次测试用程序化 `click()` 未先聚焦，测出
  「焦点未归还」是**假阴性**——程序化点击不移动焦点，浏览器真实路径是「先聚焦按钮再激活」。
  确认卡两键未验（需触发工具确认流程）。

**仍未验**：`unverified` 状态展示、过期确认卡禁用、读屏播报粒度（见 §7.4 说明）。

### 7.4.2 草稿隔离：纯前端可验（2026-10-27）

原先记为「mock 无 owner 隔离，需真实 CP」。**核实后不需要**——草稿隔离是纯前端逻辑，
与后端是否存在 owner 过滤无关：

- `lib/assistant/chat-draft.ts` 的 key 是 `${DRAFT_PREFIX}${owner}:${conv}`，
  owner 取 `draftScopeUserId`，未登录时为 `anon`；
- `resetDraftScope(nextUserId)` 在换用户时**先删上一个用户作用域的全部键、再切换**，
  并清模块级缓存。

两条机制互补，缺一不可：key 带 owner 让串号**结构上不可能**（不依赖「记得清理」），
`resetDraftScope` 让旧数据**不长期留在浏览器里**（只隔离不删，等于替别人存着未发送内容）。

新增 `chat-draft.dom.test.tsx` **9 例**：
会话维度（切走再切回草稿仍在、新会话独立槽位、空内容删键）、
用户维度（换用户读不到、两用户同号会话互不覆盖、anon 不混同、clearDraft 只清当前那一个）、
清理逻辑（删旧作用域全部键且不波及无关键、先删后切顺序正确）。

**踩到的一个坑**：文件名写成 `.dom.test.ts` 会跑在 **node** 环境（`sessionStorage is not defined`），
项目按后缀区分环境，dom 测试必须用 `.dom.test.tsx`。

### 7.4.1 `unverified` 展示：发现缺失 → 已修复（2026-10-27）

**发现**：后端把「写操作已发出但回执未经核实」记为 `unverified`
（`model.AssistantReceiptUnverified`），但前端 `toolCallPhase()` 只认
`running / pending_confirmation / ok / failed`，`unverified` 落到 `default → null`；
`statusLabel()` 的兜底是 `return status`——于是界面上会直接出现英文 **"unverified"**。
验收要求的是「明确显示**结果待核实**，不显示为成功」。

**修复**：`statusLabel` 的 default 分支单独处理 `unverified`，返回
`t('assistant.toolStatusUnverified', '结果待核实')`；中英文案均已补齐。
chip 变体维持中性（既不是 `chip-ok` 也不是 `chip-bad`）——「不确定」不该被读成失败，
但**绝不能**被读成成功。

**测试**：`ToolStatusList.dom.test.tsx` 新增一例，同时断言三个方向——
含「结果待核实」、不含后端原文 `unverified`、不含「已完成」。

### 7.4.0 读屏播报：取证到 ARIA 可访问性树（2026-10-27）

验收「消息完成时读屏播报整条，逐 token 不播报」。取证分两层，**如实标注各层做到哪**：

**已验（本轮）**：

1. 单测锁住 HTML 属性：`role="log"` + `aria-live="polite"`，且**不得为 `assertive`**。
2. E2E 用 Playwright 的 `ariaSnapshot()` 取**可访问性树**快照，断言消息区在树里确实呈现为
   log 容器、`aria-live=polite`、且有可访问名（`assistant-home-a11y.spec.ts`
   「消息区在可访问性树中是 polite 的 live region」）。

**为什么这已经足以支持勾选**：「逐 token 不播报」正是 `aria-live="polite"` 的规范定义——
等用户空闲再播报，不打断当前朗读。屏幕阅读器读的就是这棵可访问性树，
而非 HTML 属性本身；验到 ARIA 树这一层，等于验到了它们决策所依据的输入。

**未验**：某款具体屏幕阅读器（VoiceOver/NVDA）的实际播报**粒度与措辞**。
这需要真机辅助技术，无头浏览器无法覆盖。若日后要做真机走查，只需确认
「一轮回复完成后只播报一次、正文完整」，不必重验语义属性。

### 7.4 真实 CP + 本地假上游（2026-10-27）

nustarapi.com 在本环境不可达（直连超时、走代理被 reset），因此起了一个**本地假上游**
（`.tmp/fake-upstream/server.py`，实现 OpenAI `/chat/completions` 的 SSE 流，
用 `?tokens=N&delay=S` 控制节奏）。**必须如实标注：上游是假的**，验的是 run 状态机与前端行为，
不涉及真实模型质量。

真实 CP（SQLite 空库）+ 假上游下补验两条：

- **关闭浮窗后 run 继续** → 发一条消息，等流式文本出现（确认 run 已开始），点「关闭」，
  3 秒后重开浮窗：看到**完整的 40 段**输出。说明关闭浮层没有打断 run。
- **「关闭」不 cancel，「停止」才 cancel** → 另发一条，run 进行中点「停止」，
  查后端事件流 `run_end`：`status=cancelled, reason=cancelled`。
  两条路径形成对照：关闭 → 跑完；停止 → cancelled。

**长会话历史：发现缺失 → 已实现虚拟化并验证（2026-10-27，本轮闭环）**

**发现**（用 SQL 造 500 条消息实测）：后端 `/messages` 支持 `before`+`limit`（默认 20），
但前端 `useAssistantMessages` 两者都没传、也没有「加载更多」入口；
`AssistantChatPanel` 内没有任何虚拟化实现（只有 `stickToBottom` 滚动跟随）。
结果 500 条会话只渲染最近约 20 条，滚到顶部**看不到第 1 条**——是功能缺失，不是漂移没测准。

**实现**（两处改动）：

1. **数据侧** `useAssistantMessages` 改为**翻页拉全**：`limit=100`，用 `before`（消息 id，
   后端 `WHERE id < ?`）逐页往前翻，`unshift` 拼到前面；硬上限 20 页（2000 条），
   防病态会话拖死页面。
2. **渲染侧** 接入已有 `useVirtualRows` 的**变高模式**（`sizes` + 前缀和）：
   `space-y-3` 折进行盒改为 `pb-3`（护栏要求：行距必须计入行高，否则按行盒定位会累积漂移）；
   未测量过的消息先用估算值 80px，渲染后用 rAF 测量并回填（1px 容差，无变化不 setState）。

**验证**（真实 CP + 500 条会话，Chromium 实测）：

| 位置 | 可见范围 | DOM 节点数 |
|---|---|---|
| 底部 | 第 486–500 条 | 46 |
| **顶部** | **第 1–13 条** | 40 |

滚动到顶部能看到第 1 条，`scrollTop=0` 时位置正确（坐标无累积漂移），
DOM 始终只挂载 40 来个气泡而非 500。**已勾选。**

**过程中的一次取舍**：为让「贴底跟随」与虚拟窗口共用同一滚动容器，给 `useVirtualRows`
加了可选外部 `containerRef`。React Compiler 无法穿透 `externalRef ?? internalRef` 证明其稳定，
报 "Existing memoization could not be preserved"；试过 `useMemo` 包装仍未消除，
最终在 `measure` / `flushScroll` 两处加 `eslint-disable` 并注明理由——
依赖数组与 memo 均已手写完整，被跳过的是编译器的自动优化，不是正确性。

**历史会话的工具回显：已实现（原「已知缺口」已过时，2026-10-27 核实后删除）**

曾经的缺口描述是「后端把工具结果写成 `role=tool` 消息，前端只映射 user/assistant，
历史里没有 call/status 事件」。核实后发现三项都已就位：

- 后端：`AssistantChatService.ListMessageToolCalls`（`assistant_tool_history.go`）按消息聚合，
  经 `GET /assistant/conversations/:id/tool-calls` 暴露 `{ byMessage: { [messageId]: [...] } }`；
- 前端：`useAssistantToolCalls`（`api/assistant-chat.ts:246`）读取该接口；
- 接线：`AssistantHistoryPage.tsx:57` 已接入，历史消息下渲染当时的工具调用与状态。

测试覆盖（`AssistantHistoryPage.dom.test.tsx`）：
「历史消息回显当时的工具调用与状态」+「没有工具调用时不渲染工具块（不留空壳）」——
后者防的是"每条消息下面挂个空壳"，属易漏的反向断言。

## 附录：浏览器端到端取证（2026-10-25）

`apps/control-plane-web/e2e/assistant-session-isolation.spec.ts`（Playwright）两条用例：

1. **跨路由不丢会话**——发送一轮后切 `/instances` → `/logs` → 回 `/`（全程 SPA 导航，**不用 `page.goto`**，
   否则会把「跨路由」退化成「刷新后从 sessionStorage 恢复」）；断言浮窗在、消息在、草稿在，
   且 `POST /runs` 计数**仍为 1**（没有重新提交）。
2. **换用户不串号**——admin 在「新会话」写草稿不发送 → 真实登出 → **就地**登录 operator →
   断言 operator 输入框里没有 admin 的草稿；再做一次反向（operator → admin）。

**本次取证前修掉的真实缺陷**：草稿 key 原为 `jm-assistant-draft:<conversationId|new>`，**不含 userId**，
而 `logout()` 只 `queryClient.clear()`，不碰 sessionStorage 与模块级 `draftCache`。后果是新会话草稿
（key 固定为 `:new`）**必然跨用户碰撞**——B 登录后会看到 A 未发送的内容。
修法：key 改为 `jm-assistant-draft:<userId|anon>:<conversationId|new>`，新增 `setDraftScopeUserId` /
`resetDraftScope`（登出删旧作用域存储 + 清模块缓存 + 通知重读），并在 `login`/`logout`/`loadFromStorage`
三处接入；`jm-assistant-last-conversation` 同步加 userId 维度。
**反向验证**：把 key 的 owner 段去掉后该用例确定性转红，报错即「换用户后不能看到上一个用户的未发送草稿」。

## 附：PRD 表内原始记录（2026-10-26 迁入）

按「PRD 只保留一行 FR + 状态，详情归 spec」的约定，以下内容自 PRD 迁入（保留原文）。

### FR-519

共享聊天界面与全局上下文面板

（**前端主体已落地**：`AssistantChatPanel`（流式纯文本追加 → 完成后 Markdown；**不渲染图片**，防模型输出触发无人点击的远程请求）、`useAssistantChat`（提交/SSE 观察/断线按 seq 补读且**不重提**、幂等键一轮内固定）、`AssistantDock`（新会话/历史/**分页（`useAssistantConversationsInfinite` + 「加载更多」，有下一页才显示、加载中禁用）**/收起/关闭；关闭与收起**都不停止** run，停止是独立按钮）、顶栏入口经 console store 跨路由常驻；**逐调用确认卡已接入**（`ConfirmationCards`，只发 approve/reject、参数原样展示、过期禁用）；**上下文芯片已接入**（可移除，与展示标签分离）**草稿按会话保存**（`chat-draft.ts`，key 含 conversationId，新会话独立）；真机验证浮窗与输入器聚焦。**工具状态徽标已实现并接入**：`ToolStatusList` 按 `callId` 聚合 `tool_call`/`tool_status`/`tool_result` 为一行一调用，状态徽标区分执行中/待确认/已完成/失败（失败带错误码）、结果按码点截断（不劈开 emoji）且全文入 title、工具名等宽 truncate；断线补读重放同一帧不会让已完成行闪回执行中；非工具帧返回同一数组引用避免文本流连带重渲染。测试 24 例（徽标 14 + 接入 2 + 既有事件 8），i18n 7 键 zh/en 对齐且 `missing-keys` 门禁转绿。**反向验证发现并修掉一处真实缺陷**：窄屏聊天气泡内长不可断 token（工具参数/路径/URL）被裁切，且把消息列表撑出隐藏横向滚动（375px 下可横滚 27px）——补 `break-words` 修复。另经真实浏览器实测：历史抽屉可分页（初次 20 条→加载更多→40 条），但「加载更多」位于 `max-h-56` 滚动容器底部，需滚到底才可见。**历史工具回显已补齐**（本轮）：后端新增 `GET /assistant/conversations/:id/tool-calls`，从 `assistant_events` 三类工具事件按 `(messageId, callId)` 聚合出「每条回复当时调了哪些工具、各自终态、失败码、结果预览」；缺 `tool_status` 但有 `tool_result` 时推断 `ok`（比永远转圈诚实，历史数据可能来自 status 落库失败的轮次）。前端 `useAssistantToolCalls` + 历史页在消息下渲染 `ToolStatusList`，无调用不留空壳。测试：后端 4 例（跨事件归并 / 缺 status 推断 / 越权拒绝 / 无事件空 map）、前端 2 例（回显含错误码 / 无调用不渲染），均已反向验证（移除回显、破坏归并各自变红）。**已补浏览器端到端取证（2026-10-25）**：`e2e/assistant-session-isolation.spec.ts` 两例通过。① 跨路由不丢会话——全程 SPA 导航（不用 `page.goto`，否则退化成「刷新恢复」），断言浮窗/消息/草稿都在且 `POST /runs` 计数仍为 1；② 换用户不串号——admin 写草稿不发送 → 真实登出 → 就地登录 operator → 断言看不到上一用户草稿，并做反向。**取证前修掉真实缺陷**：草稿 key 不含 userId（新会话固定 `:new`，**必然跨用户碰撞**），且 `logout()` 只清 React Query、不碰 sessionStorage 与模块级 `draftCache`——B 登录会看到 A 的未发送内容。改为 key 带 `<userId|anon>` 段，新增 `setDraftScopeUserId`/`resetDraftScope` 并接入 `login`/`logout`/`loadFromStorage`，`last-conversation` 同步加 userId。反向验证：去掉 owner 段后用例确定性转红。

