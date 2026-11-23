import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'

/**
 * 配置交付（`/config-deliveries`）与审批（`/approvals`）两个域的 mock handler。
 *
 * ## 为什么两者放一起
 *
 * 交付单的状态机里 `pending_approval → approved` 这一段**由审批驱动**：交付单提交后要有人
 * 在审批台放行。把它们分到两个文件会让「提交→审批→放量」这条最需要被 e2e 覆盖的链路
 * 横跨两处、难以对照。同文件便于保持两边状态枚举一致。
 *
 * ## 字段保真
 *
 * 结构严格对齐 `configDeliveries.ts` / `approvals.ts` 的 TS interface：
 * 交付单时间字段是 RFC3339 字符串；`DeliveryTargetView` 的 `updatedAt` 同为 RFC3339。
 * 交付单刻意覆盖多个状态（draft / pending_approval / rolling / observing / paused / completed /
 * rolled_back / cancelled），让列表页的状态标签与筛选分支都有真实数据可渲染。
 */

type DeliveryStatus =
  | 'draft'
  | 'pending_approval'
  | 'approved'
  | 'rolling'
  | 'observing'
  | 'paused'
  | 'failed'
  | 'completed'
  | 'rolling_back'
  | 'rolled_back'
  | 'cancelled'

type DeliveryTargetStatus =
  | 'pending'
  | 'pushing'
  | 'pushed'
  | 'activating'
  | 'activated'
  | 'failed'
  | 'deferred'
  | 'skipped'

interface MockDeliveryOrder {
  id: number
  configFileId: number
  filePath: string
  targetLayerLevel: 'group' | 'instance'
  targetLayerScopeId: number
  targetLayerVersionId: number
  fromLayerVersionId?: number | null
  batchMode: 'percent' | 'count'
  observeWindowSec: number
  failureRateThreshold: number
  unhealthyRateThreshold: number
  activationMethod: 'hot_reload' | 'restart' | 'push_only'
  activateTimeoutSec: number
  reloadTimeoutSec: number
  autoRetryDeferred: boolean
  deferredRetryLimit: number
  status: DeliveryStatus
  pauseKind?: string
  pauseReason?: string
  breakReason?: string
  breakThreshold?: number
  breakObserved?: number
  plannedCount: number
  pushedCount: number
  activatedCount: number
  failedCount: number
  deferredCount: number
  skippedCount: number
  currentBatch: number
  totalBatches: number
  failedBatchNo?: number
  createdBy: number
  createdAt: string
  updatedAt: string
  startedAt?: string | null
  finishedAt?: string | null
}

interface MockDeliveryTarget {
  id: number
  orderId: number
  instanceId: number
  instanceName: string
  batchNo: number
  status: DeliveryTargetStatus
  failureReason?: string
  backupPath?: string
  updatedAt: string
}

interface MockApproval {
  id: number
  uuid: string
  applicantUserId: number
  applicantKind: string
  action: string
  targetType: string
  targetId: string
  payloadHash: string
  payloadFrozen: string
  reason: string
  status: 'pending' | 'withdrawn' | 'rejected' | 'expired' | 'executing' | 'succeeded' | 'failed'
  expiresAt: string
  failureReason?: string
  createdAt: string
  updatedAt: string
}

function rfc3339(offsetMinutes = 0): string {
  return new Date(Date.now() - offsetMinutes * 60_000).toISOString()
}

