import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { RuntimeActiveTaskBar } from './RuntimeActiveTaskBar'
import type { Task } from '@/lib/tasks/task-status'

function task(over: Partial<Task> = {}): Task {
  return {
    id: 1,
    taskId: 'task-1',
    nodeId: 1,
    kind: 'jdk_install',
    state: 'running',
    progress: 45,
    title: '安装 JDK Temurin 21',
    detail: '节点 node-1 · arch=x64',
    error: '',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '2026-11-16T00:00:00Z',
    updatedAt: '2026-11-16T00:00:00Z',
    ...over,
  }
}

const noop = () => {}

describe('RuntimeActiveTaskBar', () => {
  it('没有在跑的任务时整条不渲染（不占高度）', () => {
    const { container } = render(
      <RuntimeActiveTaskBar tasks={[]} cancelingIds={[]} onForceStop={noop} />,
    )
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByTestId('runtime-active-task-bar')).toBeNull()
  })

  it('显示任务标题、来源类型与进度', () => {
    render(<RuntimeActiveTaskBar tasks={[task()]} cancelingIds={[]} onForceStop={noop} />)
    expect(screen.getByText('安装 JDK Temurin 21')).toBeInTheDocument()
    expect(screen.getByTestId('runtime-active-pct-task-1')).toHaveTextContent('45%')
  })

  it('排队中不显示 0%（pending 与 0% 是两回事）', () => {
    render(
      <RuntimeActiveTaskBar tasks={[task({ state: 'pending', progress: 0 })]} cancelingIds={[]} onForceStop={noop} />,
    )
    expect(screen.getByTestId('runtime-active-pct-task-1')).toHaveTextContent('排队中')
  })

  it('点「强制停止」把这一条任务报上去', async () => {
    const onForceStop = vi.fn()
    render(<RuntimeActiveTaskBar tasks={[task()]} cancelingIds={[]} onForceStop={onForceStop} />)
    await userEvent.click(screen.getByTestId('runtime-active-stop-task-1'))
    expect(onForceStop).toHaveBeenCalledTimes(1)
    expect(onForceStop.mock.calls[0]?.[0]).toMatchObject({ taskId: 'task-1' })
  })

  it('正在停止时按钮禁用且显示「停止中…」（防重复下发取消）', () => {
    render(<RuntimeActiveTaskBar tasks={[task()]} cancelingIds={['task-1']} onForceStop={noop} />)
    const btn = screen.getByTestId('runtime-active-stop-task-1')
    expect(btn).toBeDisabled()
    expect(btn).toHaveTextContent('停止中')
  })

  it('已进入取消中（等 Worker 确认）时不再给停止按钮', () => {
    render(
      <RuntimeActiveTaskBar
        tasks={[task({ cancelRequested: true })]}
        cancelingIds={[]}
        onForceStop={noop}
      />,
    )
    expect(screen.queryByTestId('runtime-active-stop-task-1')).toBeNull()
    expect(screen.getByTestId('runtime-active-pct-task-1')).toHaveTextContent('取消中')
  })

  it('多条任务各占一行，各自独立停止', async () => {
    const onForceStop = vi.fn()
    render(
      <RuntimeActiveTaskBar
        tasks={[
          task(),
          task({ id: 2, taskId: 'task-2', kind: 'pkg_install', title: '安装全局包 pm2@latest', progress: 10 }),
        ]}
        cancelingIds={[]}
        onForceStop={onForceStop}
      />,
    )
    expect(screen.getAllByTestId('runtime-active-task')).toHaveLength(2)
    await userEvent.click(screen.getByTestId('runtime-active-stop-task-2'))
    expect(onForceStop.mock.calls[0]?.[0]).toMatchObject({ taskId: 'task-2' })
  })
})
