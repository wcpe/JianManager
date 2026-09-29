import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, fireEvent, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { domainRoute, mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { QQ_BIND_POLL_MS, type AlertChannelInfo, type ChannelBody } from '@/api/alerts'
import { ChannelDialog } from './ChannelDialog'
import { MAX_QQ_BIND_AUTO_REFRESH } from './QQBindDialog'

/**
 * ChannelDialog 强断言（FR-208 通知通道对话框）：直接渲染组件并传 props。
 * 共享 Dialog 语义由 role=dialog 锁定。覆盖：字段渲染 + 编辑回填、
 * 创建/更新提交成功（POST/PUT 命中、onClose 回调被调）、凭证 ${ENV} 预校验闸、
 * 500 注入显错误态不崩。渲染前 loginMockUser() 让 requireAuth 保护的 /alerts/* 放行。
 *
 * 注：「测试发送」按钮位于 AlertsPage ChannelsTab（页面，非本对话框），不在本文件区域内，
 * 故此处不测；其端点 POST /alerts/channels/:id/test 由页面用例覆盖。
 */

/** 已存在的 webhook 通道（编辑回填靶子）。config.url 为明文（非 ${ENV}），触发预校验闸。 */
const existingWebhook: AlertChannelInfo = {
  id: 1,
  uuid: 'chan-webhook',
  name: '运维 Webhook',
  type: 'webhook',
  enabled: true,
  config: JSON.stringify({ url: 'https://hooks.example.com/ops' }),
  createdAt: new Date().toISOString(),
}

/**
 * 已存在的 QQ 通道（编辑态直接落到 QQ 字段区，省去下拉切换这一步）。
 * 三要素留空：扫码回填用例要断言「从空到填满」的完整效果。
 */
const existingQQ: AlertChannelInfo = {
  id: 2,
  uuid: 'chan-qq',
  name: 'QQ 值班',
  type: 'qq',
  enabled: true,
  config: JSON.stringify({ appId: '', appSecret: '', targetId: '', targetType: 'c2c' }),
  createdAt: new Date().toISOString(),
}

/** 取共享 Dialog 面板。 */
function panelByTitle(title: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: title })
  return heading.closest('[role="dialog"]') as HTMLElement
}

async function pickSelectOption(trigger: HTMLElement, optionName: string) {
  HTMLElement.prototype.hasPointerCapture ??= () => false
  HTMLElement.prototype.setPointerCapture ??= () => {}
  HTMLElement.prototype.releasePointerCapture ??= () => {}
  HTMLElement.prototype.scrollIntoView ??= () => {}
  await userEvent.click(trigger.closest('button') ?? trigger)
  await userEvent.click(await screen.findByRole('option', { name: optionName }))
}

