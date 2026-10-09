export const CLIENT_DIST_QUERY_KEYS = [
  'channelId',
  'ip',
  'machineId',
  'errCode',
  'version',
  'tab',
  // FR-430：页面 B「客户端分发运维」新增冻结键——`type`=全量日志视图（all/request/…），
  // `seg`=Tab 内分档（live/events/client/ip/player/actions/groups）；随旧路由重定向一并透传。
  'type',
  'seg',
  // FR-425：自定义时间区间（RFC3339；与 RangePicker 预设档 range 互斥——设置 from/to 时清 range）。
  'from',
  'to',
] as const

export type ClientDistQueryKey = (typeof CLIENT_DIST_QUERY_KEYS)[number]
export type ClientDistQuery = Partial<Record<ClientDistQueryKey, string>>

export function readClientDistQuery(searchParams: URLSearchParams): ClientDistQuery {
  const query: ClientDistQuery = {}
  for (const key of CLIENT_DIST_QUERY_KEYS) {
    const value = searchParams.get(key)?.trim()
    if (value) query[key] = value
  }

  if (!query.channelId) {
    const legacyChannel = searchParams.get('channel')?.trim()
    if (legacyChannel) query.channelId = legacyChannel
  }
  return query
}

export function updateClientDistQuery(
  searchParams: URLSearchParams,
  patch: Partial<Record<ClientDistQueryKey, string | number | null | undefined>>,
): URLSearchParams {
  const values: ClientDistQuery = { ...readClientDistQuery(searchParams) }
  for (const key of CLIENT_DIST_QUERY_KEYS) {
    if (!(key in patch)) continue
    const value = patch[key]
    if (value === undefined) continue
    if (value === null || value === '') delete values[key]
    else values[key] = String(value)
  }

  const next = new URLSearchParams()
  for (const key of CLIENT_DIST_QUERY_KEYS) {
    if (values[key]) next.set(key, values[key])
  }
  return next
}

export function buildClientDistHref(
  pathname: string,
  searchParams: URLSearchParams,
  patch: Partial<Record<ClientDistQueryKey, string | number | null | undefined>> = {},
): string {
  const query = updateClientDistQuery(searchParams, patch).toString()
  return query ? `${pathname}?${query}` : pathname
}
