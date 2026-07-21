export const CLIENT_DIST_QUERY_KEYS = [
  'channelId',
  'ip',
  'machineId',
  'errCode',
  'version',
  'tab',
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
