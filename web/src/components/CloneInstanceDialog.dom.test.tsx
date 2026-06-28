import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster } from 'sonner'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useInstances } from '@/api/instances'
import CloneInstanceDialog from './CloneInstanceDialog'

/**
 * CloneInstanceDialog 强断言（FR-202 供给/部署流程，POST /instances/:id/clone）。
 * 复制对话框依赖跨域 instances（GET /instances?role=proxy）seed handler（主树 instance.ts）。
 * 断言：① 渲染对话框 + 可注册的代理勾选项（seed proxy=lobby-proxy）；② 预检 dryRun 出资源预览、
 * 不落库不关窗；③ 提交复制 → 副本落 instances 集合联动 + 对话框关闭；④ 注入端点 500 → 不崩溃、
 * 对话框不误关、集合不新增。
 *
 * CloneInstanceDialog 无 open prop（挂载即显示），由父组件控制卸载；名称输入无 placeholder，
 * 默认值 `${sourceName}-copy`，按角色取首个 textbox 定位。
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

/** 复制流程测试壳：对话框 + useInstances 全量列表 + Toaster；复制成功后 ['instances'] 失效、列表联动。 */
function CloneHarness() {
  const [mounted, setMounted] = useState(true)
  const { data: instances } = useInstances()
  return (
    <div>
      <ul aria-label="instances-list">
        {(instances ?? []).map((i) => (
          <li key={i.id}>{i.name}</li>
        ))}
      </ul>
      {mounted && (
        <CloneInstanceDialog sourceId={1} sourceName="survival-1" onClose={() => setMounted(false)} />
      )}
      <Toaster />
    </div>
  )
}

describe('CloneInstanceDialog（mock 假后端，复制子服流程）', () => {
  it('① 渲染对话框 + 可注册代理勾选项', async () => {
    loginMockUser()
    renderWithProviders(<CloneHarness />)

    // 标题带源实例名；名称输入默认 `${sourceName}-copy`。
    expect(await screen.findByText('复制子服 — survival-1')).toBeInTheDocument()
    expect(screen.getByDisplayValue('survival-1-copy')).toBeInTheDocument()
    // 注册代理列表来自 GET /instances?role=proxy → seed proxy「lobby-proxy」复选框。
    expect(await screen.findByLabelText('lobby-proxy')).toBeInTheDocument()
  })

  it('② 预检 dryRun → 出资源预览、不落库不关窗', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<CloneHarness />)

    await screen.findByText('复制子服 — survival-1')
    const list = screen.getByLabelText('instances-list')
    // 等 seed 实例列表到位再取基线数量（query 异步解析）。
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())
    const baseCount = within(list).getAllByRole('listitem').length

    await user.click(screen.getByRole('button', { name: '预检' }))

    // dryRun 返回 allocated（端口/目录），渲染「将分配」预览块；mock 端口=25566、目录=/srv/instances/...
    expect(await screen.findByText(/将分配/)).toBeInTheDocument()
    // 预检不落库：实例数量不变，对话框仍在。
    expect(within(list).getAllByRole('listitem').length).toBe(baseCount)
    expect(screen.getByText('复制子服 — survival-1')).toBeInTheDocument()
  })

  it('③ 提交复制 → 副本落集合联动 + 对话框关闭', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<CloneHarness />)

    await screen.findByText('复制子服 — survival-1')
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    // 改名后提交（首个 textbox = 名称输入，FieldLabel 无 htmlFor）。
    const nameInput = within(screen.getByText('复制子服 — survival-1').closest('div') as HTMLElement)
      .getAllByRole('textbox')[0]
    await user.clear(nameInput)
    await user.type(nameInput, 'survival-clone')
    await user.click(screen.getByRole('button', { name: '复制' }))

    // 联动：POST /instances/1/clone（非 dryRun）插入 instances → 失效重取 → 副本出现。
    await waitFor(() => expect(within(list).getByText('survival-clone')).toBeInTheDocument())
    // 成功后 onClose→卸载，对话框消失。
    await waitFor(() =>
      expect(screen.queryByText('复制子服 — survival-1')).not.toBeInTheDocument(),
    )
  })

  it('④ 注入复制端点 500 → 不崩溃、对话框不误关、集合不新增', async () => {
    const user = userEvent.setup()
    loginMockUser()
    mockInject('post', '/instances/:id/clone', { kind: 'status', status: 500 })
    renderWithProviders(<CloneHarness />)

    await screen.findByText('复制子服 — survival-1')
    const list = screen.getByLabelText('instances-list')
    await waitFor(() => expect(within(list).getByText('survival-1')).toBeInTheDocument())

    const nameInput = within(screen.getByText('复制子服 — survival-1').closest('div') as HTMLElement)
      .getAllByRole('textbox')[0]
    await user.clear(nameInput)
    await user.type(nameInput, 'clone-fail')
    await user.click(screen.getByRole('button', { name: '复制' }))

    // 错误态：onError 不 close→对话框仍在；提交按钮回到可点；集合无新增副本。
    await waitFor(() => expect(screen.getByRole('button', { name: '复制' })).toBeEnabled())
    expect(screen.getByText('复制子服 — survival-1')).toBeInTheDocument()
    expect(within(list).queryByText('clone-fail')).not.toBeInTheDocument()
  })
})
