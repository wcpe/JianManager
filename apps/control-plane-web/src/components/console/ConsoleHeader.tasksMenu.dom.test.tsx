import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { db } from '@jianmanager/devmock/db'
import ConsoleHeader from './ConsoleHeader'

/**
 * FR-327 页眉任务中心下拉面板：入口点击弹面板（最近 N 条：kind 徽标/名称/进行中进度/终态徽章），
 * 点条目跳任务中心 `?task=` 深链定位，底部「进入任务中心」看全量，面板外点击关闭。
 * 数据来自 devmock 种子任务（task-jdk-1 已完成 / task-backup-2 进行中 45%）。
 */

/** 清空假后端全部任务（空态用例）。 */
function removeAllTasks() {
  const tasks = db<{ id: number }>('tasks')
  tasks.list().forEach((t) => tasks.remove(t.id))
}

describe('ConsoleHeader 任务中心下拉面板（FR-327）', () => {
  it('点击入口弹面板：行 = kind 徽标 + 名称 + 进行中进度 / 终态徽章', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: '任务中心' }))
    const menu = await screen.findByRole('menu')

    // 终态任务：kind 文案徽标 + 终态徽章（已完成），不画进度条。
    const doneRow = await within(menu).findByRole('menuitem', { name: /安装 JDK Temurin 21/ })
    expect(doneRow).toHaveTextContent('JDK 安装')
    expect(doneRow).toHaveTextContent('已完成')
    expect(doneRow).not.toHaveTextContent('100%')

    // 进行中任务：进度百分比 + stage 详情（detail）。
    const runningRow = within(menu).getByRole('menuitem', { name: /备份实例 survival/ })
    expect(runningRow).toHaveTextContent('45%')
    expect(runningRow).toHaveTextContent('打包世界文件')
  })

  it('点条目 → 跳任务中心并 ?task= 深链定位该任务', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: '任务中心' }))
    const menu = await screen.findByRole('menu')

    await user.click(await within(menu).findByRole('menuitem', { name: /备份实例 survival/ }))
    expect(window.location.pathname).toBe('/tasks')
    expect(window.location.search).toBe('?task=task-backup-2')
  })

  it('底部「进入任务中心」→ /tasks；面板随选择关闭', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: '任务中心' }))
    const menu = await screen.findByRole('menu')

    await user.click(within(menu).getByRole('menuitem', { name: '进入任务中心' }))
    expect(window.location.pathname).toBe('/tasks')
    expect(window.location.search).toBe('')
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })

  it('面板外点击关闭（不导航）', async () => {
    loginMockUser()
    // Radix 打开面板期间给 body 置 pointer-events:none（阻隔外部交互），
    // 关闭 userEvent 的 pointer-events 校验以模拟「面板外点击」。
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: '任务中心' }))
    await screen.findByRole('menu')

    await user.click(document.body)
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
    expect(window.location.pathname).toBe('/instances')
  })

  it('有在跑任务时入口显示数量与平均进度；空任务时静态图标 + 面板空态', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { unmount } = renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    // 种子含 running 任务：入口内出现进行中数量（tabular-nums 计数）。
    const trigger = await screen.findByRole('button', { name: '任务中心' })
    await waitFor(() => expect(trigger).toHaveTextContent(/\d+/))
    unmount()

    // 清空任务：入口仍常驻（静态图标、无计数），面板显示空态文案。
    removeAllTasks()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })
    const idleTrigger = await screen.findByRole('button', { name: '任务中心' })
    await user.click(idleTrigger)
    const menu = await screen.findByRole('menu')
    await within(menu).findByText('暂无任务')
    expect(idleTrigger).not.toHaveTextContent(/\d/)
  })
})
