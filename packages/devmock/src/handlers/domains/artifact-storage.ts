import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { requireAuth } from '@jianmanager/devmock/auth-middleware'
import type {
  ArtifactMigrationFailure,
  ArtifactMigrationInfo,
  ArtifactMigrationStatus,
  ArtifactStorageChannel,
  SaveArtifactStorageBody,
  Task,
} from '@jianmanager/devmock/contracts'

/**
 * 制品存储渠道域 mock handler（FR-347，见 ADR-073）。
 * 覆盖 web/src/api/artifactStorages.ts 的全部 endpoint：列表 / 创建 / 编辑 / 删除守卫
 * （内置不可删不可编辑、活跃不可删）/ 设活跃（先清后设恰一条）/ 候选与已存连通测试。
 * seed 两条：内置 local 活跃 + s3 示例（spec §3.7）。
 */

const artifactStorages = db<ArtifactStorageChannel>('artifactStorages', () => [
  {
    id: 1,
    name: '本机存储',
    type: 'local',
    endpoint: '',
    bucket: '',
    region: '',
    prefix: '',
    useSsl: false,
    presignTtlSeconds: 600,
    active: true,
    builtin: true,
    hasAccessKey: false,
    hasSecretKey: false,
    lastTestAt: undefined,
    lastTestOk: false,
    lastTestMessage: '',
    createdAt: '2026-07-01T08:00:00Z',
    updatedAt: '2026-07-01T08:00:00Z',
  },
  {
    id: 2,
    name: 'rustfs-主渠道',
    type: 's3',
    endpoint: 'rustfs.lan:9000',
    bucket: 'jm-artifacts',
    region: 'us-east-1',
    prefix: 'jm',
    useSsl: false,
    presignTtlSeconds: 600,
    active: false,
    builtin: false,
    hasAccessKey: true,
    hasSecretKey: true,
    lastTestAt: '2026-07-15T00:00:00Z',
    lastTestOk: true,
    lastTestMessage: '连接正常',
    createdAt: '2026-07-10T08:00:00Z',
    updatedAt: '2026-07-10T08:00:00Z',
  },
])

/** 列表序（与后端 List 对齐）：内置最前，其余按 id 升序。 */
function sortedChannels(): ArtifactStorageChannel[] {
  return [...artifactStorages.list()].sort((a, b) =>
    a.builtin === b.builtin ? a.id - b.id : a.builtin ? -1 : 1,
  )
}

// ───────────────────────── 存量迁移（FR-348）─────────────────────────

const migrationTasks = db<Task>('artifactMigrationTasks', () => [])
const migrations = db<ArtifactMigrationInfo & { id: number }>('artifactMigrations', () => [])
const migrationFailures = db<ArtifactMigrationFailure>('artifactMigrationFailures', () => [])

const TERMINAL = new Set(['succeeded', 'failed', 'canceled'])

/** 最近一次迁移任务（id 最大者）。 */
function latestMigrationTask(): Task | undefined {
  return [...migrationTasks.list()].sort((a, b) => b.id - a.id)[0]
}

/**
 * 迁移进度模拟：每次 GET /artifact-storages/migration 轮询把 running 任务推进一条
 * （已迁 +1），迁完 eligible（total - skipped）后落 succeeded——预览模式下进度卡两拍走完。
 */
function advanceMigration(task: Task): void {
  if (task.state !== 'running') return
  const info = migrations.find((m) => m.taskId === task.taskId)
  if (!info) return
  const eligible = info.total - info.skipped
  const migrated = Math.min(info.migrated + 1, eligible)
  migrations.update(info.id, { migrated })
  if (migrated >= eligible) {
    migrationTasks.update(task.id, {
      state: 'succeeded',
      progress: 100,
      result: JSON.stringify({ total: info.total, migrated, failed: info.failed, skipped: info.skipped }),
      updatedAt: new Date().toISOString(),
    })
  } else {
    migrationTasks.update(task.id, {
      progress: eligible > 0 ? Math.round((migrated * 100) / eligible) : 100,
      detail: `共 ${info.total} · 已迁 ${migrated} · 失败 ${info.failed} · 跳过 ${info.skipped}`,
      updatedAt: new Date().toISOString(),
    })
  }
}

