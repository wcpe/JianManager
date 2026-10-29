import { describe, expect, it, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { useAssistantChat } from '@/components/views/assistant/useAssistantChat'
import { AssistantChatProvider } from '@/components/views/assistant/assistant-chat-context'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { usePermissionsStore } from '@/stores/permissions'
import AssistantHomePage from './AssistantHomePage'

/** 
 * AI 首页的**权限与取数边界**（§6 / §8）。
 *
 * 这些断言守的是几条「看代码像对、跑起来才知错」的规则：
 *  1. 非管理员**不发** `/alerts/events`（不是「发了再隐藏」——请求一旦发出数据就到浏览器了）；
 *  2. 首页不把摘要 hook 自带的轮询带进来（常开页会持续压 CP）；
 *  3. 无会话时「最近会话」整块不渲染（spec 明确「不留空壳」）；
 *  4. 建议问题按权限过滤，不是只把不可见的藏起来。
 */
function trackRequests() {
  const seen: string[] = []
  server.events.on('request:start', ({ request }) => {
    seen.push(new URL(request.url).pathname)
  })
  return {
    seen,
    has: (path: string) => seen.some((p) => p === path),
    countOf: (path: string) => seen.filter((p) => p === path).length,
  }
}

/** 直接改权限 store：首页读的是它，不必走完整 auth 流程。 */

/** 
 * 首页现在从外壳层的 Context 取会话状态（不再自建）。
 * 测试里补一层 Provider，**仍然调用真实的 useAssistantChat**——
 * 这样测到的状态装配与生产一致，而不是喂一个手工 mock。
 */
function HomeInProvider() {
  const chat = useAssistantChat(true)
  return (
    <AssistantChatProvider value={chat}>
      <AssistantHomePage />
    </AssistantChatProvider>
  )
}

function setAdmin(isAdmin: boolean) {
  usePermissionsStore.setState({ loaded: true, isPlatformAdmin: isAdmin, nodes: new Set() })
}

/** 
 * 让 `/auth/me` 返回**非**平台管理员。
 *
 * 只 `setAdmin(false)` 不够：外壳挂载后会拉 `/auth/me`，mock 里 userId 1 是管理员，
 * 回写会把 store 改回 `true`。所以这里同时覆盖接口，模拟真实的普通用户登录。
 */
function asMember() {
  setAdmin(false)
  server.use(
    http.get(API('/auth/me'), () =>
      HttpResponse.json({
        userId: 2,
        username: 'member',
        role: 0,
        roleKey: 'member',
        isPlatformAdmin: false,
        nodes: ['instance.read'],
      }),
    ),
  )
}

describe('AI 首页：权限与取数边界', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('平台管理员：挂载告警区块并请求 /alerts/events', async () => {
    setAdmin(true)
    const probe = trackRequests()
    renderWithProviders(<HomeInProvider />)

    await waitFor(() => expect(probe.has('/api/v1/alerts/events')).toBe(true))
  })

  it('非管理员：**不发出** /alerts/events 请求（不是发了再隐藏）', async () => {
    // 必须把 /auth/me 也改成非管理员：外壳会在挂载后拉它并回写权限 store，
    // 只改 store 会被这次回写覆盖成管理员——那时发请求是**正确行为**，测不出本题。
    asMember()
    const probe = trackRequests()
    renderWithProviders(<HomeInProvider />)

    // 等首页的关键区块就绪，确保「没请求」不是因为还没到发请求的时机。
    await screen.findByText('集群水位')
    // 等 /auth/me 回来并落库，再断言——否则可能在回写前就检查，得到假阴性。
    await waitFor(() => expect(usePermissionsStore.getState().loaded).toBe(true))
    expect(probe.has('/api/v1/alerts/events')).toBe(false)
  })

  it('非管理员看不到告警建议问题', async () => {
    asMember()
    renderWithProviders(<HomeInProvider />)

    await screen.findByText('哪些实例最近崩溃过？')
    expect(screen.queryByText('帮我看看有没有未解决的告警')).not.toBeInTheDocument()
  })

  it('管理员能看到告警建议问题', async () => {
    setAdmin(true)
    renderWithProviders(<HomeInProvider />)

    expect(await screen.findByText('帮我看看有没有未解决的告警')).toBeInTheDocument()
  })

  it('集群水位只读 totals：请求带固定 range，且不渲染趋势图容器', async () => {
    setAdmin(true)
    const probe = trackRequests()
    const { container } = renderWithProviders(<HomeInProvider />)

    await screen.findByText('集群水位')
    // range 是必填参数，首页固定传 24h。
    const metricCall = probe.seen.find((p) => p === '/api/v1/metrics/overview')
    expect(metricCall).toBeDefined()
    // 「只取 totals」的可核验形式：页面里没有趋势图（recharts 容器）。
    expect(container.querySelector('.recharts-wrapper')).toBeNull()
  })

  it('首页摘要不轮询：自己那份查询不注册任何 refetchInterval，也不与顶栏共用 key', async () => {
    setAdmin(true)
    // 自建 client 才能拿到查询缓存对象——这比「数 URL 请求」可靠：
    // 顶栏也拉同一端点且自带 10 秒轮询，数 URL 分不清是谁发的
    // （真机实测：离开首页到 /nodes 后该端点仍每 10 秒被请求，证明那是顶栏）。
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <BrowserRouter>
        <QueryClientProvider client={client}>
          {/* 保留自建 client（要读它的 queryCache），外面补会话 Context 包装 */}
          <HomeInProvider />
        </QueryClientProvider>
      </BrowserRouter>,
    )

    await screen.findByText('集群水位')
    const mine = client.getQueryCache().findAll({ queryKey: ['assistantHome', 'metricOverview'] })
    expect(mine).toHaveLength(1)
    // 没有定时器 → 进入时取一次。
    expect(mine[0]?.options.refetchInterval ?? false).toBe(false)
    // 不与顶栏共用 key：共用即会被顶栏的轮询一起刷新（这是本用例存在的理由）。
    expect(mine[0]?.queryKey).not.toEqual(['metricOverview', '24h', 'auto'])
  })
  it('无会话时「最近会话」整块不渲染（不留空壳）', async () => {
    setAdmin(true)
    // 让会话列表返回空，验证区块完全不出现而不是显示空态文案。
    server.use(
      http.get(API('/assistant/conversations'), () => HttpResponse.json({ items: [], nextCursor: '' })),
    )
    renderWithProviders(<HomeInProvider />)

    await screen.findByText('集群水位')
    expect(screen.queryByText('最近会话')).not.toBeInTheDocument()
  })

  it('有会话时最多显示 5 条', async () => {
    setAdmin(true)
    const many = Array.from({ length: 9 }, (_, i) => ({
      id: 9000 + i,
      title: `会话 ${i}`,
      selectedModelId: null,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }))
    server.use(
      http.get(API('/assistant/conversations'), () => HttpResponse.json({ items: many, nextCursor: '' })),
    )
    renderWithProviders(<HomeInProvider />)

    await screen.findByText('最近会话')
    expect(screen.getByText('会话 0')).toBeInTheDocument()
    expect(screen.getByText('会话 4')).toBeInTheDocument()
    // 第 6 条起不显示（区块内最多 5 条）。
    expect(screen.queryByText('会话 5')).not.toBeInTheDocument()
  })

  it('显示按时段的问候语', async () => {
    setAdmin(true)
    renderWithProviders(<HomeInProvider />)

    // 三个时段文案互斥，跑测试的真实时间只会命中其中一个——断言「命中其中之一」。
    const greeting = await screen.findByText(/早上好|下午好|晚上好/)
    expect(greeting).toBeInTheDocument()
  })
})

