import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { toast, Toaster } from 'sonner'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import App from '@/App'
import { useStartInstance, useStopInstance, useRestartInstance, useKillInstance, useDeleteInstance } from '@/api/instances'
import ResourceExplorer from '@/components/explorer/ResourceExplorer'
import CreateInstanceDialog from '@/components/CreateInstanceDialog'

/**
 * FR-030 前端通知系统与 UX 标准化回归：
 * 1. App 挂全局 sonner Toaster，业务任意处 toast 可显示；
 * 2. 实例启动/停止/重启/终止/删除操作给 toast 反馈；
 * 3. 文件写操作给 toast 反馈；
 * 4. 创建实例错误走 toast，不依赖旧内联 error div。
 */

beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
})

function InstanceActionToastHarness() {
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()
  const del = useDeleteInstance()
  return (
    <div>
      <button type="button" onClick={() => start.mutate(1)}>启动实例</button>
      <button type="button" onClick={() => stop.mutate(1)}>停止实例</button>
      <button type="button" onClick={() => restart.mutate(1)}>重启实例</button>
      <button type="button" onClick={() => kill.mutate(1)}>终止实例</button>
      <button type="button" onClick={() => del.mutate(1)}>删除实例</button>
      <Toaster />
    </div>
  )
}

function FileToastHarness() {
  return (
    <div>
      <ResourceExplorer instanceId={1} />
      <Toaster />
    </div>
  )
}

function CreateInstanceToastHarness() {
  const [open, setOpen] = useState(true)
  return (
    <div>
      <CreateInstanceDialog open={open} onClose={() => setOpen(false)} />
      <Toaster />
    </div>
  )
}

describe('FR-030 前端通知系统与 UX 标准化', () => {
  it('App 挂载全局 Toaster 后可显示业务 toast', async () => {
    renderWithProviders(<App />, { route: '/login' })

    toast.success('FR-030 全局通知可见')

    expect(await screen.findByText('FR-030 全局通知可见')).toBeInTheDocument()
  })

  it('实例启动/停止/重启/终止/删除操作显示 Toast 反馈', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<InstanceActionToastHarness />)

    await user.click(screen.getByRole('button', { name: '启动实例' }))
    expect(await screen.findByText('实例启动中…')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '停止实例' }))
    expect(await screen.findByText('实例已停止')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重启实例' }))
    expect(await screen.findByText('实例重启中…')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '终止实例' }))
    expect(await screen.findByText('实例已强制终止')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '删除实例' }))
    expect(await screen.findByText('实例已删除')).toBeInTheDocument()
  })

  it('文件新建操作显示 Toast 反馈', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<FileToastHarness />)

    const seed = await screen.findByText('server.properties')
    expect(seed).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '新建' }))
    await user.click(await screen.findByText('新建文件'))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'fr030-toast.txt')
    const confirm = within(dialog).getByRole('button', { name: '确认' })
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    expect(await screen.findByText('创建成功')).toBeInTheDocument()
    expect(await screen.findByText('fr030-toast.txt')).toBeInTheDocument()
  })

  it('创建实例错误显示 Toast，且对话框不误关闭', async () => {
    const user = userEvent.setup()
    loginMockUser()
    mockInject('post', '/instances', { kind: 'status', status: 500 })
    renderWithProviders(<CreateInstanceToastHarness />)

    await screen.findByText('创建实例')
    await user.type(screen.getByPlaceholderText('Survival Server'), 'fr030-create-fail')
    await user.click(screen.getByText('选择节点'))
    await user.click(await screen.findByRole('button', { name: 'alpha' }))
    await user.type(screen.getByPlaceholderText('java -Xmx2G -jar paper.jar nogui'), 'java -jar paper.jar nogui')
    await user.click(screen.getByRole('button', { name: '创建' }))

    expect(await screen.findByText('注入的模拟错误')).toBeInTheDocument()
    expect(screen.getByText('创建实例')).toBeInTheDocument()
  })
})
