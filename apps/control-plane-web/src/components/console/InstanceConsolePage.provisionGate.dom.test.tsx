import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { db } from '@jianmanager/devmock/db'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-331：搭建中实例硬性禁止启动——实例控制台侧。
 * provision 任务未终态期间实例 STOPPED + statusReason「搭建中：…」（FR-319 二轮③），
 * 启动按钮直接禁用（tooltip 指明搭建中 + 引导看任务中心），搭建中横幅走琥珀状态样式
 * 而非红色「上次启动失败」（是进行时不是失败）；reason 清空后自然解禁。
 */
describe('InstanceConsolePage 搭建中禁启（FR-331）', () => {
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
    expect(banner).toHaveTextContent('实例搭建中')
    expect(banner).toHaveTextContent('搭建中：正在下载核心（完成前请勿启动）')
    expect(screen.getByRole('link', { name: '去任务中心查看进度' })).toHaveAttribute('href', '/tasks')
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
