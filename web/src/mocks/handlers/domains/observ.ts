import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'

/**
 * 可观测与日志域 mock handler（FR-208）：metrics / alerts / notifications / tasks / logs。
 * 照 spec §7 范式：domainRoute 注册本域每个 endpoint，受保护端点首行 requireAuth，
 * db('<集合>', seedFn) 声明并播种集合（字段贴合 web/src/api/{metrics,alerts,notifications,tasks,logs}.ts）。
 *
 * 不重定义地基端点：GET /instances/events（SSE）由 realtime/instance-events.ts 提供，本文件不碰。
 * 指标为派生数据（无独立集合），返回合理时序/总量让监控图表与卡片有内容。
 */

// ── 集合实体类型（与 web/src/api/*.ts 的 interface 对齐） ──

/** 告警规则（对齐 api/alerts.ts AlertRuleInfo）。channelIds 为字符串化 JSON 数组。 */
interface AlertRule {
  id: number
  uuid: string
  name: string
  triggerType: string
  level: string
  targetType: string
  targetId: number | null
  metric: string
  operator: string
  threshold: number
  durationSec: number
  keyword: string
  eventMatch: string
  channelIds: string
  dedupWindowSec: number
  silenceStart: string
  silenceEnd: string
  notifyRecover: boolean
  notifyType: string
  notifyTarget: string
  enabled: boolean
  createdAt: string
}

/** 告警事件（对齐 api/alerts.ts AlertEventInfo）。 */
interface AlertEvent {
  id: number
  ruleId: number
  targetId: number
  level: string
  triggerType: string
  value: number
  message: string
  count: number
  resolved: boolean
  firedAt: string
  lastFiredAt?: string
  resolvedAt?: string
  acknowledged: boolean
  acknowledgedBy?: number
  acknowledgedAt?: string
  read: boolean
  rule?: { name?: string }
}

/** 通知通道（对齐 api/alerts.ts AlertChannelInfo）。config 为字符串化 JSON。 */
interface AlertChannel {
  id: number
  uuid: string
  name: string
  type: string
  enabled: boolean
  config: string
  createdAt: string
}

/** 站内信（对齐 api/notifications.ts Notification）。 */
interface Notification {
  id: number
  userId: number
  level: 'info' | 'success' | 'warning' | 'error'
  title: string
  body: string
  taskId?: string
  readAt?: string
  createdAt: string
}

