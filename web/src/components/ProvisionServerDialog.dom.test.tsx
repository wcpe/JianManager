import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster } from 'sonner'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useInstances } from '@/api/instances'
import ProvisionServerDialog from './ProvisionServerDialog'

/**
 * ProvisionServerDialog 强断言（FR-202 供给/部署流程，POST /instances/provision/bukkit）。
 * 部署对话框依赖跨域 nodes（GET /nodes）+ groups（GET /groups）+ cores（GET /cores）seed handler，
 * 均已在主 mock 树（node.ts / identity.ts / provision.ts）。三条统一断言：
 * ① 打开对话框 → 渲染可选节点（Combobox 列出在线节点）+ 版本列表（GET /cores）；
 * ② 填表提交 → POST 成功、实例域 instances 集合联动新增该实例、对话框关闭；
 * ③ 注入部署端点 500 → 显错误态（toast 失败 + 对话框不关、页面不崩）。
 *
 * FieldLabel 未绑定 htmlFor，按 placeholder/role 定位输入；节点/版本是 Combobox（Radix Popover）。
 */

// Radix（Popover/Select/Dialog）在 jsdom 下依赖以下 API；vitest jsdom 默认缺，按标准配方补齐。
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

/**
 * 部署流程测试壳：渲染对话框 + 一个由 useInstances() 驱动的实例名列表 + Toaster。
 * 部署成功后 useProvisionBukkit 失效 ['instances']，列表重取应出现新实例（集合联动），
 * 且对话框 onClose 后卸载——两者共同构成「部署成功」的强信号。
 */
function DeployHarness() {
  const [open, setOpen] = useState(true)
  const { data: instances } = useInstances()
  return (
    <div>
      <ul aria-label="instances-list">
        {(instances ?? []).map((i) => (
          <li key={i.id}>{i.name}</li>
        ))}
      </ul>
      <ProvisionServerDialog open={open} onClose={() => setOpen(false)} />
      <Toaster />
    </div>
  )
}

/** 通过 Combobox 选项选择一个已知值：点触发器（按当前显示文案）→ 点选项。 */
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

describe('ProvisionServerDialog（mock 假后端，部署流程）', () => {
  it('① 打开对话框 → 渲染可选节点与版本列表', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<DeployHarness />)

    const dialog = await screen.findByText('一键搭建 Paper 子服')
    const panel = dialog.closest('div') as HTMLElement
    expect(panel).toBeInTheDocument()

    // 节点 Combobox：seed 节点 alpha（status=1）可选，beta（status=0 离线）被过滤掉。
    await user.click(screen.getByText('选择节点'))
    expect(await screen.findByRole('button', { name: 'alpha' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'beta' })).not.toBeInTheDocument()
    // 选中 alpha 后触发器回填名称
    await user.click(screen.getByRole('button', { name: 'alpha' }))

    // 版本 Combobox 数据源 GET /cores?type=paper → ['1.21.1','1.21','1.20.6']。
    await user.click(screen.getByText('选择版本'))
    expect(await screen.findByRole('button', { name: '1.21.1' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1.20.6' })).toBeInTheDocument()
  })

  it('② 填写并提交 → 实例集合联动新增 + 对话框关闭', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<DeployHarness />)

    await screen.findByText('一键搭建 Paper 子服')
    // seed 实例先到位，确认基线
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    // 名称（placeholder=lobby）
    await user.type(screen.getByPlaceholderText('lobby'), 'deploy-paper')
    // 节点 + 版本（必填项）
    await pickCombo(user, document.body, '选择节点', 'alpha')
    await pickCombo(user, document.body, '选择版本', '1.21')

    // 提交（按钮文案=搭建）
    await user.click(screen.getByRole('button', { name: '搭建' }))

    // 联动：POST /instances/provision/bukkit 落 instances 集合 → 失效重取 → 列表出现新实例。
    await waitFor(() => expect(within(list).getByText('deploy-paper')).toBeInTheDocument())
    // 对话框关闭（onSuccess→close()→open=false→卸载）。
    await waitFor(() =>
      expect(screen.queryByText('一键搭建 Paper 子服')).not.toBeInTheDocument(),
    )
  })

  it('③ 注入部署端点 500 → 不崩溃、对话框不误关、实例集合不新增', async () => {
    const user = userEvent.setup()
    loginMockUser()
    mockInject('post', '/instances/provision/bukkit', { kind: 'status', status: 500 })
    renderWithProviders(<DeployHarness />)

    await screen.findByText('一键搭建 Paper 子服')
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    await user.type(screen.getByPlaceholderText('lobby'), 'deploy-fail')
    await pickCombo(user, document.body, '选择节点', 'alpha')
    await pickCombo(user, document.body, '选择版本', '1.21')

    await user.click(screen.getByRole('button', { name: '搭建' }))

    // 错误态契约（不依赖 sonner 渲染）：onError 不调 close()→对话框仍在；提交按钮回到可点（脱离「搭建中…」）。
    await waitFor(() => expect(screen.getByRole('button', { name: '搭建' })).toBeEnabled())
    expect(screen.getByText('一键搭建 Paper 子服')).toBeInTheDocument()
    // 失败时 instances 集合不新增该实例（部署端点 500，未落库）。
    expect(within(list).queryByText('deploy-fail')).not.toBeInTheDocument()
  })
})
