import { describe, it, expect, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import AuditPage from './AuditPage'

/**
 * 滚到底部。jsdom 不做布局（scrollHeight 恒 0、没有真滚动条），但 scrollTop 是普通可读写属性
 * （`Element.prototype.scrollTop` 被 jsdom 实现为取值/赋值，不参与布局），故直接设到远超内容高度的
 * 偏移再派发 scroll —— 等价于浏览器里把列表拖到底：视口裁剪随之取末段行。
 */
function scrollToBottom(el: HTMLElement) {
  el.scrollTop = 10_000_000
  fireEvent.scroll(el)
}

/**
 * 等 action 筛选（容器侧 300ms 防抖）真正落地。
 *
 * 必须**在同一帧内**同时成立「命中行在 + 未命中行不在」：只等未命中行消失会命中重查期间的空窗
 * （`useAuditLogs` 无 placeholderData，改键那一刻 data 变 undefined），只等命中行出现又会命中
 * 尚未过滤的旧列表（instance.start 本来就在种子里）。
 */
async function waitActionFiltered() {
  await waitFor(() => {
    expect(screen.queryByText('user.login')).not.toBeInTheDocument()
    expect(screen.getByText('instance.start')).toBeInTheDocument()
  })
}

/**
 * AuditPage 强断言（FR-199 身份访问域）。受 requireAuth 保护：渲染前 loginMockUser()。
 * AuditPage 同时调 GET /users（筛选下拉）+ GET /audit（列表），两者均由本域 mock。
 * 三条：① 渲染种子审计行 ② 筛选 action 后列表联动收窄 ③ 注入 500 后页面显错误态。
 */
describe('AuditPage（mock 假后端）', () => {
  it('渲染种子审计日志（种子里的 action 均在）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    expect(await screen.findByText('user.login')).toBeInTheDocument()
    expect(screen.getByText('instance.start')).toBeInTheDocument()
    expect(screen.getByText('group.create')).toBeInTheDocument()
    /*
      种子现为 5 条：原有 3 条 + 2 条节点配置类变更（node.proxy.set / node.artifact_cache.set_cap），
      后者是给节点「配置」页签的「最近变更」提供数据用的。
      断言总数而非只断言三条 action，避免以后再加种子又连累这条用例。
    */
    expect(screen.getByText('已加载 5 / 共 5 条')).toBeInTheDocument()
  })

  it('按 action 筛选 → 列表联动收窄（仅命中行保留）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    await screen.findByText('user.login')
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText(/操作/), 'instance')
    await waitActionFiltered()
    expect(screen.queryByText('group.create')).not.toBeInTheDocument()
  })

  it('分页 envelope 显示真实命中总数', async () => {
    loginMockUser()
    server.use(
      http.get(API('/audit'), () =>
        HttpResponse.json({
          items: [
            {
              id: 1,
              uuid: 'a-1',
              userId: 1,
              action: 'user.login',
              targetType: 'user',
              targetId: '1',
              detail: '',
              ip: '127.0.0.1',
              createdAt: '2026-06-28T10:00:00Z',
              user: { id: 1, username: 'admin' },
            },
          ],
          total: 250,
          page: 1,
          pageSize: 100,
        }),
      ),
    )

    renderWithProviders(<AuditPage />)

    expect(await screen.findByText('user.login')).toBeInTheDocument()
    expect(screen.getByText('已加载 1 / 共 250 条')).toBeInTheDocument()
  })

  it('加载更多按 100 条分页请求并可累计浏览超过 200 条数据', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const requests: URL[] = []
    server.use(
      http.get(API('/audit'), ({ request }) => {
        const url = new URL(request.url)
        requests.push(url)
        const page = Number(url.searchParams.get('page'))
        const pageSize = Number(url.searchParams.get('pageSize'))
        const start = (page - 1) * pageSize
        const items = Array.from({ length: Math.min(pageSize, 250 - start) }, (_, index) => {
          const id = start + index + 1
          return {
            id,
            uuid: `a-${id}`,
            userId: 1,
            action: `audit.row.${id}`,
            targetType: 'audit',
            targetId: String(id),
            detail: '',
            ip: '127.0.0.1',
            failed: false,
            error: '',
            createdAt: '2026-06-28T10:00:00Z',
            user: { id: 1, username: 'admin' },
          }
        })
        return HttpResponse.json({ items, total: 250, page, pageSize })
      }),
    )

    renderWithProviders(<AuditPage />)

    expect(await screen.findByText('audit.row.1')).toBeInTheDocument()
    expect(requests[0].searchParams.get('page')).toBe('1')
    expect(requests[0].searchParams.get('pageSize')).toBe('100')
    expect(screen.getByText('已加载 100 / 共 250 条')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '加载更多' }))

    expect(requests[1].searchParams.get('page')).toBe('2')
    expect(requests[1].searchParams.get('pageSize')).toBe('100')
    // 页脚计数是「已加载量」的真源：第 2 页已并入浏览窗口；滚动位置仍在顶部，故首行仍在视口内。
    expect(await screen.findByText('已加载 200 / 共 250 条')).toBeInTheDocument()
    expect(screen.getByText('audit.row.1')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '加载更多' }))

    expect(requests[2].searchParams.get('page')).toBe('3')
    expect(requests[2].searchParams.get('pageSize')).toBe('100')
    expect(await screen.findByText('已加载 250 / 共 250 条')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '加载更多' })).toBeDisabled()
    // 视口裁剪（FR-502 阶段 6）：250 行已全部加载，但视口外的行不进 DOM（不再一次性铺满整页）。
    expect(screen.queryByText('audit.row.250')).not.toBeInTheDocument()

    // 滚到底 → 末页数据确实可浏览（原断言「第 N 行在 DOM」要证的就是这条路径）。
    scrollToBottom(screen.getByTestId('audit-log-virtual'))
    expect(await screen.findByText('audit.row.250')).toBeInTheDocument()
    // 窗口已移到末段：首行随之移出 DOM，证明渲染量只与视口有关、与已加载总量无关。
    expect(screen.queryByText('audit.row.1')).not.toBeInTheDocument()
  })

  it('导出使用相同筛选条件，但不携带分页参数；下载链接先入 DOM 再撤销 URL', async () => {
    loginMockUser()
    const user = userEvent.setup()
    let exportedUrl: URL | null = null
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:audit')
    let revokeCalls = 0
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {
      revokeCalls += 1
    })
    // 点击那一刻链接是否已挂进文档；以及撤销是否与点击落在**同一个同步块**里
    // （同块撤销会把部分浏览器里尚未取到字节的下载掐断——这正是本轮修的问题）。
    let connectedAtClick: boolean | null = null
    let revokedSameTask: boolean | null = null
    let sameTaskProbe: Promise<void> = Promise.resolve()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      connectedAtClick = this.isConnected
      // 微任务探针：同处一个任务的撤销会被它看到；排进 setTimeout 的撤销不会。
      sameTaskProbe = Promise.resolve().then(() => {
        revokedSameTask = revokeCalls > 0
      })
    })
    server.use(
      http.get(API('/audit/export'), ({ request }) => {
        exportedUrl = new URL(request.url)
        return new HttpResponse('{"id":1}\n', { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )

    renderWithProviders(<AuditPage />)
    await screen.findByText('user.login')
    await user.type(screen.getByPlaceholderText(/操作/), 'instance')
    // 先等防抖落地（列表收窄到只剩命中行），再导出——导出的正是用户眼前这组筛选。
    await waitActionFiltered()
    await user.click(screen.getByRole('button', { name: '导出' }))

    await waitFor(() => expect(exportedUrl).not.toBeNull())
    expect(exportedUrl?.searchParams.get('action')).toBe('instance')
    expect(exportedUrl?.searchParams.has('page')).toBe(false)
    expect(exportedUrl?.searchParams.has('pageSize')).toBe(false)
    expect(exportedUrl?.searchParams.has('limit')).toBe(false)

    expect(connectedAtClick).toBe(true)
    await sameTaskProbe
    expect(revokedSameTask).toBe(false)
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith('blob:audit'))

    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
    click.mockRestore()
  })

  it('注入 500（GET /audit）→ 页面显错误态', async () => {
    loginMockUser()
    mockInject('get', '/audit', { kind: 'status', status: 500 })
    renderWithProviders(<AuditPage />)
    // useAuditLogs isError → AuditPage 渲染 audit.loadError。
    expect(await screen.findByText('加载审计日志失败')).toBeInTheDocument()
  })

  // FR-303：action 键 i18n——已知键显翻译且原键仍可见（角标+悬停），未知键回退原样不崩。
  it('已知 action 键显示中文翻译，原键以角标保留（FR-303）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    // 种子 instance.start / group.create 均有 audit.actions 映射 → 显中文。
    expect(await screen.findByText('启动实例')).toBeInTheDocument()
    expect(screen.getByText('创建用户组')).toBeInTheDocument()
    // 原键仍可见（小号 mono 角标），筛选输入按原键筛的心智不断。
    expect(screen.getByText('instance.start')).toBeInTheDocument()
    expect(screen.getByText('group.create')).toBeInTheDocument()
  })

  it('未知 action 键回退原键显示，不崩（FR-303）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    // 种子 user.login 不在 audit.actions 映射表（后端无此键）→ 原样展示。
    expect(await screen.findByText('user.login')).toBeInTheDocument()
  })

  // 用户筛选原先是「useUsers() 拉全量 + 逐个挂 SelectItem」（规则点名的禁用形态）；
  // 现在候选走服务端搜索（默认前 N 条 + 键入 q），本用例锁死「请求带 limit 信封 + 键入只发一次 q」。
  it('用户筛选走服务端搜索：候选带 limit 信封，键入只发一次带 q 的请求', async () => {
    loginMockUser()
    const userUrls: string[] = []
    const auditUrls: string[] = []
    server.use(
      http.get(API('/users'), ({ request }) => {
        userUrls.push(request.url)
        return HttpResponse.json({
          items: [{ id: 9, uuid: 'u9', username: 'zoe', role: 0, status: 1, createdAt: '2026-01-01T00:00:00Z' }],
          total: 1,
          limit: 50,
          offset: 0,
        })
      }),
      http.get(API('/audit'), ({ request }) => {
        auditUrls.push(request.url)
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 100 })
      }),
    )
    renderWithProviders(<AuditPage />)

    // 首屏候选：带 limit 走分页信封（不再拉整个用户数组）。
    await waitFor(() => expect(userUrls).toHaveLength(1))
    expect(userUrls[0]).toContain('limit=50')
    expect(userUrls[0]).not.toContain('q=')

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: '全部用户' }))
    await user.type(await screen.findByPlaceholderText('搜索或输入…'), 'zoe')

    await waitFor(() => expect(userUrls.some((u) => u.includes('q=zoe'))).toBe(true))
    // 300ms 防抖：中间的 z / zo 不许各发一次。
    expect(userUrls.filter((u) => u.includes('q='))).toHaveLength(1)

    // 选中后筛选上报到列表请求（userId 生效）。
    await user.click(screen.getByRole('button', { name: 'zoe' }))
    await waitFor(() => expect(auditUrls.at(-1)).toContain('userId=9'))
  })
})
