import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
import zh from '@/i18n/zh.json'
import en from '@/i18n/en.json'
import McpActivityPage, { WINDOW_PRESETS } from './McpActivityPage'

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
}

const makeToken = (userId: number, username: string, role: number) =>
  `mock.${btoa(JSON.stringify({ userId, username, role, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`

function login(role: number) {
  const token = makeToken(1, role === 10 ? 'admin' : 'member', role)
  useAuthStore.getState().login(token, 'r-1')
}

const ACTIVITY_URL = '/agent/mcp/activity'

// mock 响应里的 window 字段按真后端回显形态填：后端用 Go duration 的 String() 回显
// （入参 '24h' → 响应 '24h0m0s'、'168h' → '168h0m0s'；真后端单测 mcp_test.go 与
// docs/specs/mcp-stateless-endpoint/api.md 都是这个口径），并非入参原样。
// 该字段当前页面不渲染，但 mock 不能比真契约宽松：否则将来一旦把它渲染出来，
// 测试仍会绿而线上显示的是 '168h0m0s'。
// 注意：请求侧断言（params.window）仍是入参原样 '24h' / '168h'，不要跟着改成 duration 形态。

/** 统计窗口档位按钮所在分组（分组名取自 i18n 的 mcpActivity.window）。 */
const windowGroup = () => screen.getByRole('group', { name: /统计窗口|Window/ })

// 档位值原样进 URL 的 window 参数，后端按 Go duration 解析并限定 1h~168h。
// 这层契约单测必须自己兜住：否则「加一档 + 补 i18n」就能让测试全绿，只在真机被 400 拒绝
// （`7d` 就是这么漏出去的）。下面用最小解析器复刻后端语义，不引库。
const GO_DURATION_RE = /^(\d+[hms])+$/
const ONE_HOUR_SECONDS = 3600
const MAX_WINDOW_SECONDS = 168 * ONE_HOUR_SECONDS

/** 按 Go duration 语义累加 h/m/s 得到秒数；形态不合法返回 NaN。 */
function parseGoDurationSeconds(value: string): number {
  if (!GO_DURATION_RE.test(value)) return NaN
  let seconds = 0
  for (const [, amount, unit] of value.matchAll(/(\d+)([hms])/g)) {
    seconds += Number(amount) * (unit === 'h' ? 3600 : unit === 'm' ? 60 : 1)
  }
  return seconds
}

/** 把语言包扁平化成「点分 key → 叶子值」，口径同 src/i18n/missing-keys.test.ts 的 flattenKeys。 */
function flattenLeaves(
  obj: Record<string, unknown>,
  prefix = '',
  out = new Map<string, unknown>(),
): Map<string, unknown> {
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') flattenLeaves(v as Record<string, unknown>, path, out)
    else out.set(path, v)
  }
  return out
}

const ZH_LEAVES = flattenLeaves(zh)
const EN_LEAVES = flattenLeaves(en)

