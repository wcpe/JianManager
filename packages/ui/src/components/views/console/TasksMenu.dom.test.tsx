import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { TasksMenu } from './TasksMenu'
import type { Task } from '@jianmanager/ui/lib/task-status'

/**
 * FR-327 页眉任务下拉 · 受控视图测（ADR-097 c 范式）。
 *
 * 应用侧 `ConsoleHeader.tasksMenu.dom.test.tsx` 走 mock 假后端验联动；
 * 这里补的是**入口形态与上报契约**：活跃任务时的计数/平均进度、空闲态、
 * 点条目带 taskId 深链、底部入口不带 id。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        header: {
          tasks: '任务', tasksRunning: '{{count}} 个任务进行中（平均 {{progress}}%）',
          tasksActiveCount: '进行中 {{count}}', viewAllTasks: '进入任务中心',
        },
        tasks: {
          empty: '暂无任务',
          state: { succeeded: '已完成', failed: '失败', canceled: '已取消', canceling: '取消中' },
          kind: { provision: '搭建服务器', jdkInstall: '安装 JDK' },
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function task(over: Partial<Task> = {}): Task {
  return {
    id: 1, taskId: 't-1', nodeId: 1, kind: 'provision', state: 'running', progress: 40,
    title: '搭建子服', detail: '拉取核心', error: '', result: '', cancelRequested: false,
    createdBy: 1, createdAt: '2025-01-01T00:00:00Z', updatedAt: '2025-01-01T00:00:00Z',
    ...over,
  }
}

function renderMenu(props: Partial<Parameters<typeof TasksMenu>[0]> = {}) {
  const merged = { tasks: undefined as Task[] | undefined, onOpenTask: vi.fn(), ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <TasksMenu {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('TasksMenu（FR-327 页眉任务下拉）', () => {
  it('有活跃任务时入口显示数量与平均进度；点条目带 taskId 深链上报。', async () => {
    const user = userEvent.setup()
    const props = renderMenu({
      tasks: [
        task({ progress: 40, title: '搭建子服' }),
        task({ id: 2, taskId: 't-2', progress: 60, title: '安装 JDK', kind: 'jdk_install' }),
      ],
    })

    // 入口形态：转圈 + 计数 2 + 平均 50%
    const trigger = screen.getByRole('button', { name: '任务' })
    expect(within(trigger).getByText('2')).toBeInTheDocument()
    expect(within(trigger).getByText('50%')).toBeInTheDocument()

    await user.click(trigger)
    await user.click(await screen.findByText('搭建子服'))
    expect(props.onOpenTask).toHaveBeenCalledWith('t-1')
  })

  it('空闲（全部终态）时入口为静态图标，底部入口不带 id。', async () => {
    const user = userEvent.setup()
    const props = renderMenu({ tasks: [task({ state: 'succeeded', progress: 100 })] })

    const trigger = screen.getByRole('button', { name: '任务' })
    expect(within(trigger).queryByText('%')).not.toBeInTheDocument()

    await user.click(trigger)
    await user.click(await screen.findByText('进入任务中心'))
    expect(props.onOpenTask).toHaveBeenCalledWith()
  })

  it('未加载或无任务时下拉显示空态。', async () => {
    const user = userEvent.setup()
    renderMenu({ tasks: undefined })
    await user.click(screen.getByRole('button', { name: '任务' }))
    expect(await screen.findByText('暂无任务')).toBeInTheDocument()
  })
})