export const handlers = [
  domainRoute('get', '/artifact-storages', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(sortedChannels())
  }),

  domainRoute('post', '/artifact-storages', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as SaveArtifactStorageBody
    if (body.type !== 's3') {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `非法的制品存储渠道类型: "${body.type}"（面板仅可创建 s3 渠道）` },
        { status: 422 },
      )
    }
    if (!body.endpoint || !body.bucket) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: '制品存储渠道配置非法: endpoint/bucket 不能为空' },
        { status: 422 },
      )
    }
    if (artifactStorages.find((c) => c.name === body.name)) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `制品存储渠道名称已存在: "${body.name}"` },
        { status: 422 },
      )
    }
    const ttl = body.presignTtlSeconds ?? 600
    if (ttl < 60 || ttl > 3600) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: '制品存储渠道配置非法: 预签名有效期须在 60~3600 秒之间' },
        { status: 422 },
      )
    }
    const now = new Date().toISOString()
    const created = artifactStorages.insert({
      name: body.name,
      type: 's3',
      endpoint: body.endpoint ?? '',
      bucket: body.bucket ?? '',
      region: body.region || 'us-east-1',
      prefix: body.prefix ?? '',
      useSsl: body.useSsl ?? false,
      presignTtlSeconds: ttl,
      active: false,
      builtin: false,
      hasAccessKey: !!body.accessKey,
      hasSecretKey: !!body.secretKey,
      lastTestOk: false,
      lastTestMessage: '',
      createdAt: now,
      updatedAt: now,
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/artifact-storages/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = artifactStorages.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品存储渠道不存在' }, { status: 404 })
    if (existing.builtin) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '内置本机存储渠道不可编辑或删除' }, { status: 422 })
    }
    const body = (await info.request.json()) as SaveArtifactStorageBody
    if (body.type && body.type !== existing.type) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `制品存储渠道类型不可修改: ${existing.type} → ${body.type}` },
        { status: 422 },
      )
    }
    if (artifactStorages.find((c) => c.name === body.name && c.id !== id)) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `制品存储渠道名称已存在: "${body.name}"` },
        { status: 422 },
      )
    }
    // 凭证留空 = 保留（脱敏编辑语义）；配置已变清 lastTest*。
    const updated = artifactStorages.update(id, {
      name: body.name,
      endpoint: body.endpoint ?? '',
      bucket: body.bucket ?? '',
      region: body.region || 'us-east-1',
      prefix: body.prefix ?? '',
      useSsl: body.useSsl ?? false,
      presignTtlSeconds: body.presignTtlSeconds ?? 600,
      hasAccessKey: existing.hasAccessKey || !!body.accessKey,
      hasSecretKey: existing.hasSecretKey || !!body.secretKey,
      lastTestAt: undefined,
      lastTestOk: false,
      lastTestMessage: '',
      updatedAt: new Date().toISOString(),
    })
    return HttpResponse.json(updated)
  }),

  domainRoute('delete', '/artifact-storages/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = artifactStorages.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品存储渠道不存在' }, { status: 404 })
    if (existing.builtin) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '内置本机存储渠道不可编辑或删除' }, { status: 422 })
    }
    if (existing.active) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: '活跃渠道禁止删除，请先切换活跃渠道' },
        { status: 422 },
      )
    }
    // seed 的 s3 渠道模拟被制品引用（删除守卫演示，DOM 测试断言此路径）。
    if (id === 2) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: '制品存储渠道被制品引用，无法删除: 当前被 3 个制品引用' },
        { status: 422 },
      )
    }
    artifactStorages.remove(id)
    return HttpResponse.json({ message: '已删除' })
  }),

  domainRoute('post', '/artifact-storages/test', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    await info.request.json().catch(() => ({}))
    return HttpResponse.json({ ok: true, message: '连接正常', latencyMs: 12 })
  }),

  domainRoute('post', '/artifact-storages/:id/test', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = artifactStorages.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品存储渠道不存在' }, { status: 404 })
    const updated = artifactStorages.update(id, {
      lastTestAt: new Date().toISOString(),
      lastTestOk: true,
      lastTestMessage: existing.type === 'local' ? '本机存储可用' : '连接正常',
    })
    return HttpResponse.json({ ok: true, message: updated?.lastTestMessage ?? '连接正常', latencyMs: 12 })
  }),

  domainRoute('post', '/artifact-storages/:id/activate', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = artifactStorages.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品存储渠道不存在' }, { status: 404 })
    // 先清后设：全表恰一条活跃（与后端事务语义对齐）。
    for (const ch of artifactStorages.list()) {
      if (ch.active && ch.id !== id) artifactStorages.update(ch.id, { active: false })
    }
    const updated = artifactStorages.update(id, { active: true })
    return HttpResponse.json(updated)
  }),

  // ===== 存量迁移（FR-348）：发起 / 状态轮询（含进度模拟推进）/ 失败明细 =====

  domainRoute('post', '/artifact-storages/:id/migrate', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const target = artifactStorages.get(id)
    if (!target) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品存储渠道不存在' }, { status: 404 })
    // endpoint 含 unreachable 时模拟目标真连探测失败，覆盖 422 契约演示。
    if (target.type === 's3' && target.endpoint.includes('unreachable')) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `目标渠道连接失败：${target.endpoint} 不可达` },
        { status: 422 },
      )
    }
    const latest = latestMigrationTask()
    if (latest && !TERMINAL.has(latest.state)) {
      return HttpResponse.json(
        { error: 'MIGRATION_IN_FLIGHT', message: '已有制品迁移任务在途，同一时间仅允许一个迁移任务' },
        { status: 409 },
      )
    }
    const now = new Date().toISOString()
    const taskId = `mig-${Date.now()}`
    const task = migrationTasks.insert({
      taskId,
      nodeId: 0,
      kind: 'artifact_migrate',
      state: 'running',
      progress: 0,
      title: `制品存量迁移 → ${target.name}`,
      detail: '排队中',
      error: '',
      result: '',
      cancelRequested: false,
      createdBy: 1,
      createdAt: now,
      updatedAt: now,
    })
    // seed 计数：共 3 · 待迁 2 · 跳过 1（GET 轮询逐拍推进，两拍走完）。
    migrations.insert({
      taskId: task.taskId,
      targetChannelId: id,
      targetName: target.name,
      total: 3,
      migrated: 0,
      failed: 0,
      skipped: 1,
    })
    return HttpResponse.json({ taskId: task.taskId }, { status: 202 })
  }),

  domainRoute('get', '/artifact-storages/migration', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const task = latestMigrationTask()
    if (!task) return HttpResponse.json({ task: null, migration: null })
    advanceMigration(task)
    const fresh = migrationTasks.find((t) => t.taskId === task.taskId) ?? task
    const info2 = migrations.find((m) => m.taskId === task.taskId)
    const migration = info2
      ? {
          taskId: info2.taskId,
          targetChannelId: info2.targetChannelId,
          targetName: artifactStorages.get(info2.targetChannelId)?.name ?? info2.targetName,
          total: info2.total,
          migrated: info2.migrated,
          failed: info2.failed,
          skipped: info2.skipped,
        }
      : null
    const status: ArtifactMigrationStatus = { task: fresh, migration }
    return HttpResponse.json(status)
  }),

  domainRoute('get', '/artifact-storages/migration/:taskId/failures', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const taskId = String(info.params.taskId)
    return HttpResponse.json(migrationFailures.list((f) => f.taskId === taskId))
  }),
]