/** 交付单：覆盖 8 种状态，让状态筛选与徽标分支都有数据。 */
const deliveryOrders = db<MockDeliveryOrder>('config-delivery-orders', () => [
  {
    id: 1,
    configFileId: 1,
    filePath: 'server.properties',
    targetLayerLevel: 'group',
    targetLayerScopeId: 1,
    targetLayerVersionId: 1,
    fromLayerVersionId: null,
    batchMode: 'percent',
    observeWindowSec: 120,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 30,
    activationMethod: 'hot_reload',
    activateTimeoutSec: 60,
    reloadTimeoutSec: 60,
    autoRetryDeferred: true,
    deferredRetryLimit: 3,
    status: 'draft',
    plannedCount: 8,
    pushedCount: 0,
    activatedCount: 0,
    failedCount: 0,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 0,
    totalBatches: 3,
    createdBy: 1,
    createdAt: rfc3339(600),
    updatedAt: rfc3339(600),
  },
  {
    id: 2,
    configFileId: 1,
    filePath: 'server.properties',
    targetLayerLevel: 'instance',
    targetLayerScopeId: 321,
    targetLayerVersionId: 2,
    fromLayerVersionId: 1,
    batchMode: 'count',
    observeWindowSec: 60,
    failureRateThreshold: 10,
    unhealthyRateThreshold: 20,
    activationMethod: 'restart',
    activateTimeoutSec: 120,
    reloadTimeoutSec: 120,
    autoRetryDeferred: false,
    deferredRetryLimit: 0,
    status: 'pending_approval',
    plannedCount: 12,
    pushedCount: 0,
    activatedCount: 0,
    failedCount: 0,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 0,
    totalBatches: 2,
    createdBy: 1,
    createdAt: rfc3339(300),
    updatedAt: rfc3339(240),
  },
  {
    id: 3,
    configFileId: 2,
    filePath: 'bukkit.yml',
    targetLayerLevel: 'group',
    targetLayerScopeId: 1,
    targetLayerVersionId: 3,
    fromLayerVersionId: null,
    batchMode: 'percent',
    observeWindowSec: 180,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 25,
    activationMethod: 'hot_reload',
    activateTimeoutSec: 60,
    reloadTimeoutSec: 60,
    autoRetryDeferred: true,
    deferredRetryLimit: 3,
    status: 'rolling',
    plannedCount: 12,
    pushedCount: 6,
    activatedCount: 6,
    failedCount: 0,
    deferredCount: 1,
    skippedCount: 0,
    currentBatch: 2,
    totalBatches: 3,
    createdBy: 1,
    createdAt: rfc3339(180),
    updatedAt: rfc3339(10),
    startedAt: rfc3339(60),
  },
  {
    id: 4,
    configFileId: 1,
    filePath: 'server.properties',
    targetLayerLevel: 'group',
    targetLayerScopeId: 2,
    targetLayerVersionId: 4,
    fromLayerVersionId: null,
    batchMode: 'count',
    observeWindowSec: 120,
    failureRateThreshold: 15,
    unhealthyRateThreshold: 30,
    activationMethod: 'restart',
    activateTimeoutSec: 90,
    reloadTimeoutSec: 90,
    autoRetryDeferred: true,
    deferredRetryLimit: 2,
    // 熔断暂停：带出 breakReason / breakThreshold / breakObserved，让「熔断现场」展示分支有数据
    status: 'paused',
    pauseKind: 'circuit_break',
    pauseReason: '失败率超阈值，自动暂停',
    breakReason: 'failure_rate',
    breakThreshold: 15,
    breakObserved: 28,
    plannedCount: 10,
    pushedCount: 4,
    activatedCount: 3,
    failedCount: 1,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 1,
    totalBatches: 2,
    createdBy: 1,
    createdAt: rfc3339(900),
    updatedAt: rfc3339(400),
    startedAt: rfc3339(500),
  },
  {
    id: 5,
    configFileId: 3,
    filePath: 'plugins/AllinGame/config.json',
    targetLayerLevel: 'group',
    targetLayerScopeId: 1,
    targetLayerVersionId: 5,
    fromLayerVersionId: null,
    batchMode: 'percent',
    observeWindowSec: 60,
    failureRateThreshold: 0,
    unhealthyRateThreshold: 0,
    activationMethod: 'push_only',
    activateTimeoutSec: 30,
    reloadTimeoutSec: 30,
    autoRetryDeferred: false,
    deferredRetryLimit: 0,
    status: 'completed',
    plannedCount: 12,
    pushedCount: 12,
    activatedCount: 12,
    failedCount: 0,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 3,
    totalBatches: 3,
    createdBy: 1,
    createdAt: rfc3339(1440),
    updatedAt: rfc3339(1200),
    startedAt: rfc3339(1300),
    finishedAt: rfc3339(1200),
  },
  {
    id: 6,
    configFileId: 1,
    filePath: 'server.properties',
    targetLayerLevel: 'instance',
    targetLayerScopeId: 322,
    targetLayerVersionId: 6,
    fromLayerVersionId: 2,
    batchMode: 'count',
    observeWindowSec: 60,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 20,
    activationMethod: 'restart',
    activateTimeoutSec: 60,
    reloadTimeoutSec: 60,
    autoRetryDeferred: false,
    deferredRetryLimit: 0,
    status: 'rolled_back',
    plannedCount: 6,
    pushedCount: 6,
    activatedCount: 4,
    failedCount: 2,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 2,
    totalBatches: 2,
    createdBy: 1,
    createdAt: rfc3339(2000),
    updatedAt: rfc3339(1800),
    startedAt: rfc3339(1900),
    finishedAt: rfc3339(1800),
  },
  {
    id: 7,
    configFileId: 2,
    filePath: 'bukkit.yml',
    targetLayerLevel: 'group',
    targetLayerScopeId: 3,
    targetLayerVersionId: 7,
    fromLayerVersionId: null,
    batchMode: 'percent',
    observeWindowSec: 120,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 30,
    activationMethod: 'hot_reload',
    activateTimeoutSec: 60,
    reloadTimeoutSec: 60,
    autoRetryDeferred: true,
    deferredRetryLimit: 3,
    status: 'cancelled',
    plannedCount: 8,
    pushedCount: 1,
    activatedCount: 1,
    failedCount: 0,
    deferredCount: 0,
    skippedCount: 7,
    currentBatch: 1,
    totalBatches: 3,
    createdBy: 1,
    createdAt: rfc3339(2500),
    updatedAt: rfc3339(2300),
  },
  {
    id: 8,
    configFileId: 3,
    filePath: 'plugins/AllinGame/config.json',
    targetLayerLevel: 'group',
    targetLayerScopeId: 2,
    targetLayerVersionId: 8,
    fromLayerVersionId: null,
    batchMode: 'percent',
    observeWindowSec: 90,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 20,
    activationMethod: 'hot_reload',
    activateTimeoutSec: 60,
    reloadTimeoutSec: 60,
    autoRetryDeferred: true,
    deferredRetryLimit: 2,
    status: 'observing',
    plannedCount: 12,
    pushedCount: 12,
    activatedCount: 12,
    failedCount: 0,
    deferredCount: 0,
    skippedCount: 0,
    currentBatch: 3,
    totalBatches: 3,
    createdBy: 1,
    createdAt: rfc3339(120),
    updatedAt: rfc3339(5),
    startedAt: rfc3339(60),
  },
])

