import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/** FR-269：服务器统一控制台 mock-api 原型。 */
describe('InstanceConsolePage', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('渲染服务器状态条、固定分区和概览 KPI', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />)

    expect(await screen.findByText(/服务器控制台 \/ survival-1/)).toBeInTheDocument()
    expect(screen.getByText('RUNNING')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /打开终端/ })).toBeInTheDocument()

    for (const tab of ['概览', '控制台', '文件配置', '监控', '玩家', '插件', '备份定时', '业务', 'Bot']) {
      expect(screen.getByRole('button', { name: tab })).toBeInTheDocument()
    }

    expect(screen.getByText('CPU')).toBeInTheDocument()
    expect(screen.getByText('内存')).toBeInTheDocument()
    expect(screen.getAllByText('TPS').length).toBeGreaterThan(0)
    expect(screen.getByText('最近事件')).toBeInTheDocument()
    expect(screen.getByText('运行日志预览')).toBeInTheDocument()
  })
})
