import { describe, it, expect } from 'vitest'
import { Route, Routes } from 'react-router'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
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
})