/** 逐目标进度：为每张单生成若干行，含 failed / deferred 以覆盖失败原因与备份标记展示。 */
const deliveryTargets = db<MockDeliveryTarget>('config-delivery-targets', () =>
  deliveryOrders.list().flatMap((order) =>
    Array.from({ length: Math.min(order.plannedCount, 6) }, (_, i) => {
      const instanceId = 321 + i
      const status: DeliveryTargetStatus =
        i < order.activatedCount
          ? 'activated'
          : i < order.activatedCount + order.failedCount
            ? 'failed'
            : i < order.activatedCount + order.failedCount + order.deferredCount
              ? 'deferred'
              : order.status === 'draft' || order.status === 'pending_approval'
                ? 'pending'
                : 'pushed'
      return {
        id: order.id * 100 + i,
        orderId: order.id,
        instanceId,
        instanceName: `game-${String(instanceId).padStart(2, '0')}`,
        batchNo: Math.min(order.totalBatches, Math.floor(i / 2) + 1),
        status,
        ...(status === 'failed' ? { failureReason: '激活超时：服务端在 60s 内未报告就绪' } : {}),
        ...(status === 'activated' ? { backupPath: `/backup/${instanceId}/server.properties.bak` } : {}),
        updatedAt: rfc3339(10 + i),
      }
    }),
  ),
)