describe('ChannelDialog（mock 假后端）', () => {
  it('① 创建模式：默认站内类型渲染名称字段 + 站内提示', async () => {
    loginMockUser()
    renderWithProviders(<ChannelDialog channel={null} onClose={vi.fn()} />)

    const panel = panelByTitle('新建通道')
    // 名称输入 + 启用复选 + 站内类型提示文案。
    expect(within(panel).getAllByRole('textbox').length).toBeGreaterThanOrEqual(1)
    expect(within(panel).getByRole('checkbox', { name: '启用' })).toBeChecked()
    expect(
      within(panel).getByText('站内通知无需外部配置，告警将出现在事件列表中。'),
    ).toBeInTheDocument()
  })

  it('① 编辑模式：webhook 通道回填名称 + URL', async () => {
    loginMockUser()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={vi.fn()} />)

    const panel = panelByTitle('编辑通道')
    const nameInput = within(panel).getAllByRole('textbox')[0] as HTMLInputElement
    expect(nameInput.value).toBe('运维 Webhook')
    // webhook 类型 → URL 字段渲染并回填种子值（明文 url）。
    const urlInput = within(panel).getByPlaceholderText('${JM_DINGTALK_WEBHOOK}') as HTMLInputElement
    expect(urlInput.value).toBe('https://hooks.example.com/ops')
  })

  it('② 创建提交（站内）→ POST /alerts/channels 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

    const panel = panelByTitle('新建通道')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], '钉钉运维群')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 编辑提交：URL 改为 ${ENV} 引用后 → PUT /alerts/channels/:id 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={onClose} />)

    const panel = panelByTitle('编辑通道')
    const urlInput = within(panel).getByPlaceholderText('${JM_DINGTALK_WEBHOOK}')
    await userEvent.clear(urlInput)
    // userEvent.type 把 { 视作特殊键起始，须 {{ 转义出字面 {；} 本身是字面量。
    await userEvent.type(urlInput, '${{JM_OPS_WEBHOOK}')
    expect((urlInput as HTMLInputElement).value).toBe('${JM_OPS_WEBHOOK}')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 凭证预校验闸：明文 URL（非 ${ENV}）→ 显错误且保存禁用，不提交', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={onClose} />)

    const panel = panelByTitle('编辑通道')
    // 种子 URL 为明文 https://...，非 ${ENV} 引用 → 校验失败文案 + 保存禁用。
    expect(within(panel).getByText('凭证须以 ${ENV_VAR} 形式引用环境变量')).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: '保存' })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('② Telegram：token/chatId 必填，token 只能使用 ${ENV} 引用', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

    const panel = panelByTitle('新建通道')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], 'Telegram 告警')
    await pickSelectOption(within(panel).getByText('站内通知'), 'Telegram')

    const save = within(panel).getByRole('button', { name: '保存' })
    expect(save).toBeDisabled()
    expect(within(panel).getAllByText('此项必填').length).toBeGreaterThanOrEqual(2)

    const token = within(panel).getByPlaceholderText('${JM_TELEGRAM_TOKEN}')
    await userEvent.type(token, 'plain-token')
    expect(within(panel).getByText('凭证须以 ${ENV_VAR} 形式引用环境变量')).toBeInTheDocument()
    expect(save).toBeDisabled()

    await userEvent.clear(token)
    await userEvent.type(token, '${{JM_TELEGRAM_TOKEN}')
    await userEvent.type(within(panel).getAllByRole('textbox')[2], '-100123456')
    await userEvent.click(save)

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② QQ：字段按类型渲染，明文 AppSecret 被拦，${ENV} 引用后可提交（FR-494）', async () => {
    loginMockUser()
    // 捕获提交体，锁定与后端 ChannelConfig 契约一致的小驼峰字段名。
    const sent: ChannelBody[] = []
    server.use(
      domainRoute('post', '/alerts/channels', async ({ request }) => {
        sent.push((await request.json()) as ChannelBody)
        return HttpResponse.json({ id: 99 }, { status: 200 })
      }),
    )
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

    const panel = panelByTitle('新建通道')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], 'QQ 值班')
    await pickSelectOption(within(panel).getByText('站内通知'), 'QQ 机器人')

    // qq 专属字段集：名称 + AppID + AppSecret + 目标 OpenID + API 地址 = 5 个文本框；
    // 目标类型固定单聊（不再暴露该字段），故只剩「通道类型」一个下拉。
    const secret = within(panel).getByPlaceholderText('${QQ-102000001}')
    const baseUrl = within(panel).getByPlaceholderText('https://api.bot.qq.com')
    expect(within(panel).getAllByRole('textbox')).toHaveLength(5)
    expect(within(panel).getAllByRole('combobox')).toHaveLength(1)
    expect(within(panel).getByText('AppID')).toBeInTheDocument()
    expect(within(panel).getByText('目标 OpenID')).toBeInTheDocument()
    // 群聊路径已下线：目标类型是只读文案，页面上不再有任何群选项。
    expect(within(panel).getByText('单聊（平台不支持群主动消息）')).toBeInTheDocument()

    const save = within(panel).getByRole('button', { name: '保存' })
    // AppID / AppSecret / 目标 OpenID 三项必填全空 → 保存禁用。
    expect(save).toBeDisabled()
    expect(within(panel).getAllByText('此项必填').length).toBeGreaterThanOrEqual(3)

    const boxes = within(panel).getAllByRole('textbox')
    await userEvent.type(boxes[1], '102000001') // AppID
    await userEvent.type(boxes[3], 'USER_OPENID_C2C') // 目标 openid
    // 明文 AppSecret（非 ${ENV}）→ 校验失败文案 + 保存仍禁用。
    await userEvent.type(secret, 'plain-secret')
    expect(within(panel).getByText('凭证须以 ${ENV_VAR} 形式引用环境变量')).toBeInTheDocument()
    expect(save).toBeDisabled()

    await userEvent.clear(secret)
    // 带连字符的引用名必须通过校验——后端扫码绑定返回的就是 ${QQ-<appId>} 这个形状。
    await userEvent.type(secret, '${{QQ-102000001}')
    expect((secret as HTMLInputElement).value).toBe('${QQ-102000001}')
    expect(within(panel).queryByText('凭证须以 ${ENV_VAR} 形式引用环境变量')).not.toBeInTheDocument()
    // 可选 API 地址留空也应放行（后端取默认值）。
    expect((baseUrl as HTMLInputElement).value).toBe('')
    expect(save).toBeEnabled()
    await userEvent.click(save)

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(sent).toHaveLength(1))
    // AppSecret 以 ${ENV} 引用原样提交，明文与凭证不落库；baseUrl 未填则不提交该字段。
    // toMatchObject 只做子集匹配，故 baseUrl 的缺席需单独断言。
    expect(sent[0].config).not.toHaveProperty('baseUrl')
    expect(sent[0]).toMatchObject({
      name: 'QQ 值班',
      type: 'qq',
      enabled: true,
      config: {
        appId: '102000001',
        appSecret: '${QQ-102000001}',
        targetType: 'c2c',
        targetId: 'USER_OPENID_C2C',
      },
    })
  })

  it('③ 注入 500：提交后对话框不关闭、字段保留（非崩溃，错误态）', async () => {
    // mutateAsync 在 onError(toast) 后仍 reject；吞掉本用例内的未处理拒绝避免污染 runner。
    const onRejection = () => {}
    process.on('unhandledRejection', onRejection)
    try {
      loginMockUser()
      mockInject('post', '/alerts/channels', { kind: 'status', status: 500 })
      const onClose = vi.fn()
      renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

      const panel = panelByTitle('新建通道')
      const nameInput = within(panel).getAllByRole('textbox')[0]
      await userEvent.type(nameInput, '飞书群')
      await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

      // 失败（onError 弹 toast，不调 onClose）→ 对话框仍在、未崩溃、已填值保留。
      await waitFor(() => expect((nameInput as HTMLInputElement).value).toBe('飞书群'))
      expect(onClose).not.toHaveBeenCalled()
      expect(screen.getByRole('heading', { name: '新建通道' })).toBeInTheDocument()
    } finally {
      process.off('unhandledRejection', onRejection)
    }
  })
})

