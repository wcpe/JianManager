import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from 'react'

/** 
 * 页面上下文的**消费端**（§5.1 / §5.2）。
 *
 * 分层说明（重要）：
 *  - 本文件实现「注册表 + 芯片 + 发送时快照」三件事；
 *  - **各页面调用 `usePageContext` 上报自身上下文**属于自身的实现范围，
 *    尚未接入（状态为未实现）。因此当前产品行为是「没有芯片」——
 *    不是坏掉，而是还没有任何页面声明自己能提供什么。
 *
 * 为什么先把消费端做掉：`SubmitRun` 的 `contextSnapshot` 字段与芯片 UI 都是
 *的一部分，先落地可以让接入时只写「注册」那一半，
 * 不必再回头改传输与界面。
 *
 * 三条不可违反的约束：
 *  1. **只收类型化引用**（`{type, id}` + 少量枚举/时间窗），不收实体快照内容
 *     ——名称、状态由后端按权限回填，前端传的名字不是凭据。
 *  2. **移除即不再外发**：芯片数组就是外发内容，不保留任何「隐藏背景」。
 *  3. **发送时冻结**：快照序列化发生在 send 那一刻，之后换页只影响下一轮。
 */

/** 实体上限（spec §4.1：至多 8 个）。 */
export const PAGE_CONTEXT_MAX_ENTITIES = 8
/** filters 单个值上限（spec §4.1：截断至 64 字符）。 */
export const PAGE_CONTEXT_MAX_FILTER_LEN = 64
/** 
 * 敏感筛选键的片段黑名单（与后端 assistant_context.go 同口径）。
 * 按键名子串匹配：实际键名有 apiKey / access_token / userPassword 等各种写法。
 */
export const PAGE_CONTEXT_SENSITIVE_MARKERS = [
  'token', 'secret', 'password', 'passwd', 'credential', 'apikey', 'api_key',
  'privatekey', 'private_key', 'authorization', 'cookie', 'sessionid', 'session_id',
  'accesskey', 'access_key', 'signature', 'bearer',
]


/** 
 * 敏感筛选**值**的形态判据（spec §8「按键名黑名单 + 值形态双重判定」）。
 *
 * 只有键名黑名单拦不住这类外发：`?q=eyJhbGciOi...`——键名 `q` 完全无害，
 * 值是模型/用户从别处粘来的 JWT 或密钥。键名判定对它是瞎的。
 *
 * 【判据刻意保守】只认**结构上明确无误**的几类，不做「高熵随机串」这种模糊猜测：
 * 误杀的代价是把正常筛选条件丢掉（用户看到「筛选没生效」却查不出原因），
 * 漏杀的代价是这一次外发多带一个值——两者都不轻，但前者更难排查。
 * 所以宁可只抓有固定前缀/固定结构的。
 *
 * 与后端 `assistantContextFilterValueLooksSecret`（assistant_context.go）**逐条对齐**：
 * 前端丢弃是为了不外发，后端丢弃是最后一道闸——任何一侧放宽都会让另一侧形同虚设。
 */
export const PAGE_CONTEXT_SECRET_VALUE_PATTERNS: readonly RegExp[] = [
  // JWT：header.payload.signature，三段 base64url（header 必然以 eyJ 开头）
  /^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}$/,
  // OpenAI 风格的 sk-/pk-/rk- 密钥
  /^(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}$/i,
  // GitHub token（ghp_/gho_/ghu_/ghs_/ghr_ 与细粒度 PAT）
  /^gh[pousr]_[A-Za-z0-9]{16,}$/,
  /^github_pat_[A-Za-z0-9_]{20,}$/,
  // AWS access key id
  /^AKIA[0-9A-Z]{16}$/,
  // Slack token
  /^xox[baprs]-[A-Za-z0-9-]{10,}$/,
  // 显式 Bearer 头（值是完整授权串）
  /^Bearer\s+\S{16,}$/i,
  // 纯十六进制摘要（md5=32 / sha1=40 / sha256=64）。
  // uuid 含连字符，不会命中——实例/节点 id 常是 uuid，不能误杀。
  /^[0-9a-f]{32,}$/i,
]

