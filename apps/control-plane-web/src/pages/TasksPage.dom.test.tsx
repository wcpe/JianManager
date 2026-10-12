import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { db } from '@jianmanager/devmock/db'
import TasksPage from './TasksPage'

/** 造一条任务（字段集对齐后端 Task；未知状态用字符串模拟「后端先上了一档前端还不认识的状态」）。 */
function mkTask(over: Record<string, unknown> = {}) {
  const now = new Date().toISOString()
  return {
    id: 1,
    taskId: 'task-a',
    nodeId: 1,
    kind: 'jdk_install',
    state: 'running',
    progress: 10,
    title: '任务 A',
    detail: '',
    error: '',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: now,
    updatedAt: now,
    ...over,
  }
}

/**
 * 滚到底部。jsdom 不做布局（scrollHeight 恒 0、没有真滚动条），但 scrollTop 是普通可读写属性
 * （`Element.prototype.scrollTop` 由 jsdom 实现为取值/赋值，不参与布局），故直接设到远超内容高度的
 * 偏移再派发 scroll —— 等价于浏览器里把列表拖到底：视口裁剪随之取末段行。
 */
function scrollToBottom(el: HTMLElement) {
  el.scrollTop = 10_000_000
  fireEvent.scroll(el)
}

/**
 * TasksPage 强断言（FR-208）：验种子任务渲染、展开看任务日志（详情联动）、错误注入显错误态；
 * 分页信封（FR-337）：共 N 条/已加载数、「加载更多」增长窗口、筛选变化复位窗口。
 */
