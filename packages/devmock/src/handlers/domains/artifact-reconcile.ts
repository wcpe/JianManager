import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'
import type {
  ArtifactReconcileDiff,
  ArtifactReconcileRun,
  ArtifactReconcileSettings,
} from '@jianmanager/devmock/contracts'

const NOW = '2026-07-18T08:00:00Z'

const settings = db<ArtifactReconcileSettings & { id: number }>('artifact-reconcile-settings', () => [
  { id: 1, enabled: true, intervalHours: 24, nextRunAt: '2026-07-19T08:00:00Z' },
])

const runs = db<ArtifactReconcileRun>('artifact-reconcile-runs', () => [
  {
    id: 1,
    channelId: 2,
    channelName: 'rustfs-主渠道',
    status: 'succeeded',
    triggeredBy: 'manual',
    startedAt: '2026-07-18T07:59:00Z',
    finishedAt: NOW,
    indexCount: 3,
    objectCount: 3,
    matchedCount: 1,
    missingCount: 1,
    orphanCount: 1,
    errorMessage: '',
  },
])

const diffs = db<ArtifactReconcileDiff>('artifact-reconcile-diffs', () => [
  {
    id: 1,
    runId: 1,
    channelId: 2,
    kind: 'missing',
    assetId: 4,
    sha256: '9'.repeat(64),
    objectKey: `var/artifacts/client-file/99/${'9'.repeat(64)}.zip`,
    size: 4096,
    status: 'open',
    resolvedAction: '',
    resolveError: '',
  },
  {
    id: 2,
    runId: 1,
    channelId: 2,
    kind: 'orphan',
    assetId: 0,
    sha256: '',
    objectKey: 'var/artifacts/client-file/ff/orphan-pack.zip',
    size: 8192,
    lastModified: '2026-07-17T12:00:00Z',
    status: 'open',
    resolvedAction: '',
    resolveError: '',
  },
])

export const handlers = [
  domainRoute('get', '/artifact-reconcile/settings', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const current = settings.get(1)
    return HttpResponse.json({
      enabled: current?.enabled ?? true,
      intervalHours: current?.intervalHours ?? 24,
      nextRunAt: current?.nextRunAt,
    })
  }),

  domainRoute('put', '/artifact-reconcile/settings', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as ArtifactReconcileSettings
    if (body.intervalHours < 1 || body.intervalHours > 720) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '定期对账周期须在 1~720 小时之间' }, { status: 422 })
    }
    const nextRunAt = body.enabled ? new Date(Date.now() + body.intervalHours * 3_600_000).toISOString() : undefined
    settings.update(1, { enabled: body.enabled, intervalHours: body.intervalHours, nextRunAt })
    return HttpResponse.json({ enabled: body.enabled, intervalHours: body.intervalHours, nextRunAt })
  }),

  domainRoute('get', '/artifact-reconcile/runs', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const limit = Number(new URL(info.request.url).searchParams.get('limit') || 20)
    return HttpResponse.json(runs.list().sort((a, b) => b.id - a.id).slice(0, limit))
  }),

  domainRoute('get', '/artifact-reconcile/runs/:id', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const run = runs.get(Number(info.params.id))
    return run
      ? HttpResponse.json(run)
      : HttpResponse.json({ error: 'NOT_FOUND', message: '对账运行记录不存在' }, { status: 404 })
  }),

  domainRoute('post', '/artifact-reconcile/runs', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as { channelId?: number }
    const channelId = body.channelId || 2
    const created = runs.insert({
      channelId,
      channelName: channelId === 2 ? 'rustfs-主渠道' : `渠道#${channelId}`,
      status: 'succeeded',
      triggeredBy: 'manual',
      startedAt: new Date().toISOString(),
      finishedAt: new Date().toISOString(),
      indexCount: 2,
      objectCount: 2,
      matchedCount: 2,
      missingCount: 0,
      orphanCount: 0,
      errorMessage: '',
    })
    return HttpResponse.json({ started: [created], skipped: [] }, { status: 202 })
  }),

  domainRoute('get', '/artifact-reconcile/runs/:id/diffs', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const runId = Number(info.params.id)
    if (!runs.get(runId)) return HttpResponse.json({ error: 'NOT_FOUND' }, { status: 404 })
    const url = new URL(info.request.url)
    const kind = url.searchParams.get('kind')
    const page = Math.max(1, Number(url.searchParams.get('page') || 1))
    const pageSize = Math.min(200, Math.max(1, Number(url.searchParams.get('pageSize') || 50)))
    const all = diffs.list((item) => item.runId === runId && (!kind || item.kind === kind))
    return HttpResponse.json({
      items: all.slice((page - 1) * pageSize, page * pageSize),
      total: all.length,
      page,
      pageSize,
    })
  }),

  domainRoute('post', '/artifact-reconcile/runs/:id/resolve-missing', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const runId = Number(info.params.id)
    let marked = 0
    for (const item of diffs.list((diff) => diff.runId === runId && diff.kind === 'missing' && diff.status === 'open')) {
      diffs.update(item.id, { status: 'resolved', resolvedAt: new Date().toISOString(), resolvedAction: 'marked_lost' })
      marked += 1
    }
    return HttpResponse.json({ marked, stale: 0 })
  }),

  domainRoute('post', '/artifact-reconcile/runs/:id/cleanup-orphans', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const runId = Number(info.params.id)
    let cleaned = 0
    for (const item of diffs.list((diff) => diff.runId === runId && diff.kind === 'orphan' && diff.status === 'open')) {
      diffs.update(item.id, { status: 'resolved', resolvedAt: new Date().toISOString(), resolvedAction: 'cleaned' })
      cleaned += 1
    }
    return HttpResponse.json({ cleaned, stale: 0, failed: 0 })
  }),
]
