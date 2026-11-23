import { beforeEach, describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useLogsFederationFacets, useLogsFederationFields, useLogsFederationStats } from './logFederation'

beforeEach(() => loginMockUser())

function Probe({ operation, explore }: { operation: 'fields' | 'facets' | 'stats'; explore: boolean }) {
  const params = { viewId: 'fixed', explore, dimensions: 'trace_id', dimensionLimit: 20 }
  const fields = useLogsFederationFields(params, operation === 'fields')
  const facets = useLogsFederationFacets(params, operation === 'facets')
  const stats = useLogsFederationStats(params, operation === 'stats')
  const query = operation === 'fields' ? fields : operation === 'facets' ? facets : stats
  const status = (query.error as { response?: { status?: number } } | null)?.response?.status
  return <output>{query.status}:{status ?? ''}</output>
}

describe('字段探索执行确认', () => {
  it.each(['fields', 'facets'] as const)('旧CP忽略explore时%s不能冒充成功空结果', async (operation) => {
    server.use(http.get(API(`/logs/federation/${operation}`), () => HttpResponse.json({ fields: [], dimensions: [], coverage: { complete: true } })))
    renderWithProviders(<Probe operation={operation} explore />)
    expect(await screen.findByText('error:501')).toBeInTheDocument()
  })

  it.each(['fields', 'facets'] as const)('新CP明确ack后%s接受自身覆盖证据', async (operation) => {
    server.use(http.get(API(`/logs/federation/${operation}`), () => HttpResponse.json({ explore_applied: true, fields: [], dimensions: [], coverage: { complete: false, partial_reasons: ['BUDGET_EXCEEDED'] } })))
    renderWithProviders(<Probe operation={operation} explore />)
    expect(await screen.findByText('success:')).toBeInTheDocument()
  })

  it.each(['fields', 'facets', 'stats'] as const)('旧默认%s无需探索ack', async (operation) => {
    server.use(http.get(API(`/logs/federation/${operation}`), () => HttpResponse.json({ fields: [], dimensions: [], points: [], coverage: { complete: true } })))
    renderWithProviders(<Probe operation={operation} explore={false} />)
    expect(await screen.findByText('success:')).toBeInTheDocument()
  })
})