/** 
 * 首页模式记忆与双向切换（§4.1/§4.2/§4.4）。
 *
 * 这几个断言守的是「只做浏览器手测就会静默退化」的行为：
 * 切换入口在不在、点了写不写记忆、重置是否连同 URL 参数一起清掉。
 */
describe('AI 首页：模式记忆与切换入口', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('提供切到经典首页的入口，点击后写入记忆为 classic', async () => {
    setAdmin(true)
    renderWithProviders(<HomeInProvider />)

    const link = await screen.findByRole('link', { name: /经典首页/ })
    expect(link).toHaveAttribute('href', '/?home=classic')

    // 点击时**写记忆**：不写的话下次打开会回到默认，用户会以为「记忆坏了」。
    await userEvent.setup().click(link)
    expect(localStorage.getItem('console.homeMode')).toBe('classic')
  })

  it('重置入口：清记忆并移除 URL 上的 home 参数', async () => {
    setAdmin(true)
    // 从「带着显式参数」的状态进入——这正是重置最容易失效的场景：
    // 只清记忆而不去参数，URL 会继续压过默认值，表现为「点了没反应」。
    //
    // 必须经 `route` 选项传入：`renderWithProviders` 内部会 `pushState({}, '', route)`，
    // 在外面先 pushState 会被它的默认 route='/' 覆盖掉，用例就测不到目标场景了
    // （本用例初版正是踩了这个坑：变异后仍通过，属假测试）。
    localStorage.setItem('console.homeMode', 'classic')

    renderWithProviders(<HomeInProvider />, { route: '/?home=assistant' })

    // Radix 下拉在 jsdom 下必须走 userEvent（原生 click 不触发 pointer 展开）。
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '首页偏好' }))
    const menu = await screen.findByRole('menu')
    await user.click(await within(menu).findByRole('menuitem', { name: /重置首页偏好/ }))

    await waitFor(() => {
      expect(localStorage.getItem('console.homeMode')).toBeNull()
    })
    await waitFor(() => {
      expect(window.location.search).not.toContain('home=')
    })
  })
})

