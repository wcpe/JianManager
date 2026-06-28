import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster } from 'sonner'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useInstances } from '@/api/instances'
import ProvisionProxyDialog from './ProvisionProxyDialog'

/**
 * ProvisionProxyDialog 强断言（FR-202 供给/部署流程，POST /instances/provision/proxy）。
 * 搭建代理对话框依赖跨域 nodes（GET /nodes）+ groups（GET /groups）+ cores（GET /cores）seed handler。
 * 三条统一断言：① 打开 → 渲染可选节点 + 代理版本列表；② 填表提交 → 代理实例落 instances 集合联动 +
 * 对话框关闭；③ 注入端点 500 → 不崩溃、对话框不误关、集合不新增。
 *
 * 注：POST /instances/provision/proxy 在 network.ts 与 provision.ts 各注册一次；MSW 取首个匹配
 * （import.meta.glob 字母序 network < provision），故由 network.ts 服务，返回 { instance, ... }，
 * 与 api/proxy.ts 的 ProvisionProxyResult 兼容。本测试只断言「成功 + 集合联动」，不依赖具体响应分支。
 */

beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** 搭建代理测试壳：对话框 + useInstances 列表 + Toaster；成功后 ['instances'] 失效、列表联动。 */
function ProxyHarness() {
  const [open, setOpen] = useState(true)
  const { data: instances } = useInstances()
  return (
    <div>
      <ul aria-label="instances-list">
        {(instances ?? []).map((i) => (
          <li key={i.id}>{i.name}</li>
        ))}
      </ul>
      <ProvisionProxyDialog open={open} onClose={() => setOpen(false)} />
      <Toaster />
    </div>
  )
}

async function pickCombo(
  user: ReturnType<typeof userEvent.setup>,
  scope: HTMLElement,
  triggerText: string,
  optionText: string,
) {
  await user.click(within(scope).getByText(triggerText))
  const option = await screen.findByRole('button', { name: optionText })
  await user.click(option)
}

describe('ProvisionProxyDialog（mock 假后端，搭建代理流程）', () => {
  it('① 打开对话框 → 渲染可选节点与代理版本列表', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<ProxyHarness />)

    expect(await screen.findByText('搭建代理（BungeeCord/Waterfall/Velocity）')).toBeInTheDocument()

    // 节点 Combobox：在线节点 alpha 可选，离线 beta 被过滤。
    await user.click(screen.getByText('选择节点'))
    expect(await screen.findByRole('button', { name: 'alpha' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'beta' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'alpha' }))

    // 版本 Combobox（默认 velocity）数据源 GET /cores?type=velocity → SNAPSHOT 列表。
    await user.click(screen.getByText('选择版本'))
    expect(await screen.findByRole('button', { name: '3.3.0-SNAPSHOT' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '3.2.0-SNAPSHOT' })).toBeInTheDocument()
  })

  it('② 填写并提交 → 代理实例集合联动新增 + 对话框关闭', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<ProxyHarness />)

    await screen.findByText('搭建代理（BungeeCord/Waterfall/Velocity）')
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    await user.type(screen.getByPlaceholderText('velocity-main'), 'edge-proxy')
    await pickCombo(user, document.body, '选择节点', 'alpha')
    await pickCombo(user, document.body, '选择版本', '3.3.0-SNAPSHOT')

    await user.click(screen.getByRole('button', { name: '搭建' }))

    // 联动：POST /instances/provision/proxy 落 instances 集合（role=proxy）→ 失效重取 → 列表出现。
    await waitFor(() => expect(within(list).getByText('edge-proxy')).toBeInTheDocument())
    await waitFor(() =>
      expect(
        screen.queryByText('搭建代理（BungeeCord/Waterfall/Velocity）'),
      ).not.toBeInTheDocument(),
    )
  })

  it('③ 注入搭建端点 500 → 不崩溃、对话框不误关、集合不新增', async () => {
    const user = userEvent.setup()
    loginMockUser()
    mockInject('post', '/instances/provision/proxy', { kind: 'status', status: 500 })
    renderWithProviders(<ProxyHarness />)

    await screen.findByText('搭建代理（BungeeCord/Waterfall/Velocity）')
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    await user.type(screen.getByPlaceholderText('velocity-main'), 'edge-fail')
    await pickCombo(user, document.body, '选择节点', 'alpha')
    await pickCombo(user, document.body, '选择版本', '3.3.0-SNAPSHOT')

    await user.click(screen.getByRole('button', { name: '搭建' }))

    // 错误态：onError 不 close→对话框仍在；提交按钮回到可点；集合无新增代理。
    await waitFor(() => expect(screen.getByRole('button', { name: '搭建' })).toBeEnabled())
    expect(screen.getByText('搭建代理（BungeeCord/Waterfall/Velocity）')).toBeInTheDocument()
    expect(within(list).queryByText('edge-fail')).not.toBeInTheDocument()
  })
})