/** 长任务（对齐 api/tasks.ts Task）。 */
interface Task {
  id: number
  taskId: string
  nodeId: number
  kind: string
  state: 'pending' | 'running' | 'succeeded' | 'failed'
  progress: number
  title: string
  detail: string
  error: string
  result: string
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 任务滚动日志（对齐 api/tasks.ts TaskLog）。 */
interface TaskLog {
  id: number
  taskId: string
  seq: number
  line: string
  ts: string
}

/** 日志条目（对齐 api/logs.ts LogEntry）。 */
interface LogRow {
  id: number
  source: string
  level: string
  instanceId: number
  instanceUuid: string
  nodeId: number
  stream?: string
  message: string
  time: string
}

const NOW = Date.now()
const iso = (offsetMs: number): string => new Date(NOW + offsetMs).toISOString()

// ── 集合声明 + 种子（import 即播种，resetDb 重播；唯一声明处） ──

const alertRules = db<AlertRule>('alertRules', () => [
  {
    id: 1,
    uuid: 'rule-cpu',
    name: 'CPU 过载告警',
    triggerType: 'metric',
    level: 'warn',
    targetType: 'node',
    targetId: 1,
    metric: 'cpu',
    operator: '>',
    threshold: 85,
    durationSec: 300,
    keyword: '',
    eventMatch: '',
    channelIds: '[1]',
    dedupWindowSec: 600,
    silenceStart: '',
    silenceEnd: '',
    notifyRecover: true,
    notifyType: '',
    notifyTarget: '',
    enabled: true,
    createdAt: iso(-86400000),
  },
  {
    id: 2,
    uuid: 'rule-crash',
    name: '实例崩溃告警',
    triggerType: 'instance_crash',
    level: 'critical',
    targetType: 'instance',
    targetId: 1,
    metric: '',
    operator: '',
    threshold: 0,
    durationSec: 0,
    keyword: '',
    eventMatch: '',
    channelIds: '[1]',
    dedupWindowSec: 0,
    silenceStart: '',
    silenceEnd: '',
    notifyRecover: false,
    notifyType: '',
    notifyTarget: '',
    enabled: false,
    createdAt: iso(-172800000),
  },
])

const alertEvents = db<AlertEvent>('alertEvents', () => [
  {
    id: 1,
    ruleId: 1,
    targetId: 1,
    level: 'warn',
    triggerType: 'metric',
    value: 91.5,
    message: '节点 node-1 CPU 使用率 91.5% 超过阈值 85%',
    count: 3,
    resolved: false,
    firedAt: iso(-3600000),
    lastFiredAt: iso(-600000),
    acknowledged: false,
    read: false,
    rule: { name: 'CPU 过载告警' },
  },
  {
    id: 2,
    ruleId: 2,
    targetId: 1,
    level: 'critical',
    triggerType: 'instance_crash',
    value: 0,
    message: '实例 survival 异常退出（exit code 1）',
    count: 1,
    resolved: true,
    firedAt: iso(-7200000),
    resolvedAt: iso(-7000000),
    acknowledged: true,
    acknowledgedBy: 1,
    acknowledgedAt: iso(-6900000),
    read: true,
    rule: { name: '实例崩溃告警' },
  },
])

const alertChannels = db<AlertChannel>('alertChannels', () => [
  {
    id: 1,
    uuid: 'chan-webhook',
    name: '运维 Webhook',
    type: 'webhook',
    enabled: true,
    config: JSON.stringify({ url: 'https://hooks.example.com/ops' }),
    createdAt: iso(-259200000),
  },
  {
    id: 2,
    uuid: 'chan-inapp',
    name: '站内通知',
    type: 'inapp',
    enabled: true,
    config: JSON.stringify({}),
    createdAt: iso(-259200000),
  },
])

const notifications = db<Notification>('notifications', () => [
  {
    id: 1,
    userId: 1,
    level: 'success',
    title: 'JDK 安装完成',
    body: 'node-1 上 Temurin 21 安装成功',
    taskId: 'task-jdk-1',
    createdAt: iso(-1800000),
  },
  {
    id: 2,
    userId: 1,
    level: 'error',
    title: '备份失败',
    body: '实例 survival 备份失败：磁盘空间不足',
    createdAt: iso(-900000),
  },
  {
    id: 3,
    userId: 1,
    level: 'info',
    title: '节点已上线',
    body: 'node-2 已注册并上线',
    readAt: iso(-600000),
    createdAt: iso(-1200000),
  },
])

const tasks = db<Task>('tasks', () => [
  {
    id: 1,
    taskId: 'task-jdk-1',
    nodeId: 1,
    kind: 'jdk_install',
    state: 'succeeded',
    progress: 100,
    title: '安装 JDK Temurin 21',
    detail: 'node-1',
    error: '',
    result: JSON.stringify({ vendor: 'temurin', version: '21' }),
    createdBy: 1,
    createdAt: iso(-3600000),
    updatedAt: iso(-3000000),
  },
  {
    id: 2,
    taskId: 'task-backup-2',
    nodeId: 1,
    kind: 'instance_backup',
    state: 'running',
    progress: 45,
    title: '备份实例 survival',
    detail: '打包世界文件',
    error: '',
    result: '',
    createdBy: 1,
    createdAt: iso(-300000),
    updatedAt: iso(-30000),
  },
  {
    id: 3,
    taskId: 'task-runtime-3',
    nodeId: 2,
    kind: 'runtime_install',
    state: 'failed',
    progress: 70,
    title: '安装便携运行时',
    detail: 'node-2',
    error: '下载校验失败：sha256 不匹配',
    result: '',
    createdBy: 1,
    createdAt: iso(-7200000),
    updatedAt: iso(-7100000),
  },
])

const taskLogs = db<TaskLog>('taskLogs', () => [
  { id: 1, taskId: 'task-jdk-1', seq: 1, line: '[info] 开始下载 Temurin 21', ts: iso(-3600000) },
  { id: 2, taskId: 'task-jdk-1', seq: 2, line: '[info] 解压到 /opt/jdk/temurin-21', ts: iso(-3300000) },
  { id: 3, taskId: 'task-jdk-1', seq: 3, line: '[info] 安装完成', ts: iso(-3000000) },
  { id: 4, taskId: 'task-runtime-3', seq: 1, line: '[info] 开始下载运行时包', ts: iso(-7200000) },
  { id: 5, taskId: 'task-runtime-3', seq: 2, line: '[error] sha256 校验失败', ts: iso(-7100000) },
])

const logs = db<LogRow>('logs', () => [
  {
    id: 1,
    source: 'instance',
    level: 'info',
    instanceId: 1,
    instanceUuid: 'inst-survival',
    nodeId: 1,
    stream: 'stdout',
    message: '[Server] Done (12.3s)! For help, type "help"',
    time: iso(-120000),
  },
  {
    id: 2,
    source: 'instance',
    level: 'warn',
    instanceId: 1,
    instanceUuid: 'inst-survival',
    nodeId: 1,
    stream: 'stdout',
    message: "[Server] Can't keep up! Is the server overloaded? Running 2500ms behind",
    time: iso(-90000),
  },
  {
    id: 3,
    source: 'control_plane',
    level: 'error',
    instanceId: 0,
    instanceUuid: '',
    nodeId: 0,
    message: 'failed to dispatch backup: disk full',
    time: iso(-60000),
  },
  {
    id: 4,
    source: 'worker',
    level: 'debug',
    instanceId: 0,
    instanceUuid: '',
    nodeId: 1,
    message: 'heartbeat sent to control-plane',
    time: iso(-30000),
  },
])

// ── 指标时序生成（无独立集合，纯派生让图表有内容） ──

/** 生成一条等间隔时序：count 点、step 间隔，值在 [base, base+amp] 间正弦波动。 */
function makeSeriesPoints(
  count: number,
  stepMs: number,
  base: number,
  amp: number,
): { ts: string; avg: number; min: number; max: number }[] {
  const out: { ts: string; avg: number; min: number; max: number }[] = []
  for (let i = count - 1; i >= 0; i--) {
    const v = base + amp * (0.5 + 0.5 * Math.sin(i / 3))
    out.push({
      ts: iso(-i * stepMs),
      avg: Number(v.toFixed(2)),
      min: Number((v * 0.92).toFixed(2)),
      max: Number((v * 1.08).toFixed(2)),
    })
  }
  return out
}

/** 把 range 字符串映射为（点数, 步长 ms）。覆盖前端 RangePicker 常见档。 */
function rangePlan(range: string): { count: number; step: number } {
  switch (range) {
    case '15m':
      return { count: 15, step: 60_000 }
    case '1h':
      return { count: 30, step: 120_000 }
    case '7d':
      return { count: 28, step: 6 * 3600_000 }
    case '24h':
    default:
      return { count: 24, step: 3600_000 }
  }
}

const GIB = 1024 * 1024 * 1024

/** 节点序列：CPU%/负载/内存/磁盘/网络（metricKey 对齐 lib/monitor-metrics NODE_CHART_DEFS）。 */
function nodeSeries(range: string) {
  const { count, step } = rangePlan(range)
  const mk = (key: string, unit: string, base: number, amp: number) => ({
    metricKey: key,
    unit,
    world: '',
    points: makeSeriesPoints(count, step, base, amp),
  })
  return [
    mk('node_cpu_pct', '%', 40, 30),
    mk('node_load', '', 1.2, 1.5),
    mk('node_mem_used', 'bytes', 6 * GIB, 2 * GIB),
    mk('node_mem_total', 'bytes', 16 * GIB, 0),
    mk('node_disk_used', 'bytes', 120 * GIB, 5 * GIB),
    mk('node_disk_total', 'bytes', 512 * GIB, 0),
    mk('node_net_rx_rate', 'bytes/s', 2_000_000, 1_500_000),
    mk('node_net_tx_rate', 'bytes/s', 1_000_000, 800_000),
  ]
}

/** 实例序列：TPS/MSPT/堆/线程/玩家 + 分世界区块（world 非空）。 */
function instanceSeries(range: string) {
  const { count, step } = rangePlan(range)
  const mk = (key: string, unit: string, base: number, amp: number, world = '') => ({
    metricKey: key,
    unit,
    world,
    points: makeSeriesPoints(count, step, base, amp),
  })
  return [
    mk('inst_tps', '', 19.5, 0.5),
    mk('inst_mspt', 'ms', 30, 20),
    mk('inst_heap_used', 'bytes', 2 * GIB, 1 * GIB),
    mk('inst_heap_max', 'bytes', 4 * GIB, 0),
    mk('inst_threads', '', 80, 20),
    mk('inst_players_online', '', 12, 8),
    mk('world_loaded_chunks', '', 600, 200, 'world'),
    mk('world_loaded_chunks', '', 200, 80, 'world_nether'),
  ]
}

// ── 工具：从 channelIds(number[]) 转字符串化 JSON，与后端一致 ──
function stringifyChannelIds(ids?: number[]): string {
  return ids && ids.length ? JSON.stringify(ids) : ''
}

export const handlers = [
  // ===== metrics（FR-060/061） =====
  domainRoute('get', '/metrics/overview', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const range = url.searchParams.get('range') ?? '24h'
    const { count, step } = rangePlan(range)
    return HttpResponse.json({
      totals: {
        nodeCount: 2,
        onlineNodeCount: 2,
        runningInstances: 3,
        cpuPct: 47,
        loadAvg: 38,
        memUsedBytes: 12 * GIB,
        memTotalBytes: 32 * GIB,
        onlinePlayers: 18,
      },
      resolution: 'raw',
      trends: [
        { metricKey: 'node_cpu_pct', unit: '%', points: makeSeriesPoints(count, step, 40, 30) },
        { metricKey: 'node_load', unit: '', points: makeSeriesPoints(count, step, 1.2, 1.2) },
        { metricKey: 'node_mem_used', unit: 'bytes', points: makeSeriesPoints(count, step, 12 * GIB, 4 * GIB) },
        { metricKey: 'inst_players_online', unit: '', points: makeSeriesPoints(count, step, 14, 8) },
      ],
    })
  }),

