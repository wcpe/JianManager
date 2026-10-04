import type { Query, QueryClient, QueryKey } from '@tanstack/react-query'
import { MOCK_POLL_FOLLOW_ORIGINAL, getMockPollInterval } from '@jianmanager/devmock/runtime-control'

/**
 * mock 模式全局轮询控制（FR-496 阶段 6 补丁）。
 *
 * 要解决的问题：mock 下各处的 `refetchInterval`（2s/10s/15s/30s…散在 `src/api/**` 几十个调用点）
 * 不可控——想安静地看一次请求、想验证「轮询会不会带出脏数据」、想把节奏放慢看 loading，
 * 都只能改代码。这里给面板一个全局旋钮，默认**跟随原始**（不接管，等于改造前行为），
 * 需要模拟极端环境时再显式选「关闭 / 1s / 5s / 15s / 60s」统一覆盖。
 *
 * 为什么用「装饰 QueryClient」而不是改 `src/api/**` 的每个调用点（这也正是它的代价）：
 * ① 范围纪律：本补丁允许改动的文件只有 `main.tsx` 与 `components/devtools/**`，不碰 api 层；
 * ② 单一收口：`QueryClient.defaultQueryOptions()` 是**所有**查询（useQuery / useQueries /
 *    infinite / 命令式 fetchQuery）解析最终选项的唯一通道，在这里改写一次即覆盖全部调用点，
 *    将来新增的 `refetchInterval` 自动纳入，不会漏；
 * ③ 语义安全：只改写「解析后本来就在轮询」的查询——`false`/`undefined` 保持原样，
 *    绝不会把一次性查询变成轮询，也不会把关闭的轮询打开。
 * 后续若要收编进正规实现，替代方案是 api 层统一取一个 `pollInterval()` 帮助函数（需改 api/**）。
 */

/** 查询 key → 展示 / 过滤用的标签（取前两段，如 `['instances','search',{…}]` → `instances/search`）。 */
export function pollQueryLabel(queryKey: QueryKey | undefined): string {
  const parts = (Array.isArray(queryKey) ? queryKey : [queryKey])
    .filter((part): part is string | number => typeof part === 'string' || typeof part === 'number')
    .slice(0, 2)
  return parts.length > 0 ? parts.join('/') : '(无 key)'
}

/** 轮询间隔的可写形式：静态毫秒值或「按查询状态决定」的函数。 */
type RefetchIntervalOption = number | false | ((query: Query) => number | false | undefined)

/** 装饰后留在选项上的标记：原始 `refetchInterval` 是否非空（用于「哪些查询本来在轮询」）。 */
interface PollingMarked {
  /** 真值 = 该查询原本配置了轮询（黑名单 UI 只列这些，避免把一次性查询也列出来误导人）。 */
  __mockPollConfigured?: boolean
}

/**
 * 只看 queryKey / refetchInterval 两项的窄化签名。
 * 库里的 `defaultQueryOptions` 带四个泛型参数，装饰它的目的仅此两项，故局部窄化 + 一次显式断言。
 */
type PollableQueryOptions = { queryKey?: QueryKey; refetchInterval?: RefetchIntervalOption } & PollingMarked
type PatchedDefaultQueryOptions = (options?: unknown) => PollableQueryOptions

let client: QueryClient | null = null
let include: string[] = []
const excluded = new Set<string>()
let listener: (() => void) | null = null
/** 上一次广播的标签集合签名：缓存事件很密，只有集合真变了才通知订阅方。 */
let lastSignature = ''

function notify(): void {
  listener?.()
}

/**
 * 安装轮询控制（main.tsx 在 mock 模式下调用一次）。
 * @param queryClient 应用唯一的 QueryClient
 * @param scope 白名单 / 黑名单，按 `pollQueryLabel` 的标签做前缀匹配；
 *              白名单非空时「只在这些查询上生效」，黑名单始终优先剔除。
 */