/** 审批票：pending 用于审批台默认列表，另含 rejected / succeeded 覆盖状态筛选。 */
const approvals = db<MockApproval>('approvals', () => [
  {
    id: 1,
    uuid: 'apv-0001',
    applicantUserId: 1,
    applicantKind: 'user',
    action: 'config.delivery.submit',
    targetType: 'config_delivery_order',
    targetId: '2',
    payloadHash: 'sha256:aaaa1111',
    payloadFrozen: JSON.stringify({ orderId: 2, filePath: 'server.properties' }),
    reason: 'region1 紧急调高 spawn-protection，需放行',
    status: 'pending',
    expiresAt: rfc3339(-1440),
    createdAt: rfc3339(240),
    updatedAt: rfc3339(240),
  },
  {
    id: 2,
    uuid: 'apv-0002',
    applicantUserId: 1,
    applicantKind: 'user',
    action: 'instance.restart',
    targetType: 'instance',
    targetId: '322',
    payloadHash: 'sha256:bbbb2222',
    payloadFrozen: JSON.stringify({ instanceId: 322 }),
    reason: '实例内存泄漏，需重启回收',
    status: 'pending',
    expiresAt: rfc3339(-720),
    createdAt: rfc3339(120),
    updatedAt: rfc3339(120),
  },
  {
    id: 3,
    uuid: 'apv-0003',
    applicantUserId: 2,
    applicantKind: 'user',
    action: 'config.delivery.submit',
    targetType: 'config_delivery_order',
    targetId: '5',
    payloadHash: 'sha256:cccc3333',
    payloadFrozen: JSON.stringify({ orderId: 5 }),
    reason: '常规配置下发',
    status: 'succeeded',
    expiresAt: rfc3339(600),
    createdAt: rfc3339(1500),
    updatedAt: rfc3339(1400),
  },
  {
    id: 4,
    uuid: 'apv-0004',
    applicantUserId: 2,
    applicantKind: 'user',
    action: 'instance.stop',
    targetType: 'instance',
    targetId: '330',
    payloadHash: 'sha256:dddd4444',
    payloadFrozen: JSON.stringify({ instanceId: 330 }),
    reason: '例行维护',
    status: 'rejected',
    failureReason: '维护窗口未到，暂不放行',
    expiresAt: rfc3339(1200),
    createdAt: rfc3339(2000),
    updatedAt: rfc3339(1900),
  },
])

function nextOrderId(): number {
  return deliveryOrders.list().reduce((mx, o) => Math.max(mx, o.id), 0) + 1
}

