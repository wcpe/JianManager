import { beforeEach, describe, expect, it, vi } from 'vitest'

// mock api/client：仅验证 429 重试编排，不触网。
vi.mock('@/api/client', () => ({
  default: { get: vi.fn() },
}))

import api from '@/api/client'
import { getWithRateLimitRetry } from './logFederation'

const mk429 = () => ({ response: { status: 429 } })
const mk500 = () => ({ response: { status: 500 } })

describe('getWithRateLimitRetry', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset()
  })

  it('偶发 429 经短退避重试后成功（压测：狂点/多标签页下的瞬时限流）', async () => {
    vi.mocked(api.get)
      .mockRejectedValueOnce(mk429())
      .mockRejectedValueOnce(mk429())
      .mockResolvedValueOnce({ data: { ok: true } })
    const res = await getWithRateLimitRetry<{ ok: boolean }>('/logs/federation', { params: {} }, [1, 1])
    expect(res.data.ok).toBe(true)
    expect(vi.mocked(api.get)).toHaveBeenCalledTimes(3)
  })

  it('持续 429 超过重试上限后抛出，由上层呈现失败态（不无限重试）', async () => {
    vi.mocked(api.get).mockRejectedValue(mk429())
    await expect(
      getWithRateLimitRetry('/logs/federation', { params: {} }, [1, 1]),
    ).rejects.toMatchObject({ response: { status: 429 } })
    expect(vi.mocked(api.get)).toHaveBeenCalledTimes(3) // 首次 + 2 次重试
  })

  it('非 429 错误立即抛出，不做重试（如 500/404 的既定降级路径不受影响）', async () => {
    vi.mocked(api.get).mockRejectedValue(mk500())
    await expect(getWithRateLimitRetry('/logs/federation', { params: {} })).rejects.toMatchObject({
      response: { status: 500 },
    })
    expect(vi.mocked(api.get)).toHaveBeenCalledTimes(1)
  })
})