describe('McpActivityPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('非平台管理员显示无权限', () => {
    login(0)
    renderWithProviders(<McpActivityPage />)
    expect(screen.getByText(/仅平台管理员|Platform admin/i)).toBeInTheDocument()
    expect(mockedApi.get).not.toHaveBeenCalled()
  })

  it('管理员按 Token 聚合渲染活动行（默认 24h 窗口）', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === ACTIVITY_URL) {
        return {
          data: {
            window: '24h0m0s',
            generatedAt: '2026-07-25T06:05:00Z',
            items: [
              {
                tokenId: 1,
                tokenName: 'ci-bot',
                tokenPrefix: 'jmat_ab12',
                lastActivityAt: '2026-07-25T06:05:00Z',
                lastAction: 'agent.whoami',
                callCount: 128,
                failureCount: 3,
                clientIPs: ['10.0.0.1'],
                clients: { 'claude-code': 120, curl: 8 },
              },
            ],
          },
        }
      }
      return { data: {} }
    })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText(/jmat_ab12/)).toBeInTheDocument()
    expect(screen.getByText('agent.whoami')).toBeInTheDocument()
    expect(screen.getByText('128')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.1')).toBeInTheDocument()
    // 客户端列按调用次数降序聚合
    expect(screen.getByText(/claude-code ×120/)).toBeInTheDocument()
    // 默认窗口 24h 且请求带上 window 参数
    expect(mockedApi.get).toHaveBeenCalledWith(ACTIVITY_URL, { params: { window: '24h' } })
    expect(screen.getByRole('button', { name: '24 小时' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('切换窗口后按新窗口重新请求', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '168h0m0s', generatedAt: '', items: [] } })

    const user = userEvent.setup()
    renderWithProviders(<McpActivityPage />)
    // 档位按钮文案来自 i18n（键名与档位值同名），这里按分组内顺序定位第二档，
    // 免得断言绑死在文案上。
    const presets = within(windowGroup()).getAllByRole('button')
    await user.click(presets[1])

    await waitFor(() => {
      expect(mockedApi.get).toHaveBeenCalledWith(ACTIVITY_URL, { params: { window: '168h' } })
    })
    expect(presets[1]).toHaveAttribute('aria-pressed', 'true')
  })

  describe('窗口档位与后端可解析性契约', () => {
    it('每个档位值都是后端能解析的 Go duration 形态（`7d` 一类不合法）', () => {
      for (const preset of WINDOW_PRESETS) {
        expect(
          GO_DURATION_RE.test(preset),
          `档位值 ${preset} 不是合法 Go duration（不支持 d 等单位），后端会以 400 拒绝`,
        ).toBe(true)
      }
    })

    it('每个档位值解析出的时长都落在后端闭区间 1h~168h 内', () => {
      for (const preset of WINDOW_PRESETS) {
        const seconds = parseGoDurationSeconds(preset)
        expect(Number.isNaN(seconds), `档位值 ${preset} 无法解析为 Go duration`).toBe(false)
        expect(seconds, `档位值 ${preset} 短于后端允许下限 1h`).toBeGreaterThanOrEqual(
          ONE_HOUR_SECONDS,
        )
        expect(seconds, `档位值 ${preset} 超过后端允许上限 168h`).toBeLessThanOrEqual(
          MAX_WINDOW_SECONDS,
        )
      }
    })
  })

  // 页面用 t(`mcpActivity.window_${w}`) 取档位文案，是动态拼接键；src/i18n/missing-keys.test.ts
  // 只扫静态调用的首参字符串字面量，对动态键零覆盖——档位值改名后漏补语言包不会有任何测试变红，
  // 第二档按钮会直接渲染出裸键。这里按 WINDOW_PRESETS 逐项守住语言包（直接读 JSON，不经过 i18next，
  // 免得它的 fallback 把缺键兜成看起来正常的中文）。
  // 注释里刻意不写带引号的调用示例：上面那个扫描器按正则扒源码、连注释一起扫，
  // 写了引号会让它误判出一个不存在的 key（本次已踩过一次）。
  describe('窗口档位文案的动态 i18n 键', () => {
    it('每个档位值的 mcpActivity.window_<档位值> 在 zh/en 中都存在且非空', () => {
      const bundles: ReadonlyArray<readonly [string, Map<string, unknown>]> = [
        ['zh.json', ZH_LEAVES],
        ['en.json', EN_LEAVES],
      ]
      const missing: string[] = []
      for (const preset of WINDOW_PRESETS) {
        const key = `mcpActivity.window_${preset}`
        for (const [file, leaves] of bundles) {
          const text = leaves.get(key)
          if (typeof text !== 'string' || text.trim() === '') {
            missing.push(`档位值 ${preset} 的 ${key} 在 ${file} 中缺失或为空`)
          }
        }
      }
      expect(missing, '动态 i18n 键缺失（档位值 + 语言文件）').toEqual([])
    })
  })

  it('空态显示提示', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h0m0s', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByText(/当前窗口内无 MCP 活动|No MCP activity/i)).toBeInTheDocument()
  })

  it('吊销 Token 入口指向 Agent Token 页（无会话可踢）', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h0m0s', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByRole('link', { name: /吊销 Token|Revoke Token/i })).toHaveAttribute(
      'href',
      '/agent-tokens',
    )
    // 会话时代的踢线按钮不得残留
    expect(screen.queryByRole('button', { name: /踢线|Kick/i })).not.toBeInTheDocument()
  })
})
