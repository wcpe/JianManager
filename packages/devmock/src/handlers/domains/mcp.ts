import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'

/**
 * MCP 活动域 mock（FR-391 / ADR-096 决策 6）：`GET /agent/mcp/activity`。
 *
 * 本 handler 的**重点不是「能返回 200」，而是复刻真后端的 window 校验语义**
 * （`internal/controlplane/mcp/handler.go` 的 `ListActivity` + `minActivityWindow`/`maxActivityWindow`）。
 * 若 mock 对任何 window 都答应，前端把档位写成 `7d` 这类非法值仍能拿到 200，
 * mock 模式下就看不见缺陷——`7d` 正是这样漏到真机才暴露的。
 * 因此这里非法形态与越界区间都必须回 400，让 mock 模式下的 e2e 成为真正的守卫。
 */

export interface McpActivityItem {
  tokenId: number
  tokenName: string
  tokenPrefix: string
  /** RFC3339 时间戳（后端 time.Time 序列化形态）。 */
  lastActivityAt: string
  lastAction: string
  callCount: number
  failureCount: number
  clientIPs: string[]
  clients: Record<string, number>
}

export interface McpActivityResponse {
  /** 实际生效窗口的 Go duration 字符串形态（如 `24h0m0s`），不是入参原样回显。 */
  window: string
  generatedAt: string
  items: McpActivityItem[]
}

/** 后端默认窗口 24h（缺省 window 时生效）。 */
const DEFAULT_WINDOW_SECONDS = 24 * 60 * 60
/** 允许区间闭区间 1h~168h，对齐 handler.go 的 minActivityWindow / maxActivityWindow。 */
const MIN_WINDOW_SECONDS = 60 * 60
const MAX_WINDOW_SECONDS = 168 * 60 * 60

const UNIT_NS: Record<string, number> = {
  ns: 1,
  us: 1e3,
  'µs': 1e3,
  'μs': 1e3,
  ms: 1e6,
  s: 1e9,
  m: 60e9,
  h: 3600e9,
}

/**
 * 按 Go `time.ParseDuration` 的文法解析时长，返回秒数；不合法返回 null。
 * 文法：可选正负号 + 一段或多段「十进制数 + 单位」，单位限 ns/us/µs/μs/ms/s/m/h。
 * 关键差异：Go duration **没有「天」**，`7d` 命中未知单位 `d` → 非法；
 * `24`（缺单位）、`abc` 同样非法。
 */
function parseGoDurationSeconds(value: string): number | null {
  // "0"（含带符号）是 Go 唯一允许省略单位的形式。
  if (/^[+-]?0$/.test(value)) return 0
  const whole = /^([+-]?)((?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+$/.exec(value)
  if (!whole) return null
  let ns = 0
  for (const seg of whole[2].matchAll(/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)) {
    ns += Number(seg[1]) * UNIT_NS[seg[2]]
  }
  return whole[1] === '-' ? -ns / 1e9 : ns / 1e9
}

/** 复刻 Go `time.Duration.String()`：有小时则 `XhYmZs`，否则退到 `YmZs` / `Zs`。 */
function formatGoDuration(totalSeconds: number): string {
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h${minutes}m${seconds}s`
  if (minutes > 0) return `${minutes}m${seconds}s`
  return `${seconds}s`
}

interface ActivitySeed extends Omit<McpActivityItem, 'lastActivityAt'> {
  /** 距今的分钟数：既生成可信时间戳，也让行随窗口变化（窗口外不该出现）。 */
  minutesAgo: number
}

// 来源 IP 用文档保留网段（RFC 5737 TEST-NET）+ 回环，避免看起来像真实地址。
const SEEDS: ActivitySeed[] = [
  {
    tokenId: 1,
    tokenName: 'claude-code-dev',
    tokenPrefix: 'jmat_7f3a',
    minutesAgo: 3,
    lastAction: 'tools/call agent_whoami',
    callCount: 428,
    failureCount: 6,
    clientIPs: ['203.0.113.24'],
    clients: { 'claude-code': 402, curl: 26 },
  },
  {
    tokenId: 2,
    tokenName: 'ci-nightly',
    tokenPrefix: 'jmat_2b91',
    minutesAgo: 42,
    lastAction: 'resources/list',
    callCount: 57,
    failureCount: 0,
    clientIPs: ['198.51.100.7'],
    clients: { 'ci-runner': 57 },
  },
  {
    // 72 小时前活跃：只在 168h 窗口内命中，用来验证切档确实换了统计窗口。
    tokenId: 3,
    tokenName: 'ops-laptop',
    tokenPrefix: 'jmat_c4e8',
    minutesAgo: 72 * 60,
    lastAction: 'tools/call instance_list',
    callCount: 9,
    failureCount: 3,
    clientIPs: ['127.0.0.1'],
    clients: { 'mcp-inspector': 9 },
  },
]

export const handlers = [
  // 权限门：真后端为 JWT + `agent.mcp.read`（默认只授平台管理员），非管理员回 403——复用平台管理员守卫。
  domainRoute('get', '/agent/mcp/activity', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied

    const raw = new URL(info.request.url).searchParams.get('window') ?? ''
    let windowSeconds = DEFAULT_WINDOW_SECONDS
    if (raw !== '') {
      const parsed = parseGoDurationSeconds(raw)
      if (parsed === null) {
        return HttpResponse.json(
          { error: 'BAD_REQUEST', message: 'window 须为时长字符串（如 24h）' },
          { status: 400 },
        )
      }
      if (parsed < MIN_WINDOW_SECONDS || parsed > MAX_WINDOW_SECONDS) {
        return HttpResponse.json(
          { error: 'BAD_REQUEST', message: 'window 允许区间 1h~168h' },
          { status: 400 },
        )
      }
      windowSeconds = parsed
    }

    const now = Date.now()
    const items = SEEDS.filter((seed) => seed.minutesAgo * 60 <= windowSeconds)
      // 契约：按 lastActivityAt 降序（最近活跃在前）。
      .sort((a, b) => a.minutesAgo - b.minutesAgo)
      .map(({ minutesAgo, ...row }) => ({
        ...row,
        lastActivityAt: new Date(now - minutesAgo * 60_000).toISOString(),
      }))

    return HttpResponse.json({
      window: formatGoDuration(windowSeconds),
      generatedAt: new Date(now).toISOString(),
      items,
    } satisfies McpActivityResponse)
  }),
]