  domainRoute('get', '/metrics/series', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const scope = url.searchParams.get('scope') ?? 'node'
    const range = url.searchParams.get('range') ?? '24h'
    const { step } = rangePlan(range)
    const all = scope === 'instance' ? instanceSeries(range) : nodeSeries(range)
    const wanted = url.searchParams.get('metrics')
    const series = wanted
      ? all.filter((s) => wanted.split(',').includes(s.metricKey))
      : all
    const span = (series[0]?.points.length ?? 0) * step
    return HttpResponse.json({
      resolution: 'raw',
      from: iso(-span),
      to: iso(0),
      series,
    })
  }),

  domainRoute('get', '/nodes/:id/metrics', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      cpuUsage: 47.2,
      memoryUsage: 38.5,
      diskUsage: 23.4,
      memoryUsedMb: 6291,
      memoryTotalMb: 16384,
      diskUsedMb: 122880,
      diskTotalMb: 524288,
    })
  }),

  domainRoute('get', '/instances/:id/metrics', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      tps: 19.8,
      onlinePlayers: 12,
      memoryMb: 2048,
      msptMillis: 28.4,
      threads: 86,
      cpuPercent: 35.6,
      heapMaxMb: 4096,
      uptimeSeconds: 86400,
      worlds: [
        { name: 'world', loadedChunks: 612, entities: 340, tileEntities: 180 },
        { name: 'world_nether', loadedChunks: 210, entities: 90, tileEntities: 40 },
      ],
      probeAvailable: true,
    })
  }),

  // ===== alerts 规则（FR-011/085） =====
  domainRoute('get', '/alerts/rules', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(alertRules.list())
  }),

  domainRoute('post', '/alerts/rules', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as Partial<AlertRule> & { channelIds?: number[] }
    const row = alertRules.insert({
      uuid: `rule-${Date.now()}`,
      name: body.name ?? '未命名规则',
      triggerType: body.triggerType ?? 'metric',
      level: body.level ?? 'warn',
      targetType: body.targetType ?? 'node',
      targetId: body.targetId ?? null,
      metric: body.metric ?? '',
      operator: body.operator ?? '',
      threshold: body.threshold ?? 0,
      durationSec: body.durationSec ?? 0,
      keyword: body.keyword ?? '',
      eventMatch: body.eventMatch ?? '',
      channelIds: stringifyChannelIds(body.channelIds),
      dedupWindowSec: body.dedupWindowSec ?? 0,
      silenceStart: body.silenceStart ?? '',
      silenceEnd: body.silenceEnd ?? '',
      notifyRecover: body.notifyRecover ?? false,
      notifyType: body.notifyType ?? '',
      notifyTarget: body.notifyTarget ?? '',
      enabled: true,
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(row, { status: 201 })
  }),

  domainRoute('put', '/alerts/rules/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json()) as Partial<AlertRule> & { channelIds?: number[] }
    const patch: Partial<AlertRule> = { ...body }
    if (body.channelIds !== undefined) patch.channelIds = stringifyChannelIds(body.channelIds)
    const row = alertRules.update(id, patch)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '规则不存在' }, { status: 404 })
    return HttpResponse.json(row)
  }),

  domainRoute('delete', '/alerts/rules/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    alertRules.remove(Number(info.params.id))
    return HttpResponse.json({ ok: true })
  }),

  // ===== alerts 事件（FR-149） =====
  domainRoute('get', '/alerts/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const level = url.searchParams.get('level')
    const resolvedParam = url.searchParams.get('resolved')
    const ackParam = url.searchParams.get('acknowledged')
    const ruleId = url.searchParams.get('ruleId')
    const keyword = url.searchParams.get('keyword')
    let items = alertEvents.list()
    if (level) items = items.filter((e) => e.level === level)
    if (resolvedParam != null) items = items.filter((e) => e.resolved === (resolvedParam === 'true'))
    if (ackParam != null) items = items.filter((e) => e.acknowledged === (ackParam === 'true'))
    if (ruleId) items = items.filter((e) => e.ruleId === Number(ruleId))
    if (keyword) items = items.filter((e) => e.message.includes(keyword))
    return HttpResponse.json({ items, total: items.length })
  }),

  domainRoute('get', '/alerts/events/unread-count', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ unread: alertEvents.list((e) => !e.read).length })
  }),

  domainRoute('post', '/alerts/events/:id/ack', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    alertEvents.update(id, { acknowledged: true, acknowledgedBy: 1, acknowledgedAt: new Date().toISOString(), read: true })
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('post', '/alerts/events/read-all', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    alertEvents.list().forEach((e) => alertEvents.update(e.id, { read: true }))
    return HttpResponse.json({ ok: true })
  }),

  // ===== alerts 通道（FR-085） =====
  domainRoute('get', '/alerts/channels', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(alertChannels.list())
  }),

  domainRoute('post', '/alerts/channels', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name?: string; type?: string; enabled?: boolean; config?: unknown }
    const row = alertChannels.insert({
      uuid: `chan-${Date.now()}`,
      name: body.name ?? '未命名通道',
      type: body.type ?? 'webhook',
      enabled: body.enabled ?? true,
      config: JSON.stringify(body.config ?? {}),
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(row, { status: 201 })
  }),

  domainRoute('put', '/alerts/channels/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json()) as { name?: string; type?: string; enabled?: boolean; config?: unknown }
    const patch: Partial<AlertChannel> = {}
    if (body.name !== undefined) patch.name = body.name
    if (body.type !== undefined) patch.type = body.type
    if (body.enabled !== undefined) patch.enabled = body.enabled
    if (body.config !== undefined) patch.config = JSON.stringify(body.config)
    const row = alertChannels.update(id, patch)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '通道不存在' }, { status: 404 })
    return HttpResponse.json(row)
  }),

  domainRoute('delete', '/alerts/channels/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    alertChannels.remove(Number(info.params.id))
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('post', '/alerts/channels/:id/test', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ ok: true, message: '测试通知已发送' })
  }),

  // ===== notifications 站内信（FR-183） =====
  domainRoute('get', '/notifications', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const onlyUnread = url.searchParams.get('unread') === 'true'
    const limit = Number(url.searchParams.get('limit') ?? '50')
    let items = notifications.list()
    if (onlyUnread) items = items.filter((n) => !n.readAt)
    return HttpResponse.json(items.slice(0, limit))
  }),

  domainRoute('get', '/notifications/unread-count', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ unread: notifications.list((n) => !n.readAt).length })
  }),

  domainRoute('post', '/notifications/:id/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    notifications.update(Number(info.params.id), { readAt: new Date().toISOString() })
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('post', '/notifications/read-all', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    notifications.list().forEach((n) => notifications.update(n.id, { readAt: new Date().toISOString() }))
    return HttpResponse.json({ ok: true })
  }),

  // ===== tasks 任务中心（FR-183） =====
  domainRoute('get', '/tasks', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const limit = Number(url.searchParams.get('limit') ?? '100')
    return HttpResponse.json(tasks.list().slice(0, limit))
  }),

  domainRoute('get', '/tasks/:taskId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const taskId = String(info.params.taskId)
    const task = tasks.find((t) => t.taskId === taskId)
    if (!task) return HttpResponse.json({ error: 'NOT_FOUND', message: '任务不存在' }, { status: 404 })
    return HttpResponse.json({ task, logs: taskLogs.list((l) => l.taskId === taskId) })
  }),

  // ===== logs 日志中心（FR-049/050/150） =====
  domainRoute('get', '/logs', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const source = url.searchParams.get('source')
    const level = url.searchParams.get('level')
    const nodeId = url.searchParams.get('nodeId')
    const instanceId = url.searchParams.get('instanceId')
    const keyword = url.searchParams.get('keyword')
    const page = Number(url.searchParams.get('page') ?? '1')
    const pageSize = Number(url.searchParams.get('pageSize') ?? '100')

    let items = logs.list()
    if (source) items = items.filter((l) => l.source === source)
    if (level) items = items.filter((l) => l.level === level)
    if (nodeId) items = items.filter((l) => l.nodeId === Number(nodeId))
    if (instanceId) items = items.filter((l) => l.instanceId === Number(instanceId))
    if (keyword) items = items.filter((l) => l.message.includes(keyword))

    const total = items.length
    const start = (page - 1) * pageSize
    return HttpResponse.json({ items: items.slice(start, start + pageSize), total, page, pageSize })
  }),

  domainRoute('get', '/logs/export', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const ndjson = logs
      .list()
      .map((l) => JSON.stringify(l))
      .join('\n')
    return new HttpResponse(ndjson, {
      headers: { 'Content-Type': 'application/x-ndjson' },
    })
  }),
]

/**
 * 域种子声明（spec §7 要求导出）：集合已在模块顶层带 seedFn 唯一声明并播种，
 * 本函数留空即可（import 触发播种，resetDb 重播），保留以符合范式契约。
 */
export function seed(): void {
  // 集合在顶层声明时即播种；此处无需重复。
}
