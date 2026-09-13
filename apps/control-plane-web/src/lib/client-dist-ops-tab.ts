/**
 * 页面 B「客户端分发运维」（`/client-dist-ops`）Tab 归一化（FR-430 / ADR-088）。
 *
 * 两条旧路由（`/client-dist-security`、`/client-dist-monitor`）保留为透传 query 的
 * 参数翻译重定向：读取原始 `tab` → 归一化为新 7 Tab 体系（+ `seg`/`type` 派生），
 * 再由 `ClientDistRedirect` 用 `buildClientDistHref` 落 `/client-dist-ops`。
 *
 * 同一函数亦服务页面 B 的「旧 key 兜底」：直接访问
 * `/client-dist-ops?tab=events|ip|players|groups` 时归一化到对应 Tab/分档，
 * 同时放行全部新 canonical Tab 值。纯函数，便于单测。
 */

/** 页面 B 正式 7 Tab（顺序即 UI 顺序）。 */
export const OPS_TABS = [
  'overview',
  'statistics',
  'monitor',
  'logs',
  'clients',
  'profiles',
  'actions',
] as const

export type OpsTab = (typeof OPS_TABS)[number]

/** Tab 内分档（`seg`）：缺省项由 {@link DEFAULT_SEG_BY_TAB} 决定。 */
export const OPS_SEGS = ['live', 'events', 'client', 'ip', 'player', 'actions', 'groups'] as const

export type OpsSeg = (typeof OPS_SEGS)[number]

/** 重定向来源：security=旧安全页；monitor=旧监控页。 */
export type OpsSource = 'security' | 'monitor'

/** 归一化结果：canonical Tab + 可选分档（`seg`）/日志类型（`type`）。 */
export interface NormalizedOpsTab {
  tab: OpsTab
  seg?: OpsSeg
  type?: string
}

/** 各 Tab 的缺省分档（未显式给 `seg` 时页面 B 采用）。 */
export const DEFAULT_SEG_BY_TAB: Partial<Record<OpsTab, OpsSeg>> = {
  monitor: 'live',
  profiles: 'client',
  actions: 'actions',
}

/** 来源路由的 landing Tab（缺失/非法 `tab` 时回落到此）。 */
const DEFAULT_TAB_BY_SOURCE: Record<OpsSource, OpsTab> = {
  security: 'overview',
  monitor: 'statistics',
}

/** `/client-dist-security` 旧 tab → 新 Tab/分档。 */
const SECURITY_TAB_MAP: Record<string, NormalizedOpsTab> = {
  overview: { tab: 'overview' },
  events: { tab: 'monitor', seg: 'events' },
  logs: { tab: 'logs' },
  profiles: { tab: 'profiles', seg: 'client' },
  ip: { tab: 'profiles', seg: 'ip' },
  players: { tab: 'profiles', seg: 'player' },
  actions: { tab: 'actions', seg: 'actions' },
  groups: { tab: 'actions', seg: 'groups' },
}

/** `/client-dist-monitor` 旧 tab → 新 Tab/分档；`logs` 缺省补 `type=request`。 */
const MONITOR_TAB_MAP: Record<string, NormalizedOpsTab> = {
  statistics: { tab: 'statistics' },
  monitor: { tab: 'monitor' },
  logs: { tab: 'logs', type: 'request' },
  clients: { tab: 'clients' },
}

/** 判断字符串是否为新体系 canonical Tab。 */
export function isOpsTab(value: string): value is OpsTab {
  return (OPS_TABS as readonly string[]).includes(value)
}

/** 判断字符串是否为合法分档值。 */
export function isOpsSeg(value: string): value is OpsSeg {
  return (OPS_SEGS as readonly string[]).includes(value)
}

/**
 * 归一化旧 `tab` 值为新 Tab 体系。
 *
 * - `source` 决定缺失/非法时的 landing（security→overview，monitor→statistics）。
 * - 先查来源专属旧 key 映射；命中即返回对应 `{ tab, seg?, type? }`。
 * - 未命中但本身已是 canonical Tab → 透传（兼容页面 B 直达合法值）。
 * - 其余（缺失/非法）→ 来源 landing Tab。
 */
export function normalizeOpsTab(
  raw: string | null | undefined,
  source: OpsSource,
): NormalizedOpsTab {
  const key = (raw ?? '').trim()
  const map = source === 'monitor' ? MONITOR_TAB_MAP : SECURITY_TAB_MAP
  const hit = map[key]
  if (hit) return hit
  if (isOpsTab(key)) return { tab: key }
  return { tab: DEFAULT_TAB_BY_SOURCE[source] }
}

/**
 * 解析页面 B 的实际分档：显式 `seg` 合法值 > 归一化别名派生的 `seg` > Tab 缺省。
 * 非法 `seg` 一律回落到 Tab 缺省（无缺省则该 Tab 无分档）。
 */
export function resolveOpsSeg(
  rawSeg: string | null | undefined,
  tab: OpsTab,
  aliasSeg?: OpsSeg,
): OpsSeg | undefined {
  const key = (rawSeg ?? '').trim()
  if (isOpsSeg(key)) return key
  if (aliasSeg) return aliasSeg
  return DEFAULT_SEG_BY_TAB[tab]
}
