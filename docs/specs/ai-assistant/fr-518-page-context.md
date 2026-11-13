# 功能规格：类型化页面上下文与实体选择（FR-518）

> 状态：**已实现（2026-10-23）**　·　关联 PRD：FR-518　·　范围：仅 FR-518
>
> 落地范围：前端消费端（`lib/assistant/page-context.ts`：注册表/芯片/序列化/裁剪）、
> 六个白名单页注册（instances / nodes / logs / monitor / alerts / tasks）、
> 「添加实体」入口（复用 `InstancePicker` + 服务端搜索 + 300ms 防抖）、
> 后端校验与重验（`service/assistant_context.go`：白名单页键、实体 ≤8、filter ≤64、
> 敏感键黑名单、4 KiB 拒收、实体按权限剔除并回 `context_warning`）。
> 测试：后端 11 项 + 前端 18 项。真机链路（假 SSE 供应商）验证过 `context_snapshot` 落库。
>
> 两处实现取舍：
> 1. **实体 id 兼容 `string | number`**（`assistantContextID` 自定义 Unmarshal）：
>    前端很可能直接传数字主键，只用 `string` 会让 `{"id":42}` 在解析阶段整体 400，
>    把「一个合法实体」误判成「请求体不合法」。
> 2. **前端也做上限裁剪**（超 4 KiB 除外）：让用户当场从芯片看到「只带前 8 个」，
>    而不是发出后才收到 `context_warning`；但**整体超限不截断**，原样交后端拒收——
>    悄悄裁剪会让用户以为上下文完整送达。
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§6 页面上下文与外发），本文不复述。
> 前置：FR-515（运行时）、FR-516（工具）。

## 1. 背景与目标

没有页面上下文的助手只能回答泛泛问题（「我的服为什么卡」）。FR-518 让助手知道**用户正看着什么**——当前页、页签、时间窗、选中的实体——从而能把问题落到具体对象上（「这台服为什么卡」）。

关键在于**不能过度外发**：整页 DOM、完整日志、环境变量、密钥都不该进模型；「移除芯片后背景不得继续外发」；「URL 与名称都不是权限凭据」。

范围内：上下文类型化契约、白名单页注册、实体选择 UI（芯片）、发送时快照、千级候选的服务端搜索、外发前裁剪与脱敏。

范围外：上下文的服务端权限校验（属 FR-516 执行期重验）、日志全文外发、跨页历史改写、DOM 抓取。

## 2. 现状核实（只读结论）

