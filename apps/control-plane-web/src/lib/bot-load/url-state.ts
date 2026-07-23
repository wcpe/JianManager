/** Bots 页可寻址 tab。 */
export const BOTS_TABS = ['fleet', 'sessions', 'templates'] as const
export type BotsTab = (typeof BOTS_TABS)[number]

/** 解析 tab 查询参数，非法回退 fleet。 */
export function parseBotsTab(raw: string | null | undefined): BotsTab {
  if (raw && (BOTS_TABS as readonly string[]).includes(raw)) return raw as BotsTab
  return 'fleet'
}

/** 会话列表筛选状态（写 URL 可恢复）。 */
export interface SessionsFilterState {
  q?: string
  instanceId?: number
  runState?: string
  page?: number
}

/** 模板列表筛选状态。 */
export interface TemplatesFilterState {
  q?: string
  tag?: string
  page?: number
}

/** 从 URLSearchParams 读会话筛选。 */
export function readSessionsFilter(params: URLSearchParams): SessionsFilterState {
  const q = params.get('q') || undefined
  const instanceIdRaw = params.get('instanceId')
  const instanceId = instanceIdRaw ? Number(instanceIdRaw) : undefined
  const runState = params.get('runState') || undefined
  const pageRaw = params.get('page')
  const page = pageRaw ? Math.max(1, Number(pageRaw) || 1) : undefined
  return {
    q,
    instanceId: instanceId && Number.isFinite(instanceId) ? instanceId : undefined,
    runState,
    page,
  }
}

/** 从 URLSearchParams 读模板筛选。 */
export function readTemplatesFilter(params: URLSearchParams): TemplatesFilterState {
  const q = params.get('q') || undefined
  const tag = params.get('tag') || undefined
  const pageRaw = params.get('page')
  const page = pageRaw ? Math.max(1, Number(pageRaw) || 1) : undefined
  return { q, tag, page }
}

/** 合并写回 URL 查询（保留其他 key）。 */
export function mergeSearchParams(
  current: URLSearchParams,
  patch: Record<string, string | number | undefined | null>,
): URLSearchParams {
  const next = new URLSearchParams(current)
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === null || v === '') next.delete(k)
    else next.set(k, String(v))
  }
  return next
}
