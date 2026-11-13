import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import { InstanceRowView } from './InstanceRowView'

const instance: InstanceInfo = {
  id: 42, uuid: 'row-lifecycle', nodeId: 1, name: 'survival-row',
  type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'STOPPED',
  startCommand: 'java -jar server.jar', workDir: '/srv/survival-row', serverPort: 25565,
  autoStart: false, autoRestart: false, tags: null, createdAt: '2026-01-01T00:00:00Z',
}

function renderRow(overrides: Partial<Parameters<typeof InstanceRowView>[0]> = {}) {
  return renderWithProviders(
    <table><tbody><InstanceRowView
      inst={instance} nodeName="host-a" selected={false} menu={null}
      onToggleSelect={vi.fn()} onStart={vi.fn()} onStop={vi.fn()} onKill={vi.fn()}
      {...overrides}
    /></tbody></table>,
  )
}

describe('InstanceRowView 生命周期状态', () => {
  it('与卡片一样在 HTTP 提交中解释进行中，并禁重复启动', () => {
    renderRow({ starting: true })
    expect(screen.getByText('启动请求提交中')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启动' })).toBeDisabled()
  })

  it('显示 STOPPING 的节点确认阶段与完整待确认原因，不标成失败', () => {
    const statusReason = '操作结果待确认，状态查询/心跳收敛: rpc canceled'
    renderRow({ inst: { ...instance, status: 'STOPPING', statusReason } })
    expect(screen.getByText('操作结果待确认')).toBeInTheDocument()
    expect(screen.getByTestId('instance-lifecycle-detail')).toHaveClass('text-status-warning')
    expect(screen.getByTestId('instance-lifecycle-status')).toHaveAttribute('title', expect.stringContaining(statusReason))
    expect(screen.getByTestId('instance-lifecycle-detail')).toHaveClass('truncate')
  })

  it('确定失败提供红色原因；状态列裁剪不丢完整原文', () => {
    const statusReason = '启动失败：上一代进程未在预算内退出'
    renderRow({ inst: { ...instance, status: 'CRASHED', statusReason } })
    expect(screen.getByText('启动失败')).toBeInTheDocument()
    expect(screen.getByTestId('instance-lifecycle-detail')).toHaveClass('text-status-danger')
    expect(screen.getByTestId('instance-lifecycle-status')).toHaveAttribute('title', expect.stringContaining(statusReason))
  })
})
