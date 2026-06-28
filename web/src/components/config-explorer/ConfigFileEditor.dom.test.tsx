import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import ConfigFileEditor from './ConfigFileEditor'

/**
 * ConfigFileEditor 强断言（FR-205 配置域代表组件）：验 configs 域 mock 读写联动 + 错误注入。
 * 用表单模式（schema 命中的 server.properties）断言——字段为原生 input，jsdom 下可查可改，
 * 避开文本模式 CodeMirror 在 jsdom 的不可靠渲染。
 */
const noop = () => {}

function renderEditor(path = 'server.properties', name = 'server.properties') {
  return renderWithProviders(
    <ConfigFileEditor
      instanceId={1}
      path={path}
      name={name}
      onClose={noop}
      onAfterSave={noop}
      onOpenVersions={noop}
      onDirtyChange={noop}
    />,
  )
}

describe('ConfigFileEditor（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser() // 受保护的 /configs/* 端点需有效 session
  })

  it('渲染种子：读出 server.properties 字段值（motd / max-players）+ valid 徽标', async () => {
    const user = userEvent.setup()
    renderEditor()
    // 切到表单模式（schema 命中后按钮可用），字段为原生 input。
    await waitFor(() => expect(screen.getByRole('button', { name: '表单' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: '表单' }))
    expect(await screen.findByDisplayValue('A Mock Minecraft Server')).toBeInTheDocument()
    expect(screen.getByDisplayValue('20')).toBeInTheDocument()
    expect(screen.getByText('valid')).toBeInTheDocument()
  })

  it('交互：改字段 → 保存可用 → 写入成功（versionId 联动，提示已保存）', async () => {
    const user = userEvent.setup()
    const onAfterSave = vi.fn()
    renderWithProviders(
      <ConfigFileEditor
        instanceId={1}
        path="server.properties"
        name="server.properties"
        onClose={noop}
        onAfterSave={onAfterSave}
        onOpenVersions={noop}
        onDirtyChange={noop}
      />,
    )
    await waitFor(() => expect(screen.getByRole('button', { name: '表单' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: '表单' }))
    const motd = await screen.findByDisplayValue('A Mock Minecraft Server')
    // 改值前保存按钮禁用（无改动）；改值后启用。
    const saveBtn = screen.getByRole('button', { name: /保存/ })
    expect(saveBtn).toBeDisabled()
    await user.clear(motd)
    await user.type(motd, 'Edited MOTD')
    expect(screen.getByRole('button', { name: /保存/ })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: /保存/ }))
    // write-fields 成功后回调 onAfterSave（versionId 由 mock 自增返回）。
    await waitFor(() => expect(onAfterSave).toHaveBeenCalled())
  })

  it('注入 500（GET configs/read）→ 显示错误态而非崩溃', async () => {
    mockInject('get', '/instances/:id/configs/read', { kind: 'status', status: 500 })
    renderEditor()
    // 组件渲染 readQ.error.message（axios 默认 "Request failed with status code 500"）。
    expect(await screen.findByText(/status code 500|读取失败|失败/)).toBeInTheDocument()
  })
})
