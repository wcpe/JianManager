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

- [ ] 首页与浮窗共用同一会话：在首页发一条，打开浮窗能看到同一条，且**没有重新请求模型**（网络断言）
- [ ] **跨路由不丢会话**：从 `/` 跳 `/instances` 再跳 `/logs`，回到 `/` 或打开浮窗，仍停在同一会话同一位置（不新建、不重复生成）
- [ ] 切页/收起/关闭后 run 继续，重新打开能看到完整结果（SSE 未中断或已补读）
- [ ] 「关闭浮窗」不触发 cancel；「停止」才触发（网络断言两条路径）
- [ ] 连续 100 次 `text_delta` 期间，对话区外组件零重渲染（Profiler 断言）
- [ ] 切到另一会话再切回，草稿仍在；登出换用户后草稿与缓存清空
- [ ] 确认卡片只有允许/拒绝两键，无参数编辑入口
- [ ] `unverified` 状态明确显示「结果待核实」，不显示为成功
- [ ] 过期确认卡片按钮禁用并说明原因
- [ ] 移动端（390px）浮窗为全屏 sheet，可正常发送与查看
- [ ] 键盘可达：Tab 能到输入器、发送、确认卡两键；浮窗关闭后焦点回到顶栏入口
- [ ] 消息完成时读屏播报整条，逐 token 不播报
- [ ] 长会话（>500 条）历史虚拟化，`itemSize` 与真实行盒一致（无累积漂移）
- [ ] 无原生 `textarea`/`select`/`table` 直接使用（ESLint 通过）
- [ ] 消息完成后的 Markdown 渲染：列表、粗体、行内代码、代码块、表格均正确呈现
- [ ] 模型输出含 `<script>alert(1)</script>` 与 `<img onerror=...>` 时**不执行、不渲染为 HTML**（XSS 断言）
- [ ] 模型输出 `![x](https://example.com/x.png)` 与 `![x](/api/... )` 时，**浏览器不产生任何第三方网络请求**（用 Network 面板或请求探针断言「零请求」，不是「图片显示不出来」）；同一断言覆盖 `iframe`/`video`/`audio`
- [ ] 外链点击后经宿主确认才在新标签打开（不在应用内直接跳转）
- [ ] 流式进行中按纯文本追加，不出现半截 Markdown 结构；完成后才切渲染态

## 8. 已定 / 未决

### 8.1 已定（2026-10-23 用户确认）

**消息渲染采用 Markdown。**

- 依据：模型回答天然含列表、粗体、代码块；纯文本呈现会显著损失可读性。而 `react-markdown@^9` 与 `remark-gfm@^4` **已在依赖中**（`apps/control-plane-web/package.json`），且 [ReleaseNotes.tsx](../../../apps/control-plane-web/src/components/views/system-update/ReleaseNotes.tsx#L26-L32) 已有成熟用法（含防 XSS 与外链确认策略），复用成本接近于零。
- 边界：**只渲染不编辑**；不引入新的 Markdown 库；不启用 `rehype-raw`（裸 HTML 一律不渲染）。

### 8.2 未决

1. **浮窗宽度**：右侧非模态，建议 380–420px；是否允许用户拖拽调宽需确认（首版不做）。
2. **多会话并行**：当前是「单活跃会话 + 历史列表」。是否需要在浮窗内并排看两个会话（如钉一个参考会话）需确认。
