import { describe, it, expect } from 'vitest'
import { Route, Routes } from 'react-router'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import InstanceFilesPage from './InstanceFilesPage'

/**
 * FR-376 深链页：`/instances/:id/files?path=` 渲染资源管理器。
 * 需包一层 Routes 才能让 useParams 取到 id。
 */
describe('InstanceFilesPage（FR-376）', () => {
  it('带 path 查询渲染资源管理器与返回链', async () => {
    loginMockUser()
    renderWithProviders(
      <Routes>
        <Route path="/instances/:id/files" element={<InstanceFilesPage />} />
      </Routes>,
      { route: '/instances/1/files?path=plugins' },
    )

    expect(await screen.findByTestId('instance-files-page')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /返回控制台|Back to console/i })).toHaveAttribute(
      'href',
      '/instances/1?tab=resource',
    )
    expect(await screen.findByTestId('resource-explorer')).toBeInTheDocument()
  })

  /**
   * 实例不存在（404 / 已删除）时不能照常渲染整页。
   *
   * 原实现只把标题回退成 `#id` 就继续渲染：用户看到的是一个「看起来正常」的文件浏览器，
   * 点进去每一步都失败。`useInstance` 的 `isError` 必须被消费。
   */
  it('实例不存在时给出错误态，不渲染文件浏览器', async () => {
    loginMockUser()
    mockInject('get', '/instances/:id', { kind: 'status', status: 404 })
    renderWithProviders(
      <Routes>
        <Route path="/instances/:id/files" element={<InstanceFilesPage />} />
      </Routes>,
      { route: '/instances/1/files' },
    )

    expect(await screen.findByText('服务器不存在或正在加载')).toBeInTheDocument()
    expect(screen.queryByTestId('instance-files-page')).not.toBeInTheDocument()
    expect(screen.queryByTestId('resource-explorer')).not.toBeInTheDocument()
  })
})
