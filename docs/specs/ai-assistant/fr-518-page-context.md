# 功能规格：类型化页面上下文与实体选择（FR-518）

> 状态：草拟 / 待用户审核，**未实现**（前端无上下文注册机制；服务端搜索与选择器已就绪）　·　关联 PRD：FR-518　·　范围：仅 FR-518
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§6 页面上下文与外发），本文不复述。
> 前置：FR-515（运行时）、FR-516（工具）。

## 1. 背景与目标

没有页面上下文的助手只能回答泛泛问题（「我的服为什么卡」）。FR-518 让助手知道**用户正看着什么**——当前页、页签、时间窗、选中的实体——从而能把问题落到具体对象上（「这台服为什么卡」）。

关键在于**不能过度外发**：整页 DOM、完整日志、环境变量、密钥都不该进模型；「移除芯片后背景不得继续外发」；「URL 与名称都不是权限凭据」。

范围内：上下文类型化契约、白名单页注册、实体选择 UI（芯片）、发送时快照、千级候选的服务端搜索、外发前裁剪与脱敏。

范围外：上下文的服务端权限校验（属 FR-516 执行期重验）、日志全文外发、跨页历史改写、DOM 抓取。

## 2. 现状核实（只读结论）

- **服务端搜索已可用**：`useInstanceSearch`（[apps/control-plane-web/src/api/instances.ts](apps/control-plane-web/src/api/instances.ts#L136-L142)）走 `GET /instances/search`，支持 `q`/分页；这是千级候选的正确路径。
- **千级选择器已可用**：`InstancePicker`（[apps/control-plane-web/src/components/views/instances/InstancePicker.tsx](apps/control-plane-web/src/components/views/instances/InstancePicker.tsx#L74)）已实现「默认取前 N + 键入防抖下发 q + 截断提示」。
- **无上下文注册机制**：各页面组件目前不向上暴露「我现在展示什么」；需新增一层轻量注册（不得侵入页面业务逻辑）。
- **页面集合稳定**：路由表在 [apps/control-plane-web/src/components/console/Workspace.tsx](apps/control-plane-web/src/components/console/Workspace.tsx#L152-L160) 起，可按 `path` 枚举白名单页。
- **URL 即状态**：本仓多数列表页把筛选/分组/排序写进 `location.search`（如实例页），故「页面上下文」的部分字段可直接从 URL 派生，无需页面主动上报。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 上下文载体 | **类型化结构**，非自由文本 | 可校验、可裁剪、可脱敏 |
| 实体标识 | `{type, id}`；名称由授权后端回填 | 名称不是凭据（shared-contracts §6.3） |
| 页注册 | **白名单**，未注册页不提供上下文 | 默认不外发 |
| 外发范围 | 只发结构（页键/页签/时间窗/实体引用/必要筛选），**不发正文** | 日志/指标正文留给工具按权限取 |
| 芯片 | 可见、可移除；移除后**不再外发**该项 | shared-contracts §6.5 |
| 冻结时机 | 发送时快照写入 `run.context_snapshot`，此后换页不改写 | shared-contracts §6.2 |
| 千级候选 | 服务端搜索 + 防抖 + 有界首屏 + 截断提示 | 复用 `InstancePicker` 既有约定 |
| 不采集 | 整页 DOM、Cookie、localStorage、环境变量、密钥、表单草稿 | shared-contracts §6.4 |
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
- 移除即从本轮上下文中删除；**不保留隐藏背景**（shared-contracts §6.5）。
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

- [ ] 未注册页（`/settings`、`/permissions`）不产生任何上下文
- [ ] 移除「实例 survival-1」芯片后，本轮外发 JSON 中不含该实体（抓包断言，不只看 UI）
- [ ] 换页后已发送消息的 `context_snapshot` 不变（后端快照断言）
- [ ] `label` 不出现在外发 JSON 中
- [ ] 伪造实体 id（不存在 / 他人实例）→ 该实体被剔除，run 继续，不返回存在性提示
- [ ] 含 `token`/`secret` 的筛选键被丢弃
- [ ] 上下文 JSON 超 4 KiB → 服务端拒绝并给出明确错误
- [ ] 千级实例下打开选择器：首屏只取 N 条、键入才搜索、截断有提示（不做全量列举）
- [ ] 页面卸载后上下文清空，助手不再声称「你正在看 X」
- [ ] 日志页只发源标识与时间窗，**不发日志正文**（抓包断言）

## 9. 未决 / 需用户确认

1. **实体上限 8 个**是占位值，需按实际排查场景确认。
2. **是否需要「整页快照」能力**（例如把当前告警列表前 20 条结构化带上）：当前设计明确不做，靠工具按权限取。
3. **`context_warning` 是否要升为 `error`**：当前设计是「剔除后继续」，若用户更希望「有任何非法项就中止」需调整。
