import { describe, it, expect, vi, beforeEach } from 'vitest'

// 用 hoisted mock 暴露 post spy，断言 dispatchBusiness 拼装的请求体（FR-121）。
const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }))
vi.mock('@/api/client', () => ({ default: { post: postMock, get: vi.fn() } }))

import { dispatchBusiness } from './business'

describe('dispatchBusiness 写动作横切参数（FR-121）', () => {
  beforeEach(() => {
    postMock.mockReset()
    postMock.mockResolvedValue({ data: { instanceId: 1, domain: 'economy', action: 'deposit', available: true, output: null } })
  })

  it('读动作（无 opts）请求体 write=false 且不带 operationId/reason', async () => {
    await dispatchBusiness(1, 'economy', 'balance', '{"player":"a"}')
    expect(postMock).toHaveBeenCalledTimes(1)
    const [, body] = postMock.mock.calls[0]
    expect(body).toMatchObject({ domain: 'economy', action: 'balance', payload: '{"player":"a"}', write: false })
    expect(body.operationId).toBeUndefined()
    expect(body.reason).toBeUndefined()
  })

  it('写动作携带 write=true + operationId + reason', async () => {
    await dispatchBusiness(2, 'economy', 'deposit', '{"player":"a","amount":"10"}', {
      write: true,
      operationId: 'op-uuid-1',
      reason: '活动补偿',
    })
    const [url, body] = postMock.mock.calls[0]
    expect(url).toBe('/instances/2/business')
    expect(body).toMatchObject({
      domain: 'economy',
      action: 'deposit',
      write: true,
      operationId: 'op-uuid-1',
      reason: '活动补偿',
    })
  })

  it('写动作 operationId 直传不被改写（幂等键稳定性）', async () => {
    const stable = 'stable-op-key'
    await dispatchBusiness(3, 'inventory', 'give', '{}', { write: true, operationId: stable })
    const [, body] = postMock.mock.calls[0]
    expect(body.operationId).toBe(stable)
  })
})
