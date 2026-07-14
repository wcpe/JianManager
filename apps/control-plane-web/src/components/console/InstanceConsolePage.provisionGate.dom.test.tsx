import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { db } from '@jianmanager/devmock/db'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-331：长操作在途实例硬性禁止启动——实例控制台侧（FR-323 扩展导入/克隆）。
 * provision/import/clone 任务未终态期间实例 STOPPED + statusReason「搭建中/导入中/克隆中：…」，
 * 启动按钮直接禁用（tooltip 引导看任务中心），横幅走琥珀状态样式
 * 而非红色「上次启动失败」（是进行时不是失败）；reason 清空后自然解禁。
 */
describe('InstanceConsolePage 长操作在途禁启（FR-331）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('搭建中：启动按钮禁用 + tooltip 引导任务中心 + 琥珀横幅（非失败横幅）', async () => {
    db<MockInstance>('instances').update(3, {
      status: 'STOPPED',
      statusReason: '搭建中：正在下载核心（完成前请勿启动）',
    })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const startBtn = await screen.findByRole('button', { name: '启动' })
    expect(startBtn).toBeDisabled()
    expect(startBtn.closest('span')).toHaveAttribute('title', expect.stringContaining('任务中心'))

    // 琥珀状态横幅：标题 + reason 全文 + 去任务中心链接；不再显示红色「上次启动失败」。
    const banner = await screen.findByRole('status')
    expect(banner).toHaveTextContent('实例任务进行中')
    expect(banner).toHaveTextContent('搭建中：正在下载核心（完成前请勿启动）')
    expect(screen.getByRole('link', { name: '去任务中心查看进度' })).toHaveAttribute('href', '/tasks')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('导入中（FR-323 搬迁在途）：同样禁启 + 琥珀横幅带 reason 全文', async () => {
    db<MockInstance>('instances').update(3, {
      status: 'STOPPED',
      statusReason: '导入中：正在搬迁目录（完成前请勿启动）',
    })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const startBtn = await screen.findByRole('button', { name: '启动' })
    expect(startBtn).toBeDisabled()
    const banner = await screen.findByRole('status')
    expect(banner).toHaveTextContent('导入中：正在搬迁目录（完成前请勿启动）')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('reason 清空（任务终态）后启动按钮解禁、横幅消失', async () => {
    db<MockInstance>('instances').update(3, { status: 'STOPPED', statusReason: '' })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const startBtn = await screen.findByRole('button', { name: '启动' })
    expect(startBtn).toBeEnabled()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
