import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { domainRoute, mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { usePermissionsStore } from '@/stores/permissions'
import type { AlertChannelInfo, QQDiscoveredGroup } from '@/api/alerts'
import { ChannelDialog } from './ChannelDialog'

/**
 * QQ 接入状态只读观测区（FR-495）的 DOM 回归。
 *
 * 存在的理由：网关状态与已发现群之前**只在 API 层存在**，没有任何界面调用点——一旦网关
 * 掉线（`1<<25` 权限失效 4014、会话失效 4006），运维只能翻日志。本文件钉住四件事：
 * ① 四个状态下都能给出可读、可操作的展示（含 4014 的权限指向）；② 群列表 0 条与 N 条都有态；
 * ③ **仍然没有任何群通道入口**（这是产品决策，不是待补的缺口）；
 * ④ 无 `alert.manage` 时整个区块不渲染、也不发注定 403 的请求。
 *
 * 走 ChannelDialog 而不是直接渲染面板：面板唯一的装配点就在那个对话框的 QQ 分区里，
 * 直接测子组件会让「忘记接线」这类回归漏网。
 *
 * toast 被 mock：观测区刻意**不**弹 toast（错误只在区块内联），这里要能断言这一点。
 */
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

/** 编辑态的 QQ 通道（三要素已填满，打开即落在 QQ 分区，不必走下拉切换）。 */
const existingQQ: AlertChannelInfo = {
  id: 2,
  uuid: 'chan-qq',
  name: 'QQ 值班',
  type: 'qq',
  enabled: true,
  config: JSON.stringify({
    appId: '102000001',
    appSecret: '${QQ-102000001}',
    targetId: 'USER_OPENID_DEMO_1',
    targetType: 'c2c',
  }),
  createdAt: new Date().toISOString(),
}

/** 取共享 Dialog 面板。 */
function panelByTitle(title: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: title })
  return heading.closest('[role="dialog"]') as HTMLElement
}

/** 渲染编辑态 QQ 通道对话框，返回其面板。 */
function renderQQDialog(): HTMLElement {
  loginMockUser()
  renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
  return panelByTitle('编辑通道')
}

/** 把网关状态端点换成指定响应体。 */
function stubGateway(body: Record<string, unknown>) {
  server.use(domainRoute('get', '/alerts/qq/gateway/status', () => HttpResponse.json(body)))
}

/** 把已发现群端点换成指定条目（onUnhandledRequest: 'error' 下必须显式提供）。 */
function stubGroups(items: QQDiscoveredGroup[], total = items.length) {
  server.use(domainRoute('get', '/alerts/qq/groups', () => HttpResponse.json({ items, total })))
}

/** 一大一小两个时间戳：相对时间文案不必断言绝对形状，但两者必须同时出现。 */
const GROUP_A: QQDiscoveredGroup = {
  id: 1,
  groupOpenid: 'GROUP_OPENID_DEMO_1',
  opMemberOpenid: 'OP_MEMBER_DEMO_1',
  firstSeenAt: new Date(Date.now() - 2 * 3600_000).toISOString(),
  lastSeenAt: new Date(Date.now() - 60_000).toISOString(),
  sourceAppId: '102000001',
}

const GROUP_B: QQDiscoveredGroup = {
  id: 2,
  groupOpenid: 'GROUP_OPENID_DEMO_2',
  opMemberOpenid: 'OP_MEMBER_DEMO_2',
  firstSeenAt: new Date(Date.now() - 86_400_000).toISOString(),
  lastSeenAt: new Date(Date.now() - 600_000).toISOString(),
  sourceAppId: '102000001',
}

