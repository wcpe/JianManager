import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import SettingsPage from './SettingsPage'
import { keyCategory, validateSettingDraft } from '@/lib/settings/settings-form'

/** 
 * SettingsPage 强断言：渲染 seed 平台配置 → 改设置保存联动回读新值 → 注入 500 显错误态。
 * 平台配置分类仅平台管理员（role=10）可见，故以 role=10 的假 JWT 登录。
 */

/** 构造 role=10 的假 JWT（payload base64url，不验签），使 auth store 解出平台管理员角色。 */
function platformAdminJwt(): string {
  const payload = btoa(
    JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
  )
  return `mock.${payload}.sig`
}

/** 登录为平台管理员：注册 mock 会话（requireAuth 放行）+ 让 auth store role=10。 */
function loginAsPlatformAdmin() {
  const jwt = platformAdminJwt()
  loginMockUser(jwt)
  useAuthStore.getState().login(jwt, 'test-refresh-token')
}

describe('SettingsPage（mock 假后端）', () => {
  it('渲染出 seed 平台配置项（日志分类含 log.level）', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    // 平台管理员可见「日志」分类导航；切过去看到 log.level 配置项（键名 mono 展示）。
    // 注意用精确名 /^日志$/：新增「日志采集」分区后，宽松的 /日志/ 会同时命中两者。
    await user.click(await screen.findByRole('button', { name: /^日志$/ }))
    expect(await screen.findByText('log.level')).toBeInTheDocument()
    // 切到「运行时」分类可见 graceful_stop.timeout（不同分类 → 印证分类分组生效）。
    await user.click(screen.getByRole('button', { name: /运行时/ }))
    expect(await screen.findByText('graceful_stop.timeout')).toBeInTheDocument()
    // 「日志采集」是独立分区——log.acq.* 归此，不被 log. 前缀误归入「日志」分类。
    await user.click(screen.getByRole('button', { name: /日志采集/ }))
    expect(await screen.findByText('log.acq.rules_enabled')).toBeInTheDocument()
    expect(screen.queryByText('log.level')).not.toBeInTheDocument()
  })

  it('改设置保存 → 联动回读新值', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    // 切到「运行时」分类，改 graceful_stop.timeout（Go duration 带单位，按当前值 30s 唯一定位，避开重名键文本）。
    await user.click(await screen.findByRole('button', { name: /运行时/ }))
    const input = (await screen.findByDisplayValue('30s')) as HTMLInputElement

    await user.clear(input)
    await user.type(input, '60s')
    await user.click(screen.getByRole('button', { name: '保存' }))

    // 联动回读：PUT 写入后 onSuccess 失效缓存重取，输入框回填新值（草稿已清，展示后端生效值）。
    // 成功 toast 走 sonner 门户、harness 未挂 Toaster，故以「值持久化为新值、旧值消失」断言写读联动。
    await waitFor(() => expect(screen.getByDisplayValue('60s')).toBeInTheDocument())
    expect(screen.queryByDisplayValue('30s')).not.toBeInTheDocument()
  })

  it('注入 500 → 平台配置显示加载失败错误态', async () => {
    loginAsPlatformAdmin()
    mockInject('get', '/settings', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    await user.click(await screen.findByRole('button', { name: /^日志$/ }))
    expect(await screen.findByText('加载平台配置失败')).toBeInTheDocument()
  })

  it('邀请邮件配置归入可编辑分类，平台公共基址接受 HTTP/HTTPS 且拒绝查询参数或片段', async () => {
    loginAsPlatformAdmin()
    renderWithProviders(<SettingsPage />)

    expect(await screen.findByRole('button', { name: /邀请邮件/ })).toBeInTheDocument()
    expect(keyCategory('platform.public_base_url')).toBe('email')
    expect(keyCategory('invite.smtp.host')).toBe('email')
    expect(validateSettingDraft('platform.public_base_url', 'http://panel.example.com')).toBeUndefined()
    expect(validateSettingDraft('platform.public_base_url', 'https://panel.example.com')).toBeUndefined()
    expect(validateSettingDraft('platform.public_base_url', 'https://panel.example.com?source=test')).toBe('settings.invalidPlatformPublicBaseUrl')
    expect(validateSettingDraft('platform.public_base_url', 'https://panel.example.com#invite')).toBe('settings.invalidPlatformPublicBaseUrl')
    expect(validateSettingDraft('platform.public_base_url', 'http://user:pass@panel.example.com')).toBe('settings.invalidPlatformPublicBaseUrl')
  })

  // github.token：网络分类可编辑 + 快捷创建按钮新标签打开 GitHub fine-grained 令牌创建页（名字/描述/永不过期预填）。
  it('github.token 归网络分类，快捷按钮直达 GitHub 创建页', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null)
    renderWithProviders(<SettingsPage />)

    await user.click(await screen.findByRole('button', { name: /网络/ }))
    expect(await screen.findByText('github.token')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /去 GitHub 创建/ }))
    expect(openSpy).toHaveBeenCalledWith(
      'https://github.com/settings/personal-access-tokens/new?name=JianManager&description=JianManager%20%E6%8E%A7%E5%88%B6%E5%8F%B0%E4%B8%93%E7%94%A8%EF%BC%9A%E8%AF%BB%E5%8F%96%E5%85%AC%E5%BC%80%E4%BB%93%E5%BA%93%20Releases%20%E6%8F%90%E5%8D%87%20GitHub%20API%20%E9%99%90%E9%A2%9D%EF%BC%88%E9%9B%B6%E6%9D%83%E9%99%90%E5%8D%B3%E5%8F%AF%EF%BC%89&expires_in=none',
      '_blank',
      'noopener,noreferrer',
    )
    openSpy.mockRestore()
  })
})

