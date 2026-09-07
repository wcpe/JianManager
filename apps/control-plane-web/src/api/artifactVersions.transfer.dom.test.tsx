import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// 用 hoisted mock 暴露 post spy，断言 ServerProbe jar 的传输类请求都显式放宽超时：
// 不带超时会被共享实例的 10s 默认值掐断——jar 数十 MB，传输未完浏览器就断连，
// 服务端落 context canceled（缓存）或直接丢失上传体（本地上传）。
const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }))
vi.mock('@/api/client', () => ({ default: { post: postMock, get: vi.fn(), put: vi.fn(), delete: vi.fn() } }))

import { useCacheServerProbeVersion, useUploadServerProbeVersion } from './artifactVersions'

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

function newClient() {
  return new QueryClient({ defaultOptions: { mutations: { retry: false } } })
}

describe('ServerProbe jar 传输请求超时', () => {
  beforeEach(() => {
    postMock.mockReset()
    postMock.mockResolvedValue({ data: { id: 1, assetId: 9 } })
  })

  it('缓存版本（CP 从 GitHub 拉取 jar）显式放宽超时，不被 10s 默认值掐断', async () => {
    const { result } = renderHook(() => useCacheServerProbeVersion(), { wrapper: wrapper(newClient()) })

    await result.current.mutateAsync(7)

    expect(postMock).toHaveBeenCalledTimes(1)
    const [url, , config] = postMock.mock.calls[0]
    expect(url).toBe('/artifact-packages/serverprobe/versions/7/cache')
    expect(config?.timeout).toBeGreaterThan(10_000)
  })

  it('本地上传 jar（上限 64MiB）显式放宽超时，不被 10s 默认值掐断', async () => {
    const { result } = renderHook(() => useUploadServerProbeVersion(), { wrapper: wrapper(newClient()) })
    const file = new File(['server-probe'], 'ServerProbe-0.3.0.jar', { type: 'application/java-archive' })

    await result.current.mutateAsync({ version: '0.3.0', file })

    expect(postMock).toHaveBeenCalledTimes(1)
    const [url, , config] = postMock.mock.calls[0]
    expect(url).toBe('/artifact-packages/serverprobe/versions/upload')
    expect(config?.timeout).toBeGreaterThan(10_000)
  })
})