/** 
 * 判断一个筛选**值**是否形似凭据。
 *
 * 注意：必须在**截断之前**用原值判断——JWT 动辄上百字符，截到 64 会把结构截断，
 * 之后再去匹配必然失配（这类「先裁剪再校验」的顺序错误会让整个判据静默失效）。
 */
export function pageContextValueLooksSecret(value: string): boolean {
  const v = value.trim()
  if (v === '') return false
  return PAGE_CONTEXT_SECRET_VALUE_PATTERNS.some((re) => re.test(v))
}
/** 允许作为上下文的实体类型（白名单，与spec §4 对齐）。 */
export const PAGE_CONTEXT_ENTITY_TYPES = ['instance', 'node', 'task', 'alert', 'player'] as const
export type PageContextEntityType = (typeof PAGE_CONTEXT_ENTITY_TYPES)[number]

/** 一个类型化实体引用。**只有 id，不含名称**（名称不是凭据，也不该外发）。 */
export interface PageContextEntityRef {
  type: PageContextEntityType
  /** 
   * 实体标识。**允许字符串**：实例/节点深链用的是 uuid，数字主键则来自列表行。
   * 统一在序列化时转成字符串——后端的契约也是「string | number 皆可」。
   */
  id: string | number
}

/** 页面上报的上下文（全部可从 props/URL 直接得到，不遍历 DOM、不读内部 state）。 */
export interface PageContextInput {
  /** 页面标识（白名单页才有意义，如 `instances`）。 */
  pageKey: string
  /** 当前页签（可选）。 */
  tab?: string
  /** 实体引用（可选）。 */
  entities?: PageContextEntityRef[]
  /** 枚举型筛选（可选；**不得**塞入自由文本 query）。 */
  filters?: Record<string, string>
  /** 时间窗（可选，如 `24h`）。 */
  timeRange?: string
}

/** 
 * 外发快照的形状（序列化进 `run.context_snapshot`）。
 *
 * `v` 是契约版本：后端将来要按版本解释字段，没有它就只能靠猜。
 */
export interface PageContextSnapshot {
  v: 1
  pageKey: string
  tab?: string
  entities: PageContextEntityRef[]
  filters?: Record<string, string>
  timeRange?: string
}

/** 芯片的可读标签（纯展示；不参与外发）。 */
export interface ContextChip {
  key: string
  label: string
  onRemove: () => void
  /** 
   * 该芯片对应的实体（仅实体类芯片有）。
   *
   * 芯片据此展开指标摘要与「打开详情」入口。刻意不把 id 塞进 label 再反解：
   * label 已本地化且可能被改文案，反解 id 是「一改文案就坏」的做法。
   */
  entity?: PageContextEntityRef
}

// ── 全局注册表（页面 → 助手） ──────────────────────────────────────────

/** 
 * 当前活跃页面的上下文。
 *
 * 用模块级 store + `useSyncExternalStore` 而不是 React Context：
 * 上报方（任意页面）与消费方（浮窗，挂在壳层）**不在同一子树**，
 * Context 传不过去；而模块级单例天然跨树，且卸载即清空。
 */
let current: PageContextInput | null = null
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

/** 页面调用：声明「我这一页能提供什么」。返回清理函数（卸载时自动清空）。 */
export function setPageContext(next: PageContextInput | null): void {
  current = next
  emit()
}

/** 读取当前页面上下文（供非 React 场景，如 send 内序列化）。 */
export function getPageContext(): PageContextInput | null {
  return current
}