describe('QQ 接入状态只读观测区（FR-495）', () => {
  it('① connected：已连接徽章 + appId + 最近事件时间', async () => {
    stubGateway({
      appId: '102000001',
      status: 'connected',
      lastEventAt: new Date(Date.now() - 5 * 60_000).toISOString(),
      lastError: '',
    })
    stubGroups([GROUP_A, GROUP_B])
    const panel = renderQQDialog()

    const status = await screen.findByTestId('qq-gateway-status')
    expect(within(status).getByText('已连接')).toBeInTheDocument()
    expect(within(status).getByText('102000001')).toBeInTheDocument()
    // 相对时间走 formatRelativeTime（5 分钟前），不硬编码绝对时间。
    expect(within(status).getByText(/最近事件：5 分钟前/)).toBeInTheDocument()
    // 正常态不出现错误行与 4014 提示。
    expect(screen.queryByTestId('qq-gateway-last-error')).not.toBeInTheDocument()
    expect(screen.queryByTestId('qq-gateway-hint')).not.toBeInTheDocument()
    // 接入状态区确实挂在 QQ 分区里（不在对话框外）。
    expect(within(panel).getByText('接入状态（只读）')).toBeInTheDocument()
  })

  it('② error(4014)：故障徽章 + 后端原文 + 明确指向 1<<25 权限的可操作提示', async () => {
    stubGateway({
      appId: '102000001',
      status: 'error',
      lastEventAt: null,
      // 后端 closeErrorText 的真实文案（中文、含 intent 字面量与官方常量名）。
      lastError: 'QQ 机器人缺少 1<<25（GROUP_AND_C2C_EVENT）intent 权限，请到开放平台申请后重试',
    })
    stubGroups([])
    renderQQDialog()

    const status = await screen.findByTestId('qq-gateway-status')
    expect(within(status).getByText('故障（需人工处理）')).toBeInTheDocument()
    // 后端诊断原文逐字保留（它含关闭码与平台常量，是本页唯一可操作的线索）。
    expect(screen.getByTestId('qq-gateway-last-error').textContent).toContain('1<<25')
    // 本地化提示把「怎么办」说清：申请权限 + 单聊告警不受影响。
    const hint = screen.getByTestId('qq-gateway-hint').textContent ?? ''
    expect(hint).toContain('GROUP_AND_C2C_EVENT')
    expect(hint).toContain('QQ 开放平台')
    expect(hint).toContain('单聊告警不受影响')
    // 无最近事件时不渲染「最近事件」行（后端 lastEventAt 为 null）。
    expect(within(status).queryByText(/最近事件/)).not.toBeInTheDocument()
  })

  it('③ 加载中：先出骨架占位，数据到了换成状态徽章', async () => {
    // 延迟 40ms 制造一个可观察的加载态（不用假计时器，避免波及 Radix/React 调度）。
    // `then` 不能省：注入的 delay 分支不穿 then 时只回 `{}`，状态就变成「未知」，
    // 测的就不再是「加载完成后落到 connecting」这件事了。
    stubGroups([])
    mockInject('get', '/alerts/qq/gateway/status', {
      kind: 'delay',
      ms: 40,
      then: {
        kind: 'status',
        status: 200,
        body: { appId: '102000001', status: 'connecting', lastEventAt: null, lastError: '' },
      },
    })
    renderQQDialog()

    expect(screen.getByTestId('qq-gateway-loading')).toBeInTheDocument()
    // 加载中不展示错误也不是空白（标题常驻，用户知道这一块在干什么）。
    expect(screen.getByText('QQ 网关')).toBeInTheDocument()

    const status = await screen.findByTestId('qq-gateway-status')
    expect(within(status).getByText('连接中')).toBeInTheDocument()
    expect(screen.queryByTestId('qq-gateway-loading')).not.toBeInTheDocument()
  })

  it('④ 请求失败：区块内联错误 + 重试，不弹 toast、不崩、表单仍可保存', async () => {
    const { toast } = await import('sonner')
    stubGroups([])
    mockInject('get', '/alerts/qq/gateway/status', { kind: 'network' })
    const panel = renderQQDialog()

    const inline = await screen.findByTestId('qq-gateway-error')
    expect(inline.textContent).toContain('读取网关状态失败')
    // 失败只在区块内显示：观测区与用户正在填的表单无关，弹 toast 会盖住真正的表单错误。
    expect(toast.error).not.toHaveBeenCalled()
    expect(within(panel).getByRole('button', { name: '重试' })).toBeInTheDocument()
    // 表单本身未受影响：三要素已填满，保存仍可用。
    expect(within(panel).getByRole('button', { name: '保存' })).toBeEnabled()
  })

  it('⑤ 已发现群 0 条：计数 0 + 空态文案 + 免责说明常在', async () => {
    stubGateway({ appId: '102000001', status: 'connected', lastEventAt: null, lastError: '' })
    stubGroups([])
    renderQQDialog()

    await screen.findByTestId('qq-groups-empty')
    expect(screen.getByText('已发现群（0）')).toBeInTheDocument()
    expect(screen.getByTestId('qq-groups-empty').textContent).toContain('把机器人拉进群')
    // 免责文案不因 0 条而隐藏——它解释的是「为什么这里不能用来建通道」。
    expect(screen.getByText(/这些群当前无法接收告警/)).toBeInTheDocument()
    expect(screen.queryByTestId('qq-groups-list')).not.toBeInTheDocument()
  })

  it('⑥ 已发现群 N 条：计数取自后端 total、openid 只显前 8 位、附首次/最近发现时间', async () => {
    stubGateway({ appId: '102000001', status: 'connected', lastEventAt: null, lastError: '' })
    // total 给 25（大于本页 2 条）：计数必须用信封 total，否则多群时会静默少报。
    stubGroups([GROUP_A, GROUP_B], 25)
    renderQQDialog()

    expect(await screen.findByText('已发现群（25）')).toBeInTheDocument()
    // 折叠区默认收起（不把主动作顶出视口），点开后才有列表。
    await userEvent.click(screen.getByText('展开群列表（本页 2 个）'))

    const list = within(await screen.findByTestId('qq-groups-list'))
    const items = list.getAllByRole('listitem')
    expect(items).toHaveLength(2)
    // 截断到前 8 位 + 省略号；完整值留在 title 属性里供核对。
    expect(items[0].textContent).toContain('GROUP_OP…')
    expect(items[0].querySelector('[title]')?.getAttribute('title')).toBe('GROUP_OPENID_DEMO_1')
    expect(items[0].textContent).toContain('首次发现')
    expect(items[0].textContent).toContain('最近')
    // 超出展示上限时给「共 N 个」的交代，不让人误以为只有 8 个。
    expect(screen.getByText(/仅展示最近 8 个，共 25 个/)).toBeInTheDocument()
  })

  it('⑦ 群列表读取失败：内联错误态，不与网关错误互相遮盖', async () => {
    stubGateway({ appId: '102000001', status: 'connected', lastEventAt: null, lastError: '' })
    mockInject('get', '/alerts/qq/groups', { kind: 'status', status: 500 })
    renderQQDialog()

    const inline = await screen.findByTestId('qq-groups-error')
    expect(inline.textContent).toContain('读取已发现群失败')
    // 网关正常：两个区块各自报自己的态，不因为一个失败就把整块变成错误。
    const status = screen.getByTestId('qq-gateway-status')
    expect(within(status).getByText('已连接')).toBeInTheDocument()
  })

  it('⑧ 只读边界：呈现已发现群的同时，仍然没有任何群通道入口', async () => {
    stubGateway({ appId: '102000001', status: 'connected', lastEventAt: null, lastError: '' })
    stubGroups([GROUP_A, GROUP_B])
    const panel = renderQQDialog()
    await screen.findByText('已发现群（2）')

    // 群列表是观测，不是选择器：没有下拉、没有单选、没有「从列表选择」切换。
    expect(within(panel).getAllByRole('combobox')).toHaveLength(1) // 只剩「通道类型」
    expect(within(panel).queryByRole('listbox')).not.toBeInTheDocument()
    expect(within(panel).queryByRole('radio')).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '从列表选择' })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '手工输入' })).not.toBeInTheDocument()
    // 目标类型仍是只读文案（不因为有了群列表就恢复控件）。
    expect(within(panel).getByText('单聊（平台不支持群主动消息）')).toBeInTheDocument()
    // 群条目本身不可点击/不可选：它们不是按钮。
    const list = within(await screen.findByTestId('qq-groups-list'))
    expect(list.queryAllByRole('button')).toHaveLength(0)
  })

  it('⑨ 无 alert.manage：区块不渲染，且两个端点一次也不请求', async () => {
    let gatewayCalls = 0
    let groupCalls = 0
    server.use(
      // 让「前端权限态」与「后端真相」一致：只读账号。
      // 为什么必须同时覆盖 /auth/me：`loginMockUser()` 默认发的是**平台管理员** token，
      // auth store 登录后异步拉 /auth/me 并 `setFromAuthMe` 覆写权限 store。只写本地
      // setState、不管这个响应，等于赌「setState 比网络落地更快」——赌赢了测试绿，
      // 赌输了（本次实测）面板挂载并发请求。改成身份层面就是只读，时序不再参与结论。
      domainRoute('get', '/auth/me', () =>
        HttpResponse.json({
          userId: 1,
          username: 'viewer',
          role: 0,
          roleKey: 'member',
          isPlatformAdmin: false,
          nodes: ['alert.read'],
        }),
      ),
      domainRoute('get', '/alerts/qq/gateway/status', () => {
        gatewayCalls += 1
        return HttpResponse.json({ appId: '102000001', status: 'connected', lastEventAt: null, lastError: '' })
      }),
      domainRoute('get', '/alerts/qq/groups', () => {
        groupCalls += 1
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    loginMockUser()
    // `loginMockUser()` 对管理员 token 会**同步**写入全权限，而 `/auth/me` 的续体是异步、且
    // 不被 flush 等待的（它 await 的是网络）。所以这里必须用 waitFor 等到权限真的落地为
    // 只读再渲染：提前渲染就是拿旧权限去测，结果与实现无关、只与时序有关。
    await waitFor(() => expect(usePermissionsStore.getState().hasPerm('alert.manage')).toBe(false))

    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    // 表单本身照常渲染（这是编辑入口，与观测区无关）。
    expect(within(panel).getByText('AppID')).toBeInTheDocument()
    expect(within(panel).queryByText('接入状态（只读）')).not.toBeInTheDocument()
    // 关键：不是「渲染了但请求失败」，而是根本不发请求（后端两个端点都挂 alert.manage）。
    // 用固定等待面（不是轮询断言）是为了让潜在请求有时间显形，而不是一看到 0 就过。
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(gatewayCalls).toBe(0)
    expect(groupCalls).toBe(0)
  })
})