export const handlers = [
  // ── 交付单列表（分页 + 状态筛选）───────────────────────────────────────────
  domainRoute('get', '/config-deliveries', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1))
    const pageSize = Math.max(1, Number(url.searchParams.get('pageSize') ?? 20))
    const status = url.searchParams.get('status') ?? ''
    const keyword = (url.searchParams.get('keyword') ?? '').toLowerCase()
    let all = deliveryOrders.list()
    // status 缺省 / 'all' 表示不过滤——与后端「前端无论传什么都表达不出全部」的语义一致
    if (status && status !== 'all') all = all.filter((o) => o.status === status)
    if (keyword) all = all.filter((o) => o.filePath.toLowerCase().includes(keyword))
    all = all.sort((a, b) => b.id - a.id)
    const start = (page - 1) * pageSize
    return HttpResponse.json({
      orders: all.slice(start, start + pageSize),
      total: all.length,
      page,
      pageSize,
    })
  }),

  domainRoute('get', '/config-deliveries/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const found = deliveryOrders.list((o) => o.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '交付单不存在' }, { status: 404 })
    return HttpResponse.json(found)
  }),

  domainRoute('get', '/config-deliveries/:id/targets', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const url = new URL(info.request.url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1))
    const pageSize = Math.max(1, Number(url.searchParams.get('pageSize') ?? 20))
    const all = deliveryTargets.list((t) => t.orderId === id)
    const start = (page - 1) * pageSize
    return HttpResponse.json({
      targets: all.slice(start, start + pageSize),
      total: all.length,
      page,
      pageSize,
    })
  }),

  // 影响预览：汇总 + 逐目标（前端用它对「这一批会改到谁」做确认）
  domainRoute('get', '/config-deliveries/:id/impact', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const targets = deliveryTargets.list((t) => t.orderId === id)
    return HttpResponse.json({
      orderId: id,
      summary: {
        total: targets.length,
        toPush: targets.filter((t) => t.status === 'pending' || t.status === 'pushed').length,
        alreadySame: targets.filter((t) => t.status === 'activated').length,
        unreachable: targets.filter((t) => t.status === 'deferred').length,
      },
      targets: targets.map((t) => ({
        instanceId: t.instanceId,
        instanceName: t.instanceName,
        diffCount: (t.instanceId % 3) + 1,
        hitScopes: ['group:1'],
      })),
    })
  }),

  // 观察窗序列：逐目标时间桶 + 健康分 + TPS + 告警数
  domainRoute('get', '/config-deliveries/:id/observe', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const targets = deliveryTargets.list((t) => t.orderId === id)
    return HttpResponse.json({
      orderId: id,
      targets: targets.map((t) => ({
        instanceId: t.instanceId,
        instanceName: t.instanceName,
        buckets: [
          { ts: Math.floor(Date.now() / 1000) - 120, health: 96, tps: 19.8, alerts: 0 },
          { ts: Math.floor(Date.now() / 1000) - 60, health: 92, tps: 19.5, alerts: 1 },
          { ts: Math.floor(Date.now() / 1000), health: 88, tps: 19.1, alerts: 2 },
        ],
      })),
    })
  }),

  domainRoute('post', '/config-deliveries', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as Partial<MockDeliveryOrder>
    const id = nextOrderId()
    const created: MockDeliveryOrder = {
      id,
      configFileId: body.configFileId ?? 1,
      filePath: body.filePath ?? 'server.properties',
      targetLayerLevel: body.targetLayerLevel ?? 'group',
      targetLayerScopeId: body.targetLayerScopeId ?? 1,
      targetLayerVersionId: body.targetLayerVersionId ?? 1,
      fromLayerVersionId: null,
      batchMode: body.batchMode ?? 'percent',
      observeWindowSec: body.observeWindowSec ?? 120,
      failureRateThreshold: body.failureRateThreshold ?? 20,
      unhealthyRateThreshold: body.unhealthyRateThreshold ?? 30,
      activationMethod: body.activationMethod ?? 'hot_reload',
      activateTimeoutSec: body.activateTimeoutSec ?? 60,
      reloadTimeoutSec: body.reloadTimeoutSec ?? 60,
      autoRetryDeferred: body.autoRetryDeferred ?? true,
      deferredRetryLimit: body.deferredRetryLimit ?? 3,
      status: 'draft',
      plannedCount: body.plannedCount ?? 0,
      pushedCount: 0,
      activatedCount: 0,
      failedCount: 0,
      deferredCount: 0,
      skippedCount: 0,
      currentBatch: 0,
      totalBatches: body.totalBatches ?? 1,
      createdBy: 1,
      createdAt: rfc3339(),
      updatedAt: rfc3339(),
    }
    deliveryOrders.insert(created)
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/config-deliveries/:id', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as Partial<MockDeliveryOrder>
    const found = deliveryOrders.list((o) => o.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '交付单不存在' }, { status: 404 })
    const updated = deliveryOrders.update(id, { ...body, id, updatedAt: rfc3339() })
    return HttpResponse.json(updated ?? found)
  }),

  domainRoute('delete', '/config-deliveries/:id', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    deliveryOrders.remove(id)
    deliveryTargets.list((t) => t.orderId === id).forEach((t) => deliveryTargets.remove(t.id))
    return new HttpResponse(null, { status: 204 })
  }),

  // ── 状态机动作：每个动作都返回更新后的整单，前端据此刷新 ──────────────────
  domainRoute('post', '/config-deliveries/:id/submit', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    return transition(Number((info.params as { id: string }).id), 'pending_approval')
  }),

  domainRoute('post', '/config-deliveries/:id/approve', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    return transition(Number((info.params as { id: string }).id), 'approved')
  }),

  domainRoute('post', '/config-deliveries/:id/start', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const found = deliveryOrders.list((o) => o.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '交付单不存在' }, { status: 404 })
    // 启动后进入放量中：同时把 startedAt 写上，界面「已开始」分支才正确
    const updated = deliveryOrders.update(id, {
      status: 'rolling',
      startedAt: rfc3339(),
      currentBatch: 1,
      updatedAt: rfc3339(),
    })
    return new HttpResponse(null, { status: 202 })
  }),

  domainRoute('post', '/config-deliveries/:id/pause', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json().catch(() => ({}))) as { reason?: string }
    return transition(id, 'paused', { pauseKind: 'manual', pauseReason: body.reason ?? '人工暂停' })
  }),

  domainRoute('post', '/config-deliveries/:id/resume', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    // 继续后回到放量中；清掉熔断现场字段，避免界面继续展示过期的暂停原因
    return transition(id, 'rolling', { pauseKind: undefined, pauseReason: undefined })
  }),

  domainRoute('post', '/config-deliveries/:id/cancel', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json().catch(() => ({}))) as { reason?: string }
    return transition(id, 'cancelled', { finishedAt: rfc3339(), pauseReason: body.reason })
  }),

  domainRoute('post', '/config-deliveries/:id/rollback', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json().catch(() => ({}))) as { reason?: string }
    const found = deliveryOrders.list((o) => o.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '交付单不存在' }, { status: 404 })
    const updated = deliveryOrders.update(id, { status: 'rolled_back', finishedAt: rfc3339(), updatedAt: rfc3339() })
    return HttpResponse.json({
      order: updated ?? found,
      rolledBackCount: found.activatedCount,
      reason: body.reason ?? '',
    })
  }),

  // ── 审批（/approvals）────────────────────────────────────────────────────
  //
  // status 语义：缺省 pending；'all' 表示不过滤。这是后端必须兑现的语义——
  // 若把「缺省 pending」与「不过滤」共用一个参数，前端就永远表达不出「全部」。
  domainRoute('get', '/approvals', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const status = url.searchParams.get('status') ?? 'pending'
    const limit = Math.max(1, Number(url.searchParams.get('limit') ?? 50))
    let all = approvals.list()
    if (status !== 'all') all = all.filter((a) => a.status === status)
    all = all.sort((a, b) => b.id - a.id).slice(0, limit)
    return HttpResponse.json({ items: all })
  }),

  domainRoute('get', '/approvals/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const found = approvals.list((a) => a.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '审批单不存在' }, { status: 404 })
    return HttpResponse.json(found)
  }),

  domainRoute('post', '/approvals/:id/approve', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const found = approvals.list((a) => a.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '审批单不存在' }, { status: 404 })
    // 放行后进入 executing（执行结果由下游任务回填），与后端状态机一致
    return HttpResponse.json(approvals.update(id, { status: 'executing', updatedAt: rfc3339() }) ?? found)
  }),

  domainRoute('post', '/approvals/:id/reject', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json().catch(() => ({}))) as { note?: string }
    const found = approvals.list((a) => a.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '审批单不存在' }, { status: 404 })
    return HttpResponse.json(
      approvals.update(id, { status: 'rejected', failureReason: body.note ?? '已驳回', updatedAt: rfc3339() }) ?? found,
    )
  }),
]

/**
 * 交付单状态迁移的共用实现。
 *
 * 找不到单子时返回 404（与后端一致），否则回写 status 与 patch 并返回整单。
 * 抽成函数是因为 7 个动作只有目标状态与附加字段不同，各写一遍必然漂移。
 */
function transition(
  id: number,
  status: DeliveryStatus,
  patch: Partial<MockDeliveryOrder> = {},
): Response {
  const found = deliveryOrders.list((o) => o.id === id)[0]
  if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '交付单不存在' }, { status: 404 })
  const updated = deliveryOrders.update(id, { ...patch, status, updatedAt: rfc3339() })
  return HttpResponse.json(updated ?? found)
}