export function installMockPollingControl(
  queryClient: QueryClient,
  scope: { include?: string[]; exclude?: string[] } = {},
): void {
  client = queryClient
  include = scope.include ?? []
  excluded.clear()
  for (const label of scope.exclude ?? []) excluded.add(label)

  // 只装饰一次：HMR / 重复调用时避免把包装套成多层（每层都多一次调用与标记写入）
  const target = queryClient as QueryClient & { __mockPollingInstalled?: boolean }
  if (target.__mockPollingInstalled) return
  target.__mockPollingInstalled = true

  const original = queryClient.defaultQueryOptions.bind(queryClient) as unknown as PatchedDefaultQueryOptions
  const patched: PatchedDefaultQueryOptions = (options) => {
    const merged = original(options)
    const configured = merged.refetchInterval
    if (configured === undefined || configured === false) return merged

    merged.__mockPollConfigured = true
    const parsedKey = merged.queryKey

    /**
     * 一律改写成**函数**形式（哪怕原值是静态毫秒数），一切都在排期时现算：
     * TanStack 每次给查询排下一次轮询都会调这个函数，所以改档位 / 点掉芯片立刻生效；
     * 若改成「解析时算好的静态值」，值会被写进 options 对象冻住——之后的档位变化只能等
     * 该查询自己重新解析（重新挂载 / 重新渲染）才追得上，表现为「关了轮询还在轮询」。
     *
     * 注意「跟随原始」档并不是「不装装饰器」：仍然包这一层，只是返回值与原值完全一致
     * （静态值原样返回、条件函数原样转调），排期行为与改造前逐字相同——包这层的唯一目的是
     * 让用户随后切到具体档位时能立刻生效，而不是被冻在旧值上。
     */
    merged.refetchInterval = (query) => {
      // 原值先算出来：静态值是它自己，条件函数按当前查询状态求值（只调一次，语义同 TanStack 原生）
      const originalValue = typeof configured === 'function' ? configured(query) : configured
      const override = getMockPollInterval()

      // 跟随原始 = 不干预：完全按 api 层各自的值走（默认档，等于改造前行为）
      if (override === MOCK_POLL_FOLLOW_ORIGINAL) return originalValue
      // 条件不成立（如「只在任务 running 时轮询」）时仍然不轮询：
      // 显式覆盖档位也不能把条件轮询变成无条件轮询
      if (!originalValue) return false
      // 白名单外的查询保持自己的间隔
      if (!isCovered(pollQueryLabel(query?.queryKey ?? parsedKey))) return originalValue
      return override > 0 ? override : false
    }
    return merged
  }
  ;(queryClient as unknown as { defaultQueryOptions: PatchedDefaultQueryOptions }).defaultQueryOptions = patched

  // 面板要列出「哪些查询在轮询」，而集合随页面切换增减。这里订阅查询缓存，但只在
  // **标签集合真的变了**时才通知面板——轮询下缓存事件每秒都来一堆，直接透传会把面板刷爆。
  queryClient.getQueryCache().subscribe(() => {
    const signature = listPollingQueryLabels().join('|')
    if (signature === lastSignature) return
    lastSignature = signature
    notify()
  })
}

/** 该标签是否在作用范围内（白名单优先、黑名单剔除）。 */
function isCovered(label: string): boolean {
  if (matchesAny(excluded, label)) return false
  if (include.length > 0) return include.some((entry) => entry && label.startsWith(entry))
  return true
}

function matchesAny(set: Set<string>, label: string): boolean {
  for (const entry of set) {
    if (entry && label.startsWith(entry)) return true
  }
  return false
}

/** 当前被面板接管的轮询查询标签（去重、稳定顺序），供面板展示与黑白名单选择。 */
export function listPollingQueryLabels(): string[] {
  if (!client) return []
  const labels = new Set<string>()
  for (const query of client.getQueryCache().getAll()) {
    const options = query.options as PollableQueryOptions
    if (options.__mockPollConfigured && options.queryKey) labels.add(pollQueryLabel(options.queryKey))
  }
  return [...labels].sort()
}

/** 某标签当前是否被排除（黑名单里）。 */
export function isPollingQueryExcluded(label: string): boolean {
  return matchesAny(excluded, label)
}

/**
 * 面板「作用范围」用的完整快照：标签 + 是否被排除。
 * 必须把排除状态一起放进快照——只列标签的话，点掉一个芯片时集合没变，
 * 订阅方（useSyncExternalStore）拿到相同快照就不会重渲染，界面会停在旧状态。
 */
export function listPollingQueryScopes(): Array<{ label: string; excluded: boolean }> {
  return listPollingQueryLabels().map((label) => ({ label, excluded: isPollingQueryExcluded(label) }))
}

/** 让所有活动查询立刻按新间隔重新排期（改档位 / 改范围后调用）。 */
export function refreshMockPolling(): void {
  if (!client) return
  // 只重取「本来在轮询」的查询：TanStack 会在重取完成后按当前 refetchInterval 重新排期，
  // 因此这是「改完立刻生效」的入口；一次性查询不动，避免改档位时顺带触发一堆无关请求。
  void client.invalidateQueries({
    predicate: (query) => Boolean((query.options as PollingMarked).__mockPollConfigured),
  })
  notify()
}

/**
 * 切换某标签的纳入 / 排除（面板芯片点击）。改完立刻 `refreshMockPolling()`，
 * 否则要等下一次排期（最长 60s）才看得出变化。
 */
export function togglePollingQuery(label: string): void {
  if (excluded.has(label)) excluded.delete(label)
  else excluded.add(label)
  persistExcluded()
  refreshMockPolling()
}

/** 订阅轮询查询集合的变化（面板用它触发重渲染）。返回取消订阅函数。 */
export function subscribeMockPolling(fn: () => void): () => void {
  listener = fn
  return () => {
    if (listener === fn) listener = null
  }
}

const EXCLUDE_STORAGE_KEY = 'jm.devmock.pollExclude'

/** 读回面板上次的黑名单（刷新后保持调试现场）。 */
export function loadPersistedPollingExclude(): string[] {
  try {
    const raw = window.localStorage.getItem(EXCLUDE_STORAGE_KEY)
    const parsed: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((entry): entry is string => typeof entry === 'string') : []
  } catch {
    return []
  }
}

function persistExcluded(): void {
  try {
    window.localStorage.setItem(EXCLUDE_STORAGE_KEY, JSON.stringify([...excluded]))
  } catch {
    /* 持久化失败不影响本次会话内的过滤 */
  }
}