describe('TasksPage（mock 假后端）', () => {
  it('① 渲染出种子任务行', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('安装 JDK Temurin 21')).toBeInTheDocument()
    expect(screen.getByText('备份实例 survival')).toBeInTheDocument()
    expect(screen.getByText('安装便携运行时')).toBeInTheDocument()
  })

  it('② 展开任务 → 懒查详情日志联动出现', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装 JDK Temurin 21/ })
    await userEvent.click(row)
    // 展开后 GET /tasks/task-jdk-1 拉到滚动日志
    expect(await screen.findByText(/解压到 \/opt\/jdk\/temurin-21/)).toBeInTheDocument()
  })

  it('② 失败任务展开 → 显示错误原因', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装便携运行时/ })
    await userEvent.click(row)
    expect(await screen.findByText(/sha256 不匹配/)).toBeInTheDocument()
  })

  it('② 网络类下载失败展开 → 显示代理/镜像可操作入口（FR-279）', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /受限网络下装 JDK 21（网络失败）/ })
    await userEvent.click(row)
    expect(await screen.findByText(/出站网络不可达/)).toBeInTheDocument()
    const proxyLink = screen.getByRole('link', { name: /去配置出站代理/ })
    expect(proxyLink).toHaveAttribute('href', '/settings')
    const mirrorLink = screen.getByRole('link', { name: /更换下载源\/镜像/ })
    expect(mirrorLink).toHaveAttribute('href', '/runtime-assets')
  })

  it('② 非网络类失败不显示网络引导入口（FR-279 不误报）', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装便携运行时/ })
    await userEvent.click(row)
    await screen.findByText(/sha256 不匹配/)
    expect(screen.queryByRole('link', { name: /去配置出站代理/ })).not.toBeInTheDocument()
  })

  it('③ 注入 500 → 显示加载任务失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/tasks', { kind: 'status', status: 500 })
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('加载任务失败')).toBeInTheDocument()
  })

  it('空态：注入空列表 → 显示暂无任务', async () => {
    loginMockUser()
    mockInject('get', '/tasks', { kind: 'empty' })
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('暂无任务')).toBeInTheDocument()
  })

  it('④ 分页信封（FR-337）：共 N 条/已加载数、加载更多扩窗且保持筛选、筛选变化复位窗口', async () => {
    loginMockUser()
    // 直接从假后端集合算期望值，不硬编码种子规模（数百任务种子，见 observ.ts MOCK_TASK_COUNT）。
    const allTasks = db<{ title: string }>('tasks').list()
    const seededTotal = allTasks.length
    const batchTasks = allTasks.filter((t) => t.title.includes('批量任务'))
    const filteredTotal = batchTasks.length
    expect(seededTotal).toBeGreaterThan(200) // 前置：种子须超过两窗，否则下方断言失真
    renderWithProviders(<TasksPage />)

    // 首窗：limit 缺省 100，顶部「共 N 条 · 已加载 100」。
    expect(await screen.findByText(`共 ${seededTotal} 条 · 已加载 100`)).toBeInTheDocument()

    // 「加载更多」→ 窗口扩到 200，总数不变。
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText(`共 ${seededTotal} 条 · 已加载 200`)).toBeInTheDocument()

    // 改关键词筛选 → 窗口复位 100，total 随筛选口径收窄。
    fireEvent.change(screen.getByPlaceholderText('搜索标题 / 详情'), { target: { value: '批量任务' } })
    expect(await screen.findByText(`共 ${filteredTotal} 条 · 已加载 100`)).toBeInTheDocument()

    // 再「加载更多」→ 在同一筛选结果集内扩窗（加载更多保持筛选参数）。
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText(`共 ${filteredTotal} 条 · 已加载 200`)).toBeInTheDocument()

    // 视口裁剪（FR-502 阶段 6）：已加载 200 行，但视口外的行不进 DOM——渲染量只与视口有关。
    const scroller = screen.getByTestId('tasks-virtual')
    expect(screen.queryByText(batchTasks[199].title)).not.toBeInTheDocument()
    // 滚到底 → 末窗数据可浏览（证明「加载更多」扩出来的窗口确实可达，而非只改了个计数）。
    scrollToBottom(scroller)
    expect(await screen.findByText(batchTasks[199].title)).toBeInTheDocument()
  })

  it('关键字防抖：连敲三个字符只多发一次带最终关键字的请求', async () => {
    loginMockUser()
    const urls: string[] = []
    server.use(
      http.get(API('/tasks'), ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })
      }),
    )
    renderWithProviders(<TasksPage />)
    await screen.findByText('暂无任务')
    expect(urls).toHaveLength(1) // 首屏（无关键字）

    await userEvent.type(screen.getByPlaceholderText('搜索标题 / 详情'), 'JDK')

    await waitFor(() => expect(urls.some((u) => u.includes('keyword=JDK'))).toBe(true))
    // 本页查询是 staleTime:0 + 2s 短轮询，不防抖的话每敲一个字都是一次全量列表请求。
    expect(urls.filter((u) => u.includes('keyword='))).toHaveLength(1)
  })

  it('进度条有 progressbar 语义与可访问名（原先只是两个 div 的宽度差）', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    await screen.findByText('安装 JDK Temurin 21')

    const bar = screen.getByRole('progressbar', { name: '安装 JDK Temurin 21 进度' })
    expect(bar).toHaveAttribute('aria-valuemin', '0')
    expect(bar).toHaveAttribute('aria-valuemax', '100')
    // 数值与界面显示同源（列里那个「N%」）。
    expect(bar).toHaveAttribute('aria-valuenow', '100')
  })

  it('后端返回未知状态 → 兜底显示原始状态串，不把页面打崩', async () => {
    loginMockUser()
    // 后端先行一档（前端枚举还没有 queued）：原实现直接 `STATE_META[state].variant` 会抛错。
    server.use(
      http.get(API('/tasks'), () =>
        HttpResponse.json({
          items: [mkTask({ taskId: 'task-queued', title: '排队中的任务', state: 'queued' })],
          total: 1,
          limit: 100,
          offset: 0,
        }),
      ),
    )
    renderWithProviders(<TasksPage />)

    expect(await screen.findByText('排队中的任务')).toBeInTheDocument()
    expect(screen.getByText('queued')).toBeInTheDocument()
  })

  it('详情日志超上限 → 只渲染尾部并显式说明截断范围', async () => {
    loginMockUser()
    const seeded = db<{ title: string; taskId: string }>('tasks')
      .list()
      .find((t) => t.title === '安装 JDK Temurin 21')
    expect(seeded).toBeDefined()
    const logs = Array.from({ length: 600 }, (_, i) => ({
      id: i + 1,
      taskId: seeded!.taskId,
      seq: i + 1,
      line: `log-line-${i}`,
      ts: new Date().toISOString(),
    }))
    server.use(
      http.get(API(`/tasks/${seeded!.taskId}`), () =>
        HttpResponse.json({ task: mkTask({ taskId: seeded!.taskId, state: 'succeeded', progress: 100 }), logs }),
      ),
    )
    renderWithProviders(<TasksPage />)
    await userEvent.click(await screen.findByRole('button', { name: /安装 JDK Temurin 21/ }))

    // 截断说明（复用既有计数键，未新增 i18n）：已渲染 500 / 共 600。
    expect(await screen.findByText('已加载 500 / 共 600')).toBeInTheDocument()
    const pre = document.querySelector('pre') as HTMLPreElement
    // 只渲染尾部 500 行：最早的 log-line-0 不再进 DOM，最新一行仍在。
    expect(pre.textContent?.split('\n')).toHaveLength(500)
    expect(pre.textContent).toContain('log-line-599')
    expect(pre.textContent).not.toContain('log-line-0\n')
  })

  it('深链同步：页面已挂载时再跳 ?task=<另一个 id> 会切换展开行', async () => {
    loginMockUser()
    server.use(
      http.get(API('/tasks'), () =>
        HttpResponse.json({
          items: [mkTask({ taskId: 'task-a', title: '任务 A' }), mkTask({ taskId: 'task-b', title: '任务 B' })],
          total: 2,
          limit: 100,
          offset: 0,
        }),
      ),
      http.get(API('/tasks/task-a'), () =>
        HttpResponse.json({ task: mkTask({ taskId: 'task-a' }), logs: [{ id: 1, taskId: 'task-a', seq: 1, line: 'LOG-A', ts: new Date().toISOString() }] }),
      ),
      http.get(API('/tasks/task-b'), () =>
        HttpResponse.json({ task: mkTask({ taskId: 'task-b' }), logs: [{ id: 2, taskId: 'task-b', seq: 1, line: 'LOG-B', ts: new Date().toISOString() }] }),
      ),
    )
    renderWithProviders(<TasksPage />, { route: '/tasks?task=task-a' })
    expect(await screen.findByText('LOG-A')).toBeInTheDocument()

    // 页眉任务菜单在**本页已挂载**时再跳一次：只换查询串，组件不重挂。
    act(() => {
      window.history.pushState({}, '', '/tasks?task=task-b')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    expect(await screen.findByText('LOG-B')).toBeInTheDocument()
    expect(screen.queryByText('LOG-A')).toBeNull()
  })
})