/** 订阅变化（React 绑定见 `usePageContextValue`）。 */
export function subscribePageContext(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** 消费端：当前页面上下文（浮窗/首页用）。 */
export function usePageContextValue(): PageContextInput | null {
  return useSyncExternalStore(subscribePageContext, getPageContext, () => null)
}

/** 
 * 页面侧 hook（供接入时使用）。
 *
 * 目前产品里没有调用方——各页面尚未注册（未实现）。
 * 放在这里而不是另建文件，是为了让「注册契约」与「消费实现」在同一处可对照，
 * 接入时不必在两份文件之间来回猜字段含义。
 */
export function usePageContext(input: PageContextInput | null): void {
  // 依赖用序列化串：调用方常传字面量对象，直接依赖 input 会每次渲染都上报一次，
  // 进而触发订阅方重渲染 → 再上报 → 循环。
  const dep = JSON.stringify(input ?? null)
  // 在 effect 里上报（渲染期写外部 store 会在并发渲染下提前生效，
  // 而那次渲染可能被 React 丢弃，留下一个「本不该存在」的上下文）。
  useEffect(() => {
    setPageContext(dep === 'null' ? null : (JSON.parse(dep) as PageContextInput))
    // 卸载/换依赖时清空：页面卸载即不留残影（spec §5.1 末条）。
    return () => setPageContext(null)
  }, [dep])
}

// ── 芯片与快照 ────────────────────────────────────────────────────────

const ENTITY_LABEL: Record<PageContextEntityType, string> = {
  instance: '实例',
  node: '节点',
  task: '任务',
  alert: '告警',
  player: '玩家',
}

/** 
 * 白名单页键 → 展示名（spec §4.2）。
 *
 * 键名必须与后端 `assistantContextPageKeys` 完全一致：路由是 `/monitor`
 * （不是 `/monitoring`，后者只是重定向），因此这里的键也用 `monitor`。
 * 未列入的页面（settings / permissions / users…）不提供上下文。
 */
export const PAGE_CONTEXT_PAGE_KEYS = [
  'instances',
  'nodes',
  'logs',
  'monitor',
  'alerts',
  'tasks',
] as const
export type PageContextPageKey = (typeof PAGE_CONTEXT_PAGE_KEYS)[number]

const PAGE_LABEL: Record<string, string> = {
  instances: '实例',
  nodes: '节点',
  logs: '日志',
  monitor: '监控',
  alerts: '告警',
  tasks: '任务',
}

/** 
 * 把页面上下文转成可移除的芯片。
 *
 * 每条实体单独一个芯片（可单独移除）；页签/时间窗/筛选各一个。
 * 移除通过 `onChange` 回写「剩余项」，因此调用方拿到的就是最终外发内容。
 */
export function useContextChips(
  value: PageContextInput | null,
  onChange: (next: PageContextInput | null) => void,
): ContextChip[] {
  const [removed, setRemoved] = useState<Set<string>>(new Set())

  return useMemo(() => {
    if (!value) return []
    const chips: ContextChip[] = []
    const pageLabel = PAGE_LABEL[value.pageKey] ?? value.pageKey
    chips.push({
      key: `page:${value.pageKey}`,
      label: `当前页：${pageLabel}`,
      onRemove: () => {
        // 移除「当前页」等价于整页上下文不再外发（保留手动添加的实体也没有意义）。
        onChange(null)
      },
    })
    if (value.tab) {
      chips.push({
        key: `tab:${value.tab}`,
        label: `页签：${value.tab}`,
        onRemove: () => onChange({ ...value, tab: undefined }),
      })
    }
    if (value.timeRange) {
      chips.push({
        key: `time:${value.timeRange}`,
        label: `时间窗：${value.timeRange}`,
        onRemove: () => onChange({ ...value, timeRange: undefined }),
      })
    }
    for (const [k, v] of Object.entries(value.filters ?? {})) {
      if (removed.has(`filter:${k}`)) continue
      chips.push({
        key: `filter:${k}`,
        label: `${k}：${v}`,
        onRemove: () => {
          const nextFilters = { ...(value.filters ?? {}) }
          delete nextFilters[k]
          setRemoved((prev) => new Set(prev).add(`filter:${k}`))
          onChange({ ...value, filters: Object.keys(nextFilters).length ? nextFilters : undefined })
        },
      })
    }
    for (const ent of value.entities ?? []) {
      if (removed.has(`ent:${ent.type}:${ent.id}`)) continue
      chips.push({
        key: `ent:${ent.type}:${ent.id}`,
        label: `${ENTITY_LABEL[ent.type]}：${ent.type}-${ent.id}`,
        // 带上实体引用：芯片要据此就地展开指标摘要与「打开详情」入口。
        // 只传引用不传展示文案——文案由芯片自行 i18n，避免这里硬编码中文。
        entity: ent,
        onRemove: () => {
          setRemoved((prev) => new Set(prev).add(`ent:${ent.type}:${ent.id}`))
          onChange({
            ...value,
            entities: (value.entities ?? []).filter((e) => !(e.type === ent.type && e.id === ent.id)),
          })
        },
      })
    }
    return chips
  }, [onChange, removed, value])
}

/** 
 * 序列化外发快照（**发送时调用一次即冻结**）。
 *
 * 没有上下文时返回 null 而不是 `"{}"`：后端据此区分「没带上下文」与
 * 「带了空上下文」，前者不会在 run 上留一条无意义的快照。
 */
export function serializePageContext(value: PageContextInput | null): string | null {
  if (!value) return null

  // 前端也做一遍上限裁剪（spec §4.1 / §6）。后端会再校验一次，但前端先裁有两个好处：
  // 用户当场就能从芯片上看到「只带前 8 个」，而不是发出后才收到 context_warning；
  // 也避免为了一个必然被裁的字段撑大请求体。
  const entities = (value.entities ?? [])
    .slice(0, PAGE_CONTEXT_MAX_ENTITIES)
    .filter((e) => String(e.id).trim() !== '')
    // 敏感键/超长值在序列化时丢弃（键名判定与后端同口径，见 assistant_context.go）。
    .map((e) => ({ type: e.type, id: String(e.id) }))

  const filters: Record<string, string> = {}
  for (const [k, v] of Object.entries(value.filters ?? {})) {
    if (!k.trim() || PAGE_CONTEXT_SENSITIVE_MARKERS.some((m) => k.toLowerCase().includes(m))) continue
    // 值形态判定（spec §8 双重判定）：键名无害但值像凭据（如 ?q=eyJhbGci...）也要丢。
    // 必须在 slice **之前**判断——JWT 上百字符，截断会破坏三段结构让匹配失配。
    if (pageContextValueLooksSecret(v)) continue
    filters[k] = v.slice(0, PAGE_CONTEXT_MAX_FILTER_LEN)
  }

  const snapshot: PageContextSnapshot = {
    v: 1,
    pageKey: value.pageKey,
    entities,
    ...(value.tab ? { tab: value.tab } : {}),
    ...(Object.keys(filters).length ? { filters } : {}),
    ...(value.timeRange ? { timeRange: value.timeRange } : {}),
  }
  // 只剩 pageKey、没有任何实体/筛选/时间窗/页签时，外发它没有信息量：回 null。
  if (snapshot.entities.length === 0 && !snapshot.tab && Object.keys(filters).length === 0 && !snapshot.timeRange) {
    return null
  }
  const json = JSON.stringify(snapshot)
  // 超 4 KiB 时**不截断**，交由后端明确拒收（CONTEXT_TOO_LARGE）：
  // 前端悄悄裁剪会让用户以为上下文完整送达（spec §6 末条）。
  return json
}

/** 供容器使用：把「当前上下文 + 本地移除状态」转成芯片与快照构造器。 */
export function usePageContextChips(): {
  chips: ContextChip[]
  /** 当前生效的上下文（含手动添加的实体），供面板做去重与上限判定。 */
  entities: PageContextEntityRef[]
  /** 手动追加一个实体（§5.3：与自动上下文同权，可单独移除）。 */
  addEntity: (entity: PageContextEntityRef) => void
  buildSnapshot: () => string | null
} {
  const live = usePageContextValue()
  const [override, setOverride] = useState<PageContextInput | null | undefined>(undefined)
  const effective = override === undefined ? live : override
  const chips = useContextChips(effective, setOverride)
  const buildSnapshot = useCallback(() => serializePageContext(effective), [effective])

  /** 
   * 追加实体。
   *
   * 没有页面上下文时也要能加：此时以「当前页」为锚点建一个最小上下文。
   * 若因为没有 pageKey 就拒绝添加，用户会以为按钮坏了。
   */
  const addEntity = useCallback(
    (entity: PageContextEntityRef) => {
      const base: PageContextInput = effective ?? { pageKey: 'instances', entities: [] }
      const list = base.entities ?? []
      if (list.some((e) => e.type === entity.type && String(e.id) === String(entity.id))) return
      if (list.length >= PAGE_CONTEXT_MAX_ENTITIES) return
      setOverride({ ...base, entities: [...list, entity] })
    },
    [effective],
  )

  return { chips, entities: effective?.entities ?? [], addEntity, buildSnapshot }
}