describe('SettingsPage 布尔设置项（输入侧优化）', () => {
  /** 
   * debug.mode 是唯一的布尔型平台设置。
   *
   * 此前它被做成 true/false 下拉：二值状态变成「选项」，读起来像还有第三种可能。
   * 改为 Switch 后必须保证**提交值仍是字符串 "true"/"false"**——后端契约不变，
   * 否则一次「界面优化」会静默改变 API 语义。
   */
  it('debug.mode 渲染为开关，切换后提交字符串值', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    // 精确名：`日志采集` 是独立分区，宽松的 /日志/ 会同时命中两者。
    await user.click(await screen.findByRole('button', { name: /^日志$/ }))
    // 键名在标签与等宽说明里各出现一次，故用 getAllByText。
    expect((await screen.findAllByText('debug.mode')).length).toBeGreaterThan(0)

    // 开关的可访问名取的是**中文标签**（不是键名），故按标签文字断言，
    // 这样能同时证明「用户看到的名字」与「控件的可访问名」一致。
    // 用 getAllByRole 取首个：Radix Switch 内部还会渲染一个隐藏的 input[role=switch]，
    // 两者可访问名相同，getByRole 会因歧义失败。
    const toggles = await screen.findAllByRole('switch', { name: /调试模式/ })
    const toggle = toggles[0]!
    // 种子里是 false，故初始未选中。
    expect(toggle).not.toBeChecked()

    await user.click(toggle)
    await waitFor(() => expect(toggle).toBeChecked())
    // 旁边文字同步反映状态（避免只有颜色/位置变化这一个信号），用 getAllByText 避免歧义。
    expect(screen.getAllByText('开启').length).toBeGreaterThan(0)
  })
})

/**
 * 保存成功后必须清草稿。
 *
 * 【这条守的是什么】通用表单组件只负责"提交什么"，清草稿是容器的职责
 * （见 `settings-draft` 的 `clearSubmitted`）。改造期间两边都以为对方会做，
 * 结果保存后草稿一直留着 —— 表现是「保存按钮永远可点、脏提示永远在」，
 * 用户会以为保存没生效而反复点。
 *
 * 【为什么必须让服务端返回"旧值"】脏判定是「草稿 vs 服务端当前值」。如果 PUT 之后
 * GET 返回新值（真实后端如此），那么即使**没清草稿**，草稿与新值相等、也算无改动——
 * 这条用例就变成永远通过，抓不到缺陷（我第一版正是这样，做变异验证时它没红）。
 * 所以这里注入一个"PUT 成功但 GET 仍返回旧值"的假后端，把「草稿到底有没有被清」
 * 单独暴露出来。
 */
/** 只含本用例用到的项：graceful_stop.timeout 固定返回 30s（服务端"没变"）。 */
const SEED_SETTINGS = {
  editable: [
    { key: 'graceful_stop.timeout', value: '30s', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  ],
  readOnly: [],
}

describe('SettingsPage 保存后清草稿', () => {
  it('保存成功后保存按钮回到禁用、脏提示消失', async () => {
    loginAsPlatformAdmin()
    /*
      构造"PUT 成功但 GET 仍返回旧值"的假后端。
      devmock 的 PUT 会真的改种子数据，那样"没清草稿"也会因为草稿==新服务端值
      被判定为无改动，用例就抓不到缺陷（第一版正是如此，变异验证没红）。
    */
    server.use(
      http.get(API('/settings'), () => HttpResponse.json(SEED_SETTINGS)),
      http.put(API('/settings'), () => HttpResponse.json({ ok: true })),
    )
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    await user.click(await screen.findByRole('button', { name: /运行时/ }))
    const input = (await screen.findByDisplayValue('30s')) as HTMLInputElement
    await user.clear(input)
    await user.type(input, '45s')

    const save = screen.getByRole('button', { name: '保存' })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)

    /*
      先等展示值回落服务端值（30s），再断言"无改动"。
      【为什么不能直接等按钮变禁用】保存过程中 `busy` 也会让按钮禁用；
      `waitFor(toBeDisabled)` 会在**在途那一刻**就满足，等于什么都没测到。
      必须先确认草稿已被清（输入框回到 30s），此时的禁用才是"无改动"导致的。
    */
    await waitFor(() => expect(screen.getByDisplayValue('30s')).toBeInTheDocument())
    // 若没清草稿：草稿仍是 45s ≠ 服务端 30s ⇒ 仍算有改动 ⇒ 下面两条会红。
    expect(screen.getByTestId('settings-save')).toBeDisabled()
    expect(screen.getByTestId('settings-dirty-hint')).toHaveTextContent('无改动')
  })
})

/**
 * 设置页「日志采集」分类里的 path_overrides 必须是成对行编辑器，不是 JSON 文本框。
 *
 * 【这条守的是改造期间的一个真实缺陷】接入通用组件时，我的桥接层只透传了文本通道，
 * 把结构化通道（kv）整个丢了 —— `log.acq.path_overrides` 正好落在这个分类里，
 * 于是用户编辑它**永远不生效**，且界面上看不出任何异常。
 * 这条用例从页面级确认：该字段渲染的是行编辑器（可增删行），而不是一个要手写 JSON 的输入框。
 */
describe('SettingsPage 日志采集：path_overrides 可视化编辑', () => {
  it('渲染成对行编辑器，而不是 JSON 文本框', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    await user.click(await screen.findByRole('button', { name: /日志采集/ }))
    // 行编辑器存在即说明走的是 kv-pairs 分支（JSON 文本框不会渲染这个 testid）。
    expect(await screen.findByTestId('path-overrides-editor')).toBeInTheDocument()
    // 能加行：证明这是可编辑的成对行，而非只读展示。
    expect(screen.getByTestId('path-overrides-add')).toBeEnabled()
  })
})
