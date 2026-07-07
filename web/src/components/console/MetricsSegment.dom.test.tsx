import { describe, expect, it, beforeAll, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import MetricsSegment from './MetricsSegment'

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** FR-114：探针更新卡展示离线依赖缓存状态，方便验收缓存是否随探针内嵌。 */
describe('MetricsSegment 探针离线依赖缓存状态', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('展示 ServerProbe 离线依赖缓存大小与短指纹', async () => {
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('ServerProbe 探针更新')).toBeInTheDocument()
    expect(screen.getByText('探针已连接')).toBeInTheDocument()
    expect(screen.getByText(/内嵌版本: 0\.1\.0/)).toBeInTheDocument()
    expect(screen.getByText(/离线依赖缓存: 已内嵌 5\.4 MiB · 20894081/)).toBeInTheDocument()
  })
})