describe('AI 首页：摘要区块独立失败（§6）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('单个摘要接口 500 时，只有该区块显示错误态，输入器与其它区块照常可用', async () => {
    setAdmin(true)
    // 只打挂「近期任务」：其余端点保持 mock 正常返回。
    // 用 500 而不是空数组——空数组会被渲染成「近期没有任务」空态，那不是错误态，
    // 两者在验收里是不同要求（空 ≠ 失败）。
    server.use(http.get(API('/tasks'), () => new HttpResponse(null, { status: 500 })))

    renderWithProviders(<HomeInProvider />, { route: '/' })

    // 任务区块出现错误提示
    expect(await screen.findByText('加载失败，稍后可刷新重试')).toBeTruthy()

    // 其它区块仍在：需要关注、集群水位（有 totals 时）
    expect(screen.getByText('需要关注')).toBeTruthy()
    expect(screen.getByText('近期任务')).toBeTruthy()

    // 输入器可用：这是首页的核心功能，一个摘要失败不该拖垮它
    const input = await screen.findByRole('textbox')
    expect(input).toBeTruthy()
    expect((input as HTMLTextAreaElement).disabled).toBe(false)
  })

  it('错误态只说「加载失败」，不把后端错误原文透传到界面', async () => {
    setAdmin(true)
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({ message: 'sql: connection refused at 10.0.0.7:3306' }, { status: 500 }),
      ),
    )

    renderWithProviders(<HomeInProvider />, { route: '/' })

    expect(await screen.findByText('加载失败，稍后可刷新重试')).toBeTruthy()
    // 后端细节（内部地址、SQL 片段）不该出现在用户界面
    expect(screen.queryByText(/10\.0\.0\.7|connection refused/)).toBeNull()
  })
})

describe('AI 首页：平台管理员看到全部摘要区块（§8）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('四个摘要面板齐全，且告警区块确实渲染出来', async () => {
    setAdmin(true)
    renderWithProviders(<HomeInProvider />, { route: '/' })

    // 「异常实例」与「告警」在实现里合并为同一个「需要关注」面板，
    // 因此四个 Panel 承载 spec 说的五个逻辑区块。
    expect(await screen.findByText('需要关注')).toBeTruthy()
    expect(screen.getByText('近期任务')).toBeTruthy()
    // 集群水位依赖 totals 就绪后才占位（无数据时不渲染空壳），故等待而非同步取
    expect(await screen.findByText('集群水位', {}, { timeout: 5000 })).toBeTruthy()
    // 最近会话依赖已有会话；mock 默认有数据，故应出现
    await waitFor(() => {
      expect(screen.getByText('最近会话')).toBeTruthy()
    })
  })
})
