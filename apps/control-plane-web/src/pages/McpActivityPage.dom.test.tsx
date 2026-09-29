import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
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
            window: '24h',
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
    mockedApi.get.mockResolvedValue({ data: { window: '168h', generatedAt: '', items: [] } })

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

  it('空态显示提示', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByText(/当前窗口内无 MCP 活动|No MCP activity/i)).toBeInTheDocument()
  })

  it('吊销 Token 入口指向 Agent Token 页（无会话可踢）', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByRole('link', { name: /吊销 Token|Revoke Token/i })).toHaveAttribute(
      'href',
      '/agent-tokens',
    )
    // 会话时代的踢线按钮不得残留
    expect(screen.queryByRole('button', { name: /踢线|Kick/i })).not.toBeInTheDocument()
  })
})