- **服务端搜索已可用**：`useInstanceSearch`（[apps/control-plane-web/src/api/instances.ts](../../../apps/control-plane-web/src/api/instances.ts#L136-L142)）走 `GET /instances/search`，支持 `q`/分页；这是千级候选的正确路径。
- **千级选择器视图可复用，但取数策略要自己接**：`InstancePicker`（[apps/control-plane-web/src/components/views/instances/InstancePicker.tsx](../../../apps/control-plane-web/src/components/views/instances/InstancePicker.tsx#L74)）是**受控视图，不取数**——候选窗口（`items`）、总数（`total`）与键入回调（`onQueryChange`）都由外壳注入，防抖与请求也在外壳侧。FR-518 需要新写一个外壳：接 `useInstanceSearch`（服务端搜索）、做防抖、维护候选窗口与截断提示。**不能直接挂上 `InstancePicker` 就以为有了搜索**。
- **无上下文注册机制**：各页面组件目前不向上暴露「我现在展示什么」；需新增一层轻量注册（不得侵入页面业务逻辑）。
- **页面集合稳定**：路由表在 [apps/control-plane-web/src/components/console/Workspace.tsx](../../../apps/control-plane-web/src/components/console/Workspace.tsx#L152-L160) 起，可按 `path` 枚举白名单页。
- **URL 即状态**：本仓多数列表页把筛选/分组/排序写进 `location.search`（如实例页），故「页面上下文」的部分字段可直接从 URL 派生，无需页面主动上报。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 上下文载体 | **类型化结构**，非自由文本 | 可校验、可裁剪、可脱敏 |
| 实体标识 | `{type, id}`；名称由授权后端回填 | 名称不是凭据（shared-contracts §6 第 3 条） |
| 页注册 | **白名单**，未注册页不提供上下文 | 默认不外发 |
| 外发范围 | 只发结构（页键/页签/时间窗/实体引用/必要筛选），**不发正文** | 日志/指标正文留给工具按权限取 |
| 芯片 | 可见、可移除；移除后**不再外发**该项 | shared-contracts §6 第 5 条 |
| 冻结时机 | 发送时快照写入 `run.context_snapshot`，此后换页不改写 | shared-contracts §6 第 2 条 |
| 千级候选 | 服务端搜索 + 防抖 + 有界首屏 + 截断提示 | 复用 `InstancePicker` 既有约定 |
| 不采集 | 整页 DOM、Cookie、localStorage、环境变量、密钥、表单草稿 | shared-contracts §6 第 4 条 |
| 明确不做 | 自动注入完整日志、自动附加当前页截图、跨轮次累积历史上下文 | 防止无界外发 |

## 4. 上下文契约

### 4.1 结构

```ts
interface PageContext {
  pageKey: string                    // 白名单页键，如 "instances" / "nodes" / "logs"
  tab?: string                       // 枚举页签，须在该页声明集内
  timeRange?: string                 // 如 "24h"；仅时间序列页
  entities: EntityRef[]              // 至多 8 个
  filters?: Record<string, string>   // 白名单键，值截断至 64 字符
}

interface EntityRef {
  type: 'instance' | 'node' | 'task' | 'alert' | 'player'
  id: string | number
  label?: string                     // 仅用于 UI 展示，外发时丢弃
}
```

### 4.2 白名单页与可暴露字段

| pageKey | 可暴露 | 明确不暴露 |
|---|---|---|
| `instances` | 选中实例、状态筛选、分组维度 | 列设置、滚动位置 |
| `nodes` | 选中节点 | 节点凭证、enroll token |
| `logs` | 时间窗、级别筛选、源标识 | **日志正文**、原始行 |
| `monitor` | 时间窗、指标键、目标实体 | 原始时序点全量 |
| `alerts` | 选中告警、级别筛选 | 通知通道凭证 |
| `tasks` | 选中任务 | 任务详情正文（由工具按权限取） |

未列入的页面（如 `settings`、`permissions`、`users`）**不提供上下文**——它们的界面本身含敏感配置语义。

## 5. 交互

### 5.1 注册方式

页面通过一个轻量 hook 上报，不侵入业务状态：

```ts
// 伪代码：页面只声明「对外可说什么」，不暴露内部状态对象
usePageContext({
  pageKey: 'instances',
  tab,
  entities: selectedId ? [{ type: 'instance', id: selectedId }] : [],
  filters: { status: statusFilter },
})
```

约束：
- hook 内部只做**派生与上报**，不读页面内部 state 对象、不遍历 DOM。
- 上报值必须可从 props/URL 直接得到。
- 页面卸载即清空（不留残影）。

### 5.2 芯片

- 位置：输入框上方一行。
- 内容：「当前页：实例」「实例：survival-1 ✕」「时间窗：24h ✕」。
- 移除即从本轮上下文中删除；**不保留隐藏背景**（shared-contracts §6 第 5 条）。
- 首次使用须明示「相关数据将发送给 <供应商>」（与模型选择同处）。

### 5.3 手动添加实体

- 通过 `InstancePicker` 搜索添加实例（服务端搜索、防抖、有界首屏、截断提示）。
- 添加后的实体与本页自动上下文同权，可单独移除。

## 6. 外发与裁剪

| 规则 | 说明 |
|---|---|
| 发送时冻结 | `run.context_snapshot` 一次性写入；后续换页只影响下一轮 |
| 服务端重验 | 发送时后端按 `EntityRef` 重新查实体与权限；不存在/无权 → 该实体被剔除而非报错（避免用上下文探测资源存在性） |
| 名称丢弃 | `label` 仅 UI 用，外发前移除；模型看到的名称由工具按权限回填 |
| 长度上限 | `filters` 值 ≤64 字符；实体 ≤8 个；整体 JSON ≤4 KiB |
| 脱敏 | 任何形如 token/key/secret/password 的筛选键直接丢弃 |

**超限与非法项的处理分两类，不要混为一谈**：

| 情况 | 处理 | 依据 |
|---|---|---|
| 单个实体不存在/无权 | **剔除该实体，run 继续**，发 `context_warning` | 用户可能只是刷新了页面；因一项失效就拒绝整条消息过于粗暴 |
| 单个 filter 值 >64 字符 / 命名敏感键 | **丢弃该键，run 继续**，发 `context_warning` | 同上 |
| 实体数 >8 | **保留前 8 个，其余丢弃**，发 `context_warning` | 上限是保护性约束，不必阻塞 |
| **整个上下文 JSON >4 KiB** | **拒绝本次请求**，400 `CONTEXT_TOO_LARGE` | 已不是「某项失效」，而是请求体本身异常——继续裁剪会让用户以为上下文完整送达 |

## 7. 接口

上下文随 run 提交，不单独开接口：

```
POST /api/v1/assistant/conversations/:id/runs
{
  "message": "...",
  "modelId": 12,
  "context": { "pageKey": "instances", "entities": [{"type":"instance","id":42}], "filters": {"status":"RUNNING"} }
}
```

服务端落 `assistant_runs.context_snapshot`（FR-515 §4.3）。错误的上下文不阻断 run：剔除非法项后继续，并在事件里给一条 `context_warning`（新增事件类型）。

## 8. 验收

- [x] 未注册页（`/settings`、`/permissions`）不产生任何上下文
- [x] 移除「实例 survival-1」芯片后，本轮外发 JSON 中不含该实体（抓包断言，不只看 UI）
- [x] 换页后已发送消息的 `context_snapshot` 不变（后端快照断言）
- [x] `label` 不出现在外发 JSON 中
- [x] 伪造实体 id（不存在 / 他人实例）→ 该实体被剔除，run 继续，不返回存在性提示
- [x] 含 `token`/`secret` 的筛选键被丢弃
- [x] **敏感 query 被拒**：URL 中 `?token=...`、`?apiKey=...`、`?password=...` 等键即使落在白名单页也不进入上下文（按键名黑名单 + 值形态双重判定）
- [x] **禁止 DOM 抓取**：全量搜索前端代码，上下文组装路径不得出现 `innerText` / `textContent` / `outerHTML` / `document.querySelector` 等 DOM 读取（静态断言 + 代码评审）
- [x] 上下文 JSON 超 4 KiB → 服务端拒绝并给出明确错误
- [x] 千级实例下打开选择器：首屏只取 N 条、键入才搜索、截断有提示（不做全量列举）
- [x] 页面卸载后上下文清空，助手不再声称「你正在看 X」
- [x] 日志页只发源标识与时间窗，**不发日志正文**（抓包断言）

### 8.1 未勾项说明（2026-10-27 核对）

- **伪造实体 id 被剔除**：**本轮已补用例并勾选**。`TestAssistantContext_RevalidateRejectsForgedEntities`
  覆盖三种情况：不存在的实例 id → 剔除；他人的实例（存在但无权）→ 剔除；平台管理员 → 可访问。
  另加一条**接口形状断言**：「不存在」与「无权」必须返回同一个布尔 false——若哪天改成返回
  带区分的 error，上下文就成了存在性探测接口（该断言会让那种改动编译失败）。
  边界：本条验的是**重验层**（实体被剔除、run 继续），端到端「不返回存在性提示」由
  `sanitizeAssistantContext` 的 `context_warning` 承接（剔除后继续，不整单拒绝）。
- **敏感 query 值形态判定**：**本轮已补齐实现**。此前只有键名黑名单，拦不住
  `?q=eyJhbGci...`（键名无害、值像 JWT）。现双端各加一层值形态判据、逐条对齐：
  前端 `PAGE_CONTEXT_SECRET_VALUE_PATTERNS` / 后端 `assistantContextSecretValuePatterns`，
  覆盖 JWT、`sk-`/`ghp_`/`AKIA`/`xoxb-` 前缀、`Bearer` 头、纯 hex 摘要（md5/sha1/sha256）。
  - **判据刻意保守**：只认结构明确无误的几类，不做「高熵随机串」猜测——误杀会把正常
    筛选条件丢掉（用户只见「筛选没生效」查不出原因），比漏杀更难排查。
  - **必须在截断前判定**：JWT 上百字符，先 `slice(0,64)` 会破坏三段结构导致正则失配，
    整条判据静默失效。前后端各有一条用例专门锁这个顺序。
  - 用例：前端 `敏感筛选值形态判定` 7 例（含 uuid 与普通筛选词**不误杀**的反向断言）、
    后端 `SensitiveFilterValuesDropped` + `NoFalsePositive` + `ValueCheckBeforeTruncate`。
  - 副作用：既有两条「筛选值截断」用例用 `"a"` 重复构造长值，而全 a 恰好是合法十六进制，
    会被新判据认作摘要而丢弃——已改用 `"x"` 重复，保持它们原本测截断的语义。
- **千级实例下的选择器**：**本轮已做浏览器实测并勾选**（devmock 1200 实例，dev server :5399）。
  三条逐一验过：
  1. **首屏有界** → 选择器首屏请求为 `/instances/search?page=1&pageSize=50&sort=name&order=asc`，
     `pageSize=50`（= `CANDIDATE_LIMIT`），候选共渲染 50 行；
  2. **键入才搜索** → 在浮层内输入框（placeholder `搜索或输入…`）键入 `proxy-02`，
     300ms 防抖后发出 `?q=proxy-02&page=1&pageSize=50`，候选随之收敛到 9 行；
  3. **截断有提示** → 服务端 `total=1200`、`items=50`，页面出现
     「已显示前 50 项，共 1200 项；继续输入可缩小范围」。
  - 两个踩坑记下来，避免后来者重复：①不能用全局 `placeholder` 匹配输入框——顶栏
    「搜索服务器」是另一个搜索框，会把 `q` 打到它上面（一次实测正是这样误判成
    `pageSize=100`）；应限定在 `[data-radix-popper-content-wrapper]` 内找。
    ②截断提示渲染在**触发器下方**、不在浮层内，只抓浮层 innerText 会漏掉它。
  - `CANDIDATE_LIMIT` 目前是 50；若将来调整，本条的判据（有界 + 提示）不受影响，
    但文中的具体数字需同步。
- **页面卸载后上下文清空**：**本轮已补齐并勾选**。原本只验了「订阅者不再被通知」，
  但那只证明通知停发，**没证明上下文真的被清掉**——而后者才决定下一轮外发什么。

  新增两条**走真实组件生命周期**的用例（此前是直接调 `setPageContext(null)`，
  没证明「卸载会触发置空」这条因果）：
  1. `render(<Probe/>)` → `unmount()` → 注册表变 `null`、序列化快照为空；
  2. `rerender` 换 `pageKey` → 新上下文替换旧的，**旧页面实体不残留**。

  **关于后半句「助手不再声称你正在看 X」**：它是**观察结论**，不是可直接断言的量——
  判断模型"有没有声称"要它生成自然语言，措辞每次不同、判定主观。但它有唯一的**因**：
  页面卸载后外发载荷里不能还带旧页面的 `pageKey`/实体。上面的用例断言的正是这个上游事实，
  载荷干净了，模型就没有材料说那句话。这是本条能自动化的最强形式。

## 9. 未决 / 需用户确认

1. **实体上限 8 个**是占位值，需按实际排查场景确认。
2. **是否需要「整页快照」能力**（例如把当前告警列表前 20 条结构化带上）：当前设计明确不做，靠工具按权限取。
3. **`context_warning` 是否要升为 `error`**：当前设计是「剔除后继续」，若用户更希望「有任何非法项就中止」需调整。

## 附录：浏览器端到端取证（2026-10-25）

`apps/control-plane-web/e2e/assistant-page-context.spec.ts`（Playwright，devmock 模式）两条用例：

1. **移除芯片后不再外发**——先断言实体**在**第一轮请求的 `contextSnapshot.entities` 里（基准，防「从未加上」的假绿），
   再点移除、断言 DOM 消失，最后断言第二轮请求的 `entities` **为空**；并深遍历确认快照里**不含 `label`**。
2. **跨页快照各自独立**——`/instances` 与 `/nodes` 两轮请求的 `pageKey` 分别为 `instances`/`nodes`，
   第二轮不累积第一轮的 `filters`，且第一轮原始请求体在第二轮之后未被改写（发送时冻结）；
   同时断言浮窗与上一轮消息跨路由保留。

**验证边界（2026-10-25 首次取证时）**：devmock 读取但**不落库** `contextSnapshot`，故当时「后端快照不变」一条无法验证。

**已补真实后端取证（2026-10-26）**：`e2e/assistant-page-context-real.spec.ts` 在**真实 CP + 非 mock 前端 + 真实
SQLite 落库**下跑通 1 例，直接断言数据库：run 19 的 `pageKey=instances`、run 20 的 `pageKey=nodes`，
随后回查 run 19 仍为 `instances`**未被后续换页覆盖**。§8「换页后已发送消息的 `context_snapshot` 不变」
以此为准，已勾选。

## 附：PRD 表内原始记录（2026-10-26 迁入）

按「PRD 只保留一行 FR + 状态，详情归 spec」的约定，以下内容自 PRD 迁入（保留原文）。

### FR-518

类型化页面上下文与实体选择

（**后端与前端均已实现**：上下文结构与长度校验（白名单页 / 最多 8 个实体 / 单字段 64 字符 / 敏感键剔除 / 超 4 KiB 直接拒收）、发送前按调用者权限重验实体（不存在或无权只剔除该实体、不整单拒绝）、六个白名单页注册、可移除芯片与发送时快照；千级候选走服务端搜索（`InstancePicker` + `useInstanceSearch`，默认前 N 条 + 防抖下发 `q` + 截断提示），**不使用 Radix Select**（它依赖全部 `SelectItem` mount 才能提供首字母跳转与方向键导航，无法只渲染前 N 项）。测试：后端 11 项 + 前端 18 项全绿。**已补浏览器端到端取证（2026-10-25）**：`e2e/assistant-page-context.spec.ts` 两例通过。① 芯片移除后不外发——先断言实体**在**第一轮请求 `contextSnapshot.entities` 里（基准，防「从未加上」的假绿），移除后断言第二轮 `entities` 为空，并深遍历确认快照不含 `label`；② 跨页快照各自独立——`/instances` 与 `/nodes` 两轮 `pageKey` 不同、第二轮不累积第一轮 `filters`、第一轮请求体未被改写（发送时冻结），浮窗与消息跨路由保留。**验证边界**：devmock 读取但不落库 `contextSnapshot`，故 spec 「后端快照不变」需接真实后端补验。