/**
 * FR-494 接入体验改造：通道对话框内置「扫码绑定」，并把群聊路径从界面彻底删除。
 *
 * 轮询用例用假计时器驱动 2s 节拍（同 tasks.polling.dom.test.tsx 套路）：真等 2s 会让用例
 * 又慢又飘。只伪造 setTimeout/clearTimeout，避免波及 React 调度、Radix 与 Date。
 * 因 @testing-library/dom 在 vitest 下不认假计时器（其 jest 探测只认 jest 全局），
 * 这些用例统一用 fireEvent + 显式推进，不用 findBy/waitFor。
 */
describe('ChannelDialog QQ 扫码绑定与群聊下线（FR-494，mock 假后端）', () => {
  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  /** 推进假计时器，并多推一次 0ms 冲刷由此产生的微任务与 0 延迟定时器（mock 响应异步落地）。 */
  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
  }

  /** 点「扫码绑定」并等二维码渲染出来。 */
  async function openBindDialog(panel: HTMLElement): Promise<HTMLElement> {
    fireEvent.click(within(panel).getByRole('button', { name: '扫码绑定' }))
    await advance(0)
    return screen.getByTestId('qq-bind-qrcode')
  }

  it('② 扫码：出二维码 + 链接兜底；轮询到 completed 后回填 AppID / 引用名 / 目标 openid', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    loginMockUser()
    const polls: string[] = []
    server.use(
      domainRoute('get', '/alerts/qq/bind-task/:taskId', ({ params }) => {
        polls.push(String(params.taskId))
        // 先 pending 后 completed：覆盖「等待扫码」的过渡，而不是一上来就完成。
        if (polls.length === 1) return HttpResponse.json({ status: 'pending' })
        return HttpResponse.json({
          status: 'completed',
          appId: '102000001',
          userOpenid: 'USER_OPENID_DEMO_1',
          secretEnv: '${QQ-102000001}',
          // 诱饵：即便后端多带了明文密钥，前端也只认引用名（密钥绝不落进表单）。
          appSecret: 'PLAINTEXT',
        })
      }),
    )
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    const qr = await openBindDialog(panel)
    // 二维码渲染的是后端给的扫码链接：svg 的 aria-label/title 即该 URL，且确有图形路径。
    const qrUrl = qr.getAttribute('aria-label') ?? ''
    expect(qrUrl).toContain('https://q.qq.com/qqbot/openclaw/connect.html')
    expect(qrUrl).toContain('source=JianManager')
    expect(qr.querySelectorAll('path').length).toBeGreaterThan(0)
    // 链接文本兜底（二维码扫不出来时可在手机 QQ 打开）。
    expect(screen.getByTestId('qq-bind-url').textContent).toBe(qrUrl)
    expect(screen.getByRole('status').textContent).toContain('等待扫码')

    // 第一拍 pending：仍是等待态，表单未被动过。
    await advance(QQ_BIND_POLL_MS)
    expect(polls).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toContain('等待扫码')

    // 第二拍 completed：回填表单并自动关闭扫码弹窗。
    await advance(QQ_BIND_POLL_MS)
    expect(polls).toHaveLength(2)
    expect(screen.queryByTestId('qq-bind-qrcode')).not.toBeInTheDocument()

    const boxes = within(panel).getAllByRole('textbox') as HTMLInputElement[]
    expect(boxes[1].value).toBe('102000001')
    // 填的是后端返回的引用名，不是明文密钥（哪怕响应里带了明文）。
    expect(boxes[2].value).toBe('${QQ-102000001}')
    expect(boxes[2].value).not.toBe('PLAINTEXT')
    expect(boxes[3].value).toBe('USER_OPENID_DEMO_1')
    // 三要素齐备 → 保存可用；页面内提示已获取。
    expect(within(panel).getByRole('button', { name: '保存' })).toBeEnabled()
    expect(within(panel).getByRole('status').textContent).toContain('已获取凭证')
  })

  it('② 二维码过期 → 自动重新申请并提示「二维码已刷新」', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    loginMockUser()
    let created = 0
    server.use(
      domainRoute('post', '/alerts/qq/bind-task', () => {
        created += 1
        return HttpResponse.json({
          taskId: `demo-${created}`,
          qrUrl: `https://q.qq.com/qqbot/openclaw/connect.html?task_id=demo-${created}&source=JianManager&_wv=2`,
        })
      }),
      // 第一张码过期；第二张保持 pending，让弹窗留在等待态以便断言。
      domainRoute('get', '/alerts/qq/bind-task/:taskId', ({ params }) =>
        HttpResponse.json({ status: String(params.taskId) === 'demo-1' ? 'expired' : 'pending' }),
      ),
    )
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    await openBindDialog(panel)
    expect(created).toBe(1)

    await advance(QQ_BIND_POLL_MS)

    // 过期即自动重新申请：新 taskId、新二维码，并给出「已刷新」提示。
    expect(created).toBe(2)
    expect(screen.getByRole('status').textContent).toBe('二维码已刷新，请用最新的二维码重新扫码。')
    expect(screen.getByTestId('qq-bind-qrcode').getAttribute('aria-label')).toContain('task_id=demo-2')
  })

  it('② 关闭扫码弹窗后停止轮询：不再查询绑定结果', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    loginMockUser()
    let polls = 0
    server.use(
      domainRoute('get', '/alerts/qq/bind-task/:taskId', () => {
        polls += 1
        return HttpResponse.json({ status: 'pending' })
      }),
    )
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    await openBindDialog(panel)
    await advance(QQ_BIND_POLL_MS)
    expect(polls).toBe(1)

    fireEvent.click(screen.getByRole('button', { name: '手工填写' }))
    await advance(0)
    expect(screen.queryByTestId('qq-bind-qrcode')).not.toBeInTheDocument()

    // 弹窗已卸载：定时器被清理，再推进多拍也不应有任何绑定查询。
    await advance(QQ_BIND_POLL_MS * 5)
    expect(polls).toBe(1)
  })

  it('② 查询绑定结果失败（网络）→ 给可操作文案并停止轮询（不空转烧额度）', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    loginMockUser()
    let polls = 0
    server.use(
      domainRoute('get', '/alerts/qq/bind-task/:taskId', () => {
        polls += 1
        return HttpResponse.error()
      }),
    )
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    await openBindDialog(panel)
    await advance(QQ_BIND_POLL_MS)

    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('查询绑定结果失败')
    // 文案要给「怎么办」：重试入口与手工兜底都在。
    expect(alert.textContent).toContain('手工填写')
    expect(screen.getByRole('button', { name: '重新生成' })).toBeInTheDocument()

    // 失败后不再继续打接口：平台有频率限制，空转会把额度打光。
    await advance(QQ_BIND_POLL_MS * 5)
    expect(polls).toBe(1)
  })

  it('② 二维码反复过期 → 自动重新申请有上限，到顶后停止并提示手工兜底', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    loginMockUser()
    let created = 0
    server.use(
      domainRoute('post', '/alerts/qq/bind-task', () => {
        created += 1
        return HttpResponse.json({
          taskId: `demo-${created}`,
          qrUrl: `https://q.qq.com/qqbot/openclaw/connect.html?task_id=demo-${created}&source=JianManager&_wv=2`,
        })
      }),
      // 永远过期：看自动重申请会不会无限打平台接口。
      domainRoute('get', '/alerts/qq/bind-task/:taskId', () => HttpResponse.json({ status: 'expired' })),
    )
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)
    const panel = panelByTitle('编辑通道')

    await openBindDialog(panel)
    for (let i = 0; i < MAX_QQ_BIND_AUTO_REFRESH + 1; i += 1) {
      await advance(QQ_BIND_POLL_MS)
    }

    // 首次 + 自动重申请 MAX 次后停下来（平台限流约 9 次，不能无限重申请）。
    expect(created).toBe(MAX_QQ_BIND_AUTO_REFRESH + 1)
    expect(screen.getByRole('alert').textContent).toContain('二维码多次过期')
    expect(screen.getByRole('button', { name: '重新生成二维码' })).toBeInTheDocument()

    await advance(QQ_BIND_POLL_MS * 5)
    expect(created).toBe(MAX_QQ_BIND_AUTO_REFRESH + 1)
  })

  it('② 申请二维码失败（502）→ 给可操作文案，且手工填写路径仍可用', async () => {
    loginMockUser()
    mockInject('post', '/alerts/qq/bind-task', { kind: 'status', status: 502 })
    const sent: ChannelBody[] = []
    server.use(
      domainRoute('post', '/alerts/channels', async ({ request }) => {
        sent.push((await request.json()) as ChannelBody)
        return HttpResponse.json({ id: 102 }, { status: 200 })
      }),
    )
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)
    const panel = panelByTitle('新建通道')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], 'QQ 值班')
    await pickSelectOption(within(panel).getByText('站内通知'), 'QQ 机器人')

    await userEvent.click(within(panel).getByRole('button', { name: '扫码绑定' }))
    // 失败文案要说清「怎么办」，而不是丢一个裸错误码。
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('生成二维码失败')
    expect(alert.textContent).toContain('手工填写')

    // 关掉扫码弹窗后手工填写照常可用：三要素填齐即能保存（扫码只是加速路径）。
    await userEvent.click(screen.getByRole('button', { name: '手工填写' }))
    expect(screen.queryByTestId('qq-bind-qrcode')).not.toBeInTheDocument()
    const boxes = within(panel).getAllByRole('textbox')
    await userEvent.type(boxes[1], '102000001')
    await userEvent.type(boxes[2], '${{QQ-102000001}')
    await userEvent.type(boxes[3], 'USER_OPENID_MANUAL')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(sent).toHaveLength(1))
    expect(sent[0].config).toMatchObject({
      appId: '102000001',
      appSecret: '${QQ-102000001}',
      targetType: 'c2c',
      targetId: 'USER_OPENID_MANUAL',
    })
  })

  it('② 群聊彻底下线：无目标类型下拉、无「已发现群」区块、无群选择交互', async () => {
    loginMockUser()
    renderWithProviders(<ChannelDialog channel={existingQQ} onClose={vi.fn()} />)

    const panel = panelByTitle('编辑通道')
    // 只剩「通道类型」一个下拉；目标类型退化为只读文案「单聊」。
    expect(within(panel).getAllByRole('combobox')).toHaveLength(1)
    expect(within(panel).getByText('单聊（平台不支持群主动消息）')).toBeInTheDocument()
    expect(within(panel).queryByText('群聊（平台暂不支持）')).not.toBeInTheDocument()
    expect(within(panel).queryByText('请选择目标类型')).not.toBeInTheDocument()
    // 「已发现群」下拉、空态引导与手工/列表切换全部移除。
    expect(within(panel).queryByText('已发现群')).not.toBeInTheDocument()
    expect(within(panel).queryByText('从已发现群中选择')).not.toBeInTheDocument()
    expect(within(panel).queryByText('暂无已发现群，请先前往「QQ 接入」扫码把机器人拉进群。')).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '手工输入' })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '从列表选择' })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '前往 QQ 接入' })).not.toBeInTheDocument()
    // 目标 openid 回到纯手工输入框（单聊 user_openid）。
    expect(within(panel).getAllByRole('textbox')).toHaveLength(5)
  })

  it('② 存量群聊通道：保存时 targetType 被纠正为 c2c 并给出提示', async () => {
    loginMockUser()
    const legacyGroup: AlertChannelInfo = {
      ...existingQQ,
      config: JSON.stringify({ appId: '102000001', appSecret: '${QQ-102000001}', targetId: 'GROUP_OPENID_DEMO_1', targetType: 'group' }),
    }
    const sent: ChannelBody[] = []
    server.use(
      domainRoute('put', '/alerts/channels/:id', async ({ request }) => {
        sent.push((await request.json()) as ChannelBody)
        return HttpResponse.json({ id: 2 }, { status: 200 })
      }),
    )
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={legacyGroup} onClose={onClose} />)

    const panel = panelByTitle('编辑通道')
    // 不阻塞保存（该字段没有可编辑控件），但明确告知已改为单聊。
    expect(within(panel).getByText(/该通道原为群聊目标/)).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(sent).toHaveLength(1))
    expect(sent[0].config).toMatchObject({ targetType: 'c2c' })
  })
})
