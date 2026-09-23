import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'
import type {
  AttributionInfo,
  CapacityForecastInfo,
  PlayerTrendInfo,
  RankingResultInfo,
  SLOInfo,
  Task,
} from '@jianmanager/devmock/contracts'

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
const YEAR_MS = 365 * 86_400_000
const MOCK_INSTANCE_COUNT = 1200
const MOCK_LOG_COUNT = 12_000
const MOCK_TASK_COUNT = 1500
const MOCK_FEED_COUNT = 1200

const TASK_KIND_POOL = ['jdk_install', 'instance_backup', 'runtime_install', 'client_publish', 'node_repair'] as const
const TASK_STATE_POOL: Task['state'][] = ['pending', 'running', 'succeeded', 'failed', 'canceled']
const LOG_LEVEL_POOL = ['debug', 'info', 'warn', 'error'] as const
const LOG_SOURCE_POOL = ['instance', 'control_plane', 'worker'] as const
const NOTIFICATION_LEVEL_POOL: Notification['level'][] = ['info', 'success', 'warning', 'error']

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

const alertEvents = db<AlertEvent>('alertEvents', seedAlertEvents)

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

const notifications = db<Notification>('notifications', seedNotifications)
const tasks = db<Task>('tasks', seedTasks)
/** 制品迁移域自持任务集合；任务中心合并读取，避免跨域重复声明 tasks 种子。 */
const artifactMigrationTasks = db<Task>('artifactMigrationTasks')
const taskLogs = db<TaskLog>('taskLogs', seedTaskLogs)
const logs = db<LogRow>('logs', seedLogs)

function seedAlertEvents(): AlertEvent[] {
  const base: AlertEvent[] = [
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
      firedAt: iso(-3_600_000),
      lastFiredAt: iso(-600_000),
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
      firedAt: iso(-7_200_000),
      resolvedAt: iso(-7_000_000),
      acknowledged: true,
      acknowledgedBy: 1,
      acknowledgedAt: iso(-6_900_000),
      read: true,
      rule: { name: '实例崩溃告警' },
    },
  ]

  const generated: AlertEvent[] = []
  for (let i = 3; i <= MOCK_FEED_COUNT + 2; i++) {
    const critical = i % 9 === 0
    const offset = -Math.floor(((i - 2) / MOCK_FEED_COUNT) * YEAR_MS)
    generated.push({
      id: i,
      ruleId: critical ? 2 : 1,
      targetId: ((i - 1) % MOCK_INSTANCE_COUNT) + 1,
      level: critical ? 'critical' : 'warn',
      triggerType: critical ? 'instance_crash' : 'metric',
      value: critical ? 0 : 75 + (i % 25),
      message: critical
        ? `实例 server-${String(((i - 1) % MOCK_INSTANCE_COUNT) + 1).padStart(4, '0')} 异常退出`
        : `节点 node-${(i % 2) + 1} 资源水位超过阈值 ${75 + (i % 25)}%`,
      count: 1 + (i % 5),
      resolved: i % 3 !== 0,
      firedAt: iso(offset),
      lastFiredAt: iso(offset + 600_000),
      resolvedAt: i % 3 !== 0 ? iso(offset + 1_800_000) : undefined,
      acknowledged: i % 4 !== 0,
      acknowledgedBy: i % 4 !== 0 ? 1 : undefined,
      acknowledgedAt: i % 4 !== 0 ? iso(offset + 900_000) : undefined,
      read: i % 6 !== 0,
      rule: { name: critical ? '实例崩溃告警' : 'CPU 过载告警' },
    })
  }
  return [...base, ...generated]
}

function seedNotifications(): Notification[] {
  const base: Notification[] = [
    {
      id: 1,
      userId: 1,
      level: 'success',
      title: 'JDK 安装完成',
      body: 'node-1 上 Temurin 21 安装成功',
      taskId: 'task-jdk-1',
      createdAt: iso(-1_800_000),
    },
    {
      id: 2,
      userId: 1,
      level: 'error',
      title: '备份失败',
      body: '实例 survival 备份失败：磁盘空间不足',
      createdAt: iso(-900_000),
    },
    {
      id: 3,
      userId: 1,
      level: 'info',
      title: '节点已上线',
      body: 'node-2 已注册并上线',
      readAt: iso(-600_000),
      createdAt: iso(-1_200_000),
    },
  ]

  const generated: Notification[] = []
  for (let i = 4; i <= MOCK_FEED_COUNT + 3; i++) {
    const offset = -Math.floor(((i - 3) / MOCK_FEED_COUNT) * YEAR_MS)
    const level = NOTIFICATION_LEVEL_POOL[i % NOTIFICATION_LEVEL_POOL.length]
    generated.push({
      id: i,
      userId: 1,
      level,
      title: `批量运维事件 ${i - 3}`,
      body: `server-${String(((i - 1) % MOCK_INSTANCE_COUNT) + 1).padStart(4, '0')} 的 ${TASK_KIND_POOL[i % TASK_KIND_POOL.length]} 已记录`,
      taskId: i % 5 === 0 ? `task-scale-${i}` : undefined,
      readAt: i % 4 === 0 ? iso(offset + 120_000) : undefined,
      createdAt: iso(offset),
    })
  }
  return [...base, ...generated]
}

function seedTasks(): Task[] {
  const base: Task[] = [
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
      cancelRequested: false,
      createdBy: 1,
      createdAt: iso(-3_600_000),
      updatedAt: iso(-3_000_000),
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
      cancelRequested: false,
      createdBy: 1,
      createdAt: iso(-300_000),
      updatedAt: iso(-30_000),
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
      cancelRequested: false,
      createdBy: 1,
      createdAt: iso(-7_200_000),
      updatedAt: iso(-7_100_000),
    },
    {
      // 网络类 JDK 下载失败（FR-279）：错误含 Go stdlib 稳定标记 + Worker 追加的中文引导，
      // 供 TasksPage 渲染「去配代理 / 换镜像」入口的 DOM 断言。
      id: 4,
      taskId: 'task-jdk-neterr',
      nodeId: 2,
      kind: 'jdk_install',
      state: 'failed',
      progress: 5,
      title: '受限网络下装 JDK 21（网络失败）',
      detail: 'node-2',
      error:
        '下载失败: Get "https://github.com/adoptium/temurin21-binaries/releases/download/...": net/http: TLS handshake timeout（疑似网络受限：JDK 下载经节点出站代理执行、未配置则直连，可在面板「设置 → 网络」配置出站代理，或在「运行时资产」页更换 JDK 下载源/镜像后重试）',
      result: '',
      cancelRequested: false,
      createdBy: 1,
      createdAt: iso(-3_600_000),
      updatedAt: iso(-3_500_000),
    },
  ]

  const generated: Task[] = []
  for (let i = 4; i <= MOCK_TASK_COUNT + 3; i++) {
    const state = TASK_STATE_POOL[i % TASK_STATE_POOL.length]
    const kind = TASK_KIND_POOL[i % TASK_KIND_POOL.length]
    const offset = -Math.floor(((i - 3) / MOCK_TASK_COUNT) * YEAR_MS)
    const progress = state === 'succeeded' ? 100 : state === 'pending' ? 0 : 10 + (i % 85)
    generated.push({
      id: i,
      taskId: `task-scale-${i}`,
      nodeId: i % 2 === 0 ? 2 : 1,
      kind,
      state,
      progress,
      title: `${kind} 批量任务 ${i - 3}`,
      detail: `server-${String(((i - 1) % MOCK_INSTANCE_COUNT) + 1).padStart(4, '0')}`,
      error: state === 'failed' ? '模拟失败：节点返回非零退出码' : '',
      result: state === 'succeeded' ? JSON.stringify({ ok: true, batch: i }) : '',
      cancelRequested: state === 'running' && i % 7 === 0,
      createdBy: 1,
      createdAt: iso(offset),
      updatedAt: iso(offset + 300_000),
    })
  }
  return [...base, ...generated]
}

function seedTaskLogs(): TaskLog[] {
  const base: TaskLog[] = [
    { id: 1, taskId: 'task-jdk-1', seq: 1, line: '[info] 开始下载 Temurin 21', ts: iso(-3_600_000) },
    { id: 2, taskId: 'task-jdk-1', seq: 2, line: '[info] 解压到 /opt/jdk/temurin-21', ts: iso(-3_300_000) },
    { id: 3, taskId: 'task-jdk-1', seq: 3, line: '[info] 安装完成', ts: iso(-3_000_000) },
    { id: 4, taskId: 'task-runtime-3', seq: 1, line: '[info] 开始下载运行时包', ts: iso(-7_200_000) },
    { id: 5, taskId: 'task-runtime-3', seq: 2, line: '[error] sha256 校验失败', ts: iso(-7_100_000) },
  ]

  const generated: TaskLog[] = []
  for (let task = 4; task <= 260; task++) {
    for (let seq = 1; seq <= 4; seq++) {
      const id = 5 + (task - 4) * 4 + seq
      generated.push({
        id,
        taskId: `task-scale-${task}`,
        seq,
        line: `[info] 执行批量任务 ${task - 3} 步骤 ${seq}/4`,
        ts: iso(-Math.floor((task / 260) * YEAR_MS) + seq * 60_000),
      })
    }
  }
  return [...base, ...generated]
}

function seedLogs(): LogRow[] {
  const base: LogRow[] = [
    {
      id: 1,
      source: 'instance',
      level: 'info',
      instanceId: 1,
      instanceUuid: 'inst-survival',
      nodeId: 1,
      stream: 'stdout',
      message: '[Server] Done (12.3s)! For help, type "help"',
      time: iso(-120_000),
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
      time: iso(-90_000),
    },
    {
      id: 3,
      source: 'control_plane',
      level: 'error',
      instanceId: 0,
      instanceUuid: '',
      nodeId: 0,
      message: 'failed to dispatch backup: disk full',
      time: iso(-60_000),
    },
    {
      id: 4,
      source: 'worker',
      level: 'debug',
      instanceId: 0,
      instanceUuid: '',
      nodeId: 1,
      message: 'heartbeat sent to control-plane',
      time: iso(-30_000),
    },
  ]

  const generated: LogRow[] = []
  for (let i = 5; i <= MOCK_LOG_COUNT + 4; i++) {
    const instanceId = ((i - 1) % MOCK_INSTANCE_COUNT) + 1
    const source = LOG_SOURCE_POOL[i % LOG_SOURCE_POOL.length]
    const level = LOG_LEVEL_POOL[i % LOG_LEVEL_POOL.length]
    const offset = -Math.floor(((i - 4) / MOCK_LOG_COUNT) * YEAR_MS)
    generated.push({
      id: i,
      source,
      level,
      instanceId: source === 'instance' ? instanceId : 0,
      instanceUuid: source === 'instance' ? `i-scale-${instanceId}` : '',
      nodeId: i % 2 === 0 ? 2 : 1,
      stream: source === 'instance' ? (i % 13 === 0 ? 'stderr' : 'stdout') : undefined,
      message: source === 'instance'
        ? `[Server ${instanceId}] ${level.toUpperCase()} tick=${i} players=${i % 80}`
        : `${source} ${level} event ${i}`,
      time: iso(offset),
    })
  }
  return [...base, ...generated]
}

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
    case '1h':
      return { count: 60, step: 60_000 }
    case '6h':
      return { count: 72, step: 5 * 60_000 }
    case '7d':
      return { count: 168, step: 3600_000 }
    case '30d':
      return { count: 180, step: 4 * 3600_000 }
    case '90d':
      return { count: 180, step: 12 * 3600_000 }
    case '1y':
      return { count: 365, step: 24 * 3600_000 }
    case '24h':
    default:
      return { count: 96, step: 15 * 60_000 }
  }
}

const MIB = 1024 * 1024
const GIB = 1024 * MIB

/** range 字符串 → 毫秒跨度（SLO/容量预测窗口用）。 */
function rangeSpanMs(range: string): number {
  const { count, step } = rangePlan(range)
  return count * step
}

/** window 字符串 → 秒（排行响应回显 windowSeconds）。 */
function windowSeconds(window: string): number {
  switch (window) {
    case '5m':
      return 300
    case '1h':
      return 3600
    case '24h':
      return 86400
    case '7d':
      return 7 * 86400
    default:
      return 300
  }
}

/** 时区名 → 相对 UTC 的小时偏移（仅用于演示时段分布随时区整体平移）。 */
function tzHourOffset(tz: string): number {
  if (tz === 'UTC' || tz === 'Etc/UTC') return 0
  const m = /^UTC([+-])(\d{1,2})$/.exec(tz)
  if (m) return (m[1] === '-' ? -1 : 1) * Number(m[2])
  // 常见 IANA 名兜底（假后端不求完备，够演示「时段随时区变化」即可）。
  if (tz.includes('Shanghai') || tz.includes('Chongqing') || tz.includes('Asia/Shanghai')) return 8
  if (tz.includes('Tokyo')) return 9
  if (tz.includes('New_York')) return -5
  if (tz.includes('London')) return 0
  return 0
}

/**
 * 排行种子行（FR-469）：假后端从实例集合取前若干台，按指标的「差」方向造值。
 *
 * `nodeUuid` 必须是**真实 seed 的节点 UUID**：真后端 `RankingItem.nodeUuid` 取
 * `metric_series.node_uuid`，前端 `InstanceRankingPanel` 用
 * `nodes.find(n => n.uuid === row.nodeUuid)?.name` 渲染「节点」列——杜撰的 UUID 会
 * 解析不出名称、退化成截断 UUID（`node-moc…`），而节点筛选下拉的 value 正是节点 UUID，
 * 两边对不上就成了「选了没反应」。
 */
function rankingSeed(
  metricKey: string,
  nodeUUID?: string,
): { instanceId: number; instanceUuid: string; name: string; nodeUuid: string; value: number }[] {
  // 只读借用实例 / 节点集合（不重复声明 seedFn，避免覆盖它域播种）。
  const nodeUuidById = new Map(
    db<{ id: number; uuid: string }>('nodes')
      .list()
      .map((n) => [n.id, n.uuid]),
  )
  let rows = db<{ id: number; uuid: string; name: string; nodeId: number }>('instances').list().slice(0, 12)
  // 节点筛选：真后端按 `s.node_uuid = ?` 过滤（router/metric.go 的 nodeId → RankingQuery.NodeUUID），
  // 此处同样**消费**该参数，否则下拉选了没反应。
  // 注意取样与过滤的先后：先取候选池（slice 保持原有 12 条上限）再过滤，筛选才能剪掉条目；
  // 若反过来「每个节点各取 12 条」，节点筛选就永远返回满额、看起来依然没反应。
  // 只认**节点 UUID**（与真后端一致）：假后端实例只存 nodeId（数字），
  // 故必须经 nodeUuidById 换成 UUID 再比，避免拿数字 id 冒充 UUID 过滤。
  if (nodeUUID) rows = rows.filter((inst) => nodeUuidById.get(inst.nodeId) === nodeUUID)
  return rows.slice(0, 12).map((inst) => {
    // 用确定性伪随机（按 id 哈希）避免每次请求数值抖动导致表格跳动。
    const h = (inst.id * 2654435761) % 1000
    const frac = h / 1000
    const value =
      metricKey === 'inst_tps'
        ? Number((8 + frac * 12).toFixed(2))
        : metricKey === 'inst_mspt'
          ? Number((18 + frac * 82).toFixed(1))
          : metricKey === 'inst_cpu_pct'
            ? Number((12 + frac * 78).toFixed(1))
            : metricKey === 'inst_heap_used'
              ? Math.round((0.4 + frac * 1.5) * GIB)
              : Math.round(frac * 60)
    return {
      instanceId: inst.id,
      instanceUuid: inst.uuid,
      name: inst.name,
      // 取实例所属节点的真实 UUID。节点已归档/已删时按空串处理（真后端 JOIN instances
      // 且序列 node_uuid 恒有值，此处只兜住 seed 不一致，不杜撰、不显示假节点）。
      nodeUuid: nodeUuidById.get(inst.nodeId) ?? '',
      value,
    }
  })
}

/**
 * 容量上限指标键（与 Go 侧 `saturationMaxKey`（alert_baseline.go:338）逐条对齐）。
 * 真后端 `forecastLimit` 优先取该上限序列，缺序列时才回退节点注册快照。
 */
const CAPACITY_LIMIT_KEY: Record<string, string> = {
  node_disk_used: 'node_disk_total',
  node_mem_used: 'node_mem_total',
  inst_heap_used: 'inst_heap_max',
}

/**
 * 演示用外推天数（天）。**只有在此表内的「已用」指标**才是「有增长可外推」，
 * 其余键按真后端 `β ≤ 0` 分支返回「无增长趋势，不预测」——刻意保留 node_mem_used 不在表内，
 * 让「不预测」负例在 mock 模式下可复现（真后端同窗口下该指标同样算不出正增长）。
 */
const CAPACITY_GROWTH_DAYS: Record<string, number> = {
  node_disk_used: 5.2,
  inst_heap_used: 12.5,
}

/** 80% 置信区间（天），与点估计成比例，保证 low < days < high。 */
const CAPACITY_CI_DAYS: Record<string, { low: number; high: number }> = {
  node_disk_used: { low: 4.1, high: 7.0 },
  inst_heap_used: { low: 9.8, high: 16.2 },
}

/** 三种 insufficient 原因（与 capacity.go:173/:179/:189 的 note 逐字一致）。 */
const CAPACITY_NOTE_NO_SAMPLES = '样本不足，不预测'
const CAPACITY_NOTE_NO_LIMIT = '缺少容量上限（无上限序列且无节点容量快照），不预测'
const CAPACITY_NOTE_NO_TREND = '无增长趋势，不预测'

/** 真后端 capacityMinSamples（capacity.go:16）：少于该点数一律「样本不足，不预测」。 */
const CAPACITY_MIN_SAMPLES = 100

/**
 * 窗口内的样本点数，按真后端 `selectResolution` 的档位口径推算
 * （≤48h → raw 30s；≤30d → 5m；更长 → 1h；见 capacity.go:150 的 `selectResolution(span, "auto")`）。
 *
 * 不直接用 `rangePlan(range).count`：那是**图表**的分桶点数（如 24h → 96 个 15min 桶），
 * 而容量外推消费的是原始/归档样本点（24h → 2880 拍）。混用会让默认 24h 视图被误判为
 * 「样本不足」（96 < capacityMinSamples），与真后端行为相反。
 */
function capacitySamples(range: string): number {
  const spanSec = rangeSpanMs(range) / 1000
  const intervalSec = spanSec <= 48 * 3600 ? 30 : spanSec <= 30 * 86400 ? 300 : 3600
  return Math.floor(spanSec / intervalSec)
}

/**
 * 取 mock 自身时序的最新值（序列不存在返回 undefined）。
 *
 * 关键：容量卡与同页曲线图**共用同一份派生时序**，故 `nowValue`/`limitValue` 必须从这里取，
 * 而不是另写一套常量——旧实现给 `node_mem_used` 硬编码 heap 量级的 2 GiB 上限，
 * 与页面上同指标的曲线量级互相矛盾（M1）。
 */
function seriesLatestValue(range: string, scope: string, metricKey: string): number | undefined {
  const all = scope === 'instance' ? instanceSeries(range) : nodeSeries(range)
  const hit = all.find((s) => s.metricKey === metricKey)
  const last = hit?.points[hit.points.length - 1]
  return last ? last.avg : undefined
}

/**
 * 单指标容量预测行（FR-464）。分档顺序刻意复刻真后端 `ForecastCapacity`：
 * ① 无序列 / 点数 < capacityMinSamples → 「样本不足，不预测」；
 * ② 无配对上限（`CAPACITY_LIMIT_KEY` 无该键或上限序列缺失）→ 「缺少容量上限…」；
 * ③ 无可外推趋势（不在 `CAPACITY_GROWTH_DAYS`）→ 「无增长趋势，不预测」；
 * ④ 否则给低置信度点估计 + 80% CI。
 *
 * 未知指标键由此**不再静默降级**：它拿不到序列，走 ① 并显式 `samples: 0`，
 * 与真后端「该键无序列 → 样本不足」的响应一致，而不是伪造 `samples: 1840` 的成功体（M1/M9）。
 */
function capacityForecastRow(
  scope: string,
  range: string,
  targetId: string,
  metricKey: string,
): CapacityForecastInfo['forecasts'][number] {
  const base = {
    targetId,
    metricKey,
    slopePerSec: 0,
    exhaustAt: null,
    exhaustLowDays: null,
    exhaustHighDays: null,
    confidence: 'insufficient' as const,
  }
  const nowValue = seriesLatestValue(range, scope, metricKey)
  const limitValue = seriesLatestValue(range, scope, CAPACITY_LIMIT_KEY[metricKey] ?? '')
  // 真后端样本数 = 窗口内该指标的对齐点数；mock 无该序列时为 0（如实反映「查不到」）。
  const samples = nowValue === undefined ? 0 : capacitySamples(range)
  if (nowValue === undefined || samples < CAPACITY_MIN_SAMPLES) {
    return { ...base, nowValue: 0, limitValue: 0, samples, note: CAPACITY_NOTE_NO_SAMPLES }
  }
  if (limitValue === undefined || limitValue <= 0) {
    return { ...base, nowValue, limitValue: 0, samples, note: CAPACITY_NOTE_NO_LIMIT }
  }
  const days = CAPACITY_GROWTH_DAYS[metricKey]
  const ci = CAPACITY_CI_DAYS[metricKey]
  if (days === undefined || ci === undefined) {
    return { ...base, nowValue, limitValue, samples, note: CAPACITY_NOTE_NO_TREND }
  }
  // slope 反推自 (limit - now) / 天数，使三者在同一响应内自洽（不再各写各的常量）。
  return {
    targetId,
    metricKey,
    nowValue,
    limitValue,
    slopePerSec: (limitValue - nowValue) / (days * 86400),
    exhaustAt: iso(days * 86400_000),
    exhaustLowDays: ci.low,
    exhaustHighDays: ci.high,
    confidence: 'low',
    samples,
    note: '',
  }
}

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

/** 实例序列：TPS/MSPT/堆/线程/玩家/CPU + 分世界区块（world 非空）。 */
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
    mk('inst_cpu_pct', '%', 42, 25),
    mk('world_loaded_chunks', '', 600, 200, 'world'),
    mk('world_loaded_chunks', '', 200, 80, 'world_nether'),
  ]
}

// ── 工具：从 channelIds(number[]) 转字符串化 JSON，与后端一致 ──
function stringifyChannelIds(ids?: number[]): string {
  return ids && ids.length ? JSON.stringify(ids) : ''
}

function parseChannelIds(raw?: string): number[] {
  if (!raw) return []
  try {
    const value = JSON.parse(raw)
    return Array.isArray(value) ? value.filter((id): id is number => typeof id === 'number') : []
  } catch {
    return []
  }
}

/** 统一通知条目（对齐 api/notification-feed.ts FeedItem）。 */
interface FeedItemMock {
  source: 'message' | 'alert'
  id: number
  level: 'info' | 'success' | 'warning' | 'error'
  title: string
  body: string
  read: boolean
  createdAt: string
  taskId?: string
  triggerType?: string
  acknowledged?: boolean
  resolved?: boolean
}

/** 告警三档级别就近映射到统一四档（warn→warning、critical→error，与后端一致）。 */
function alertLevelToUnified(level: string): FeedItemMock['level'] {
  if (level === 'critical') return 'error'
  if (level === 'warn') return 'warning'
  return 'info'
}

/** 把 notifications + alertEvents 合并为统一通知流（FR-216，见 ADR-048）。 */
function mergedFeed(): FeedItemMock[] {
  const messages: FeedItemMock[] = notifications.list().map((n) => ({
    source: 'message',
    id: n.id,
    level: n.level,
    title: n.title,
    body: n.body,
    read: !!n.readAt,
    createdAt: n.createdAt,
    taskId: n.taskId,
  }))
  const alerts: FeedItemMock[] = alertEvents.list().map((e) => ({
    source: 'alert',
    id: e.id,
    level: alertLevelToUnified(e.level),
    title: e.rule?.name ?? `#${e.ruleId}`,
    body: e.message,
    read: e.read,
    createdAt: e.firedAt,
    triggerType: e.triggerType,
    acknowledged: e.acknowledged,
    resolved: e.resolved,
  }))
  return [...messages, ...alerts]
}

/**
 * SLO 是否拿到「可用证据」（FR-463）。真后端平台维按「存在 `inst_uptime` 序列 && 实例仍存在」
 * 判定（slo.go:213-217），实例维按该实例的 `inst_uptime` 拍、节点维按 `node_cpu_pct` 拍
 * （slo.go:72）。对应到 mock：证据序列等价于「目标在跑」——
 * - platform：至少一台实例 RUNNING（无 RUNNING 实例 ⇒ 无任何 uptime 拍）；
 * - instance：该 UUID 的实例存在且 RUNNING；
 * - node：该 UUID 的节点在线（status===1 且未归档，归档节点不再上报）。
 * 目标不存在时同样返回 false（真后端给 404/403，此处保守按「无证据」让前端渲染「不适用」）。
 */
function hasSLOEvidence(scope: 'platform' | 'node' | 'instance', targetId: string): boolean {
  interface SloInstance {
    id: number
    uuid: string
    nodeId: number
    status: string
  }
  const running = db<SloInstance>('instances').list((i) => i.status === 'RUNNING')
  if (scope === 'platform') return running.length > 0
  if (scope === 'instance') return running.some((i) => i.uuid === targetId)
  interface SloNode {
    id: number
    uuid: string
    status: number
    deletedAt?: string
  }
  const node = db<SloNode>('nodes').find((n) => n.uuid === targetId)
  return !!node && node.status === 1 && !node.deletedAt
}

/** 全景观测读模型用到的节点快照字段（从 db('nodes') 派生，仅为本地类型收窄）。 */
interface ObsNode {
  id: number
  uuid: string
  name: string
  status: number
  cpuUsage?: number
  memoryUsage?: number
  diskUsage?: number
  deletedAt?: string
}

/** 全景观测读模型用到的实例快照字段（从 db('instances') 派生）。 */
interface ObsInstance {
  nodeId: number
  status: string
}

/** 健康墙分级（对齐前端 HealthLevel）。 */
type HealthWallLevel = 'offline' | 'stale' | 'degraded' | 'healthy'

/** 健康墙单格（对齐前端 HealthWallNode）。 */
interface HealthWallNodeInfo {
  nodeId: number
  nodeUuid: string
  name: string
  freshness: 'fresh' | 'stale' | 'offline'
  cpuPct: number | null
  memPct: number | null
  diskPct: number | null
  running: number
  crashed: number
  stopped: number
  degraded?: number
  activeAlerts: number
  botActive: number | null
  botConnecting: number | null
  level: HealthWallLevel
  href: string
}

export const handlers = [
  // ===== 平台全景观测（FR-402 / FR-461） =====
  //
  // 说明（FR-461 补齐）：`/observability/overview` 与 `/observability/health-wall` 是平台首页
  // 与总览页的读模型，此前 devmock 缺这两条 handler——前端请求 MSW 不拦截即透传 vite proxy，
  // 真后端不存在时报 ECONNREFUSED，E2E（navigation-benchmark / metrics-probe）随之失败。
  // 此处按真实响应结构（api/metrics.ts 的 PlatformObservabilityOverviewResponse / HealthWallResponse）
  // 从 db('nodes') + db('instances') 派生，保持「有内容且自洽」。
  domainRoute('get', '/observability/overview', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodes = db<ObsNode>('nodes').filter((n) => !n.deletedAt)
    const insts = db<ObsInstance>('instances')
    const online = nodes.filter((n) => n.status === 1).length
    return HttpResponse.json({
      sampledAt: new Date().toISOString(),
      health: {
        nodeCount: nodes.length,
        onlineNodeCount: online,
        staleNodeCount: 0,
        offlineNodeCount: nodes.length - online,
        runningInstanceCount: insts.filter((i) => i.status === 'RUNNING').length,
        crashedInstanceCount: insts.filter((i) => i.status === 'CRASHED').length,
        stoppedInstanceCount: insts.filter((i) => i.status === 'STOPPED').length,
      },
      resources: {
        cpuPct: 47,
        loadPct: 38,
        memoryUsedBytes: 120 * GIB,
        memoryTotalBytes: 320 * GIB,
        freshness: 'fresh',
      },
      bots: {
        sharedRuntime: true,
        notice: 'Bot Worker 为节点级共享进程，资源不归属单个 Bot 或会话。',
        nodeCount: nodes.length,
        botWorkerRssBytes: 96 * MIB,
        botWorkerCpuPct: 12,
        workerProcessRssBytes: 32 * MIB,
        workerProcessCpuPct: 3,
        activeCount: 24,
        connectingCount: 2,
        eventLoopP95Ms: 18,
        unavailable: [],
      },
      alerts: [],
      tasks: [],
      exceptions: [],
    })
  }),

  // FR-461 健康墙：逐节点只读快照 + 分级。排序键与真后端一致（level/cpu/mem/disk/instances）。
  domainRoute('get', '/observability/health-wall', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const sort = new URL(info.request.url).searchParams.get('sort') ?? 'level'
    const nodes = db<ObsNode>('nodes').filter((n) => !n.deletedAt)
    const insts = db<ObsInstance>('instances')
    const nodes4: HealthWallNodeInfo[] = nodes.map((n) => {
      const mine = insts.filter((i) => i.nodeId === n.id)
      const crashed = mine.filter((i) => i.status === 'CRASHED').length
      const online = n.status === 1
      // 与真后端分级一致：离线 → offline；在线但有崩溃实例 → degraded；其余 healthy。
      const level: HealthWallLevel = !online ? 'offline' : crashed > 0 ? 'degraded' : 'healthy'
      return {
        nodeId: n.id,
        nodeUuid: n.uuid,
        name: n.name,
        freshness: online ? 'fresh' : 'offline',
        cpuPct: Math.round((n.cpuUsage ?? 0) * 100),
        memPct: Math.round((n.memoryUsage ?? 0) * 100),
        diskPct: Math.round((n.diskUsage ?? 0) * 100),
        running: mine.filter((i) => i.status === 'RUNNING').length,
        crashed,
        stopped: mine.filter((i) => i.status === 'STOPPED').length,
        degraded: 0,
        activeAlerts: 0,
        botActive: 0,
        botConnecting: 0,
        level,
        href: `/monitoring?node=${n.uuid}`,
      }
    })
    const levelRank: Record<HealthWallLevel, number> = { offline: 0, degraded: 1, stale: 2, healthy: 3 }
    const sorted = [...nodes4].sort((a, b) => {
      switch (sort) {
        case 'cpu':
          return (b.cpuPct ?? 0) - (a.cpuPct ?? 0)
        case 'mem':
          return (b.memPct ?? 0) - (a.memPct ?? 0)
        case 'disk':
          return (b.diskPct ?? 0) - (a.diskPct ?? 0)
        case 'instances':
          return b.running + b.crashed + b.stopped - (a.running + a.crashed + a.stopped)
        default:
          return levelRank[a.level] - levelRank[b.level]
      }
    })
    return HttpResponse.json({ nodes: sorted })
  }),

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
        runningInstances: 720,
        cpuPct: 47,
        loadAvg: 38,
        memUsedBytes: 120 * GIB,
        memTotalBytes: 320 * GIB,
        onlinePlayers: 3180,
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

  // 批量多实例序列（FR-340）：镜像真后端契约——targetIds 去重后 1~50，按 targetId 生成同构序列，
  // 支持 metrics 过滤；假后端无访问模型，解析到的目标全放行，skipped 恒空（真机才有 forbidden/not_found）。
  domainRoute('post', '/metrics/series/batch', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      scope?: string
      targetIds?: string[]
      metrics?: string[]
      range?: string
      resolution?: string
    }
    if (body.scope !== 'instance') {
      return HttpResponse.json({ error: 'INVALID_SCOPE', message: 'scope 必须为 instance' }, { status: 400 })
    }
    const targetIds = [...new Set((body.targetIds ?? []).map((s) => s.trim()).filter(Boolean))]
    if (targetIds.length === 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'targetIds 缺失或为空' }, { status: 400 })
    }
    if (targetIds.length > 50) {
      return HttpResponse.json({ error: 'TOO_MANY_TARGETS', message: '对比目标过多，最多 50 个' }, { status: 422 })
    }
    const range = body.range ?? '24h'
    const wanted = body.metrics
    const { step } = rangePlan(range)
    const series: Record<string, ReturnType<typeof instanceSeries>> = {}
    let span = 0
    for (const id of targetIds) {
      const all = instanceSeries(range)
      const filtered = wanted?.length ? all.filter((s) => wanted.includes(s.metricKey)) : all
      series[id] = filtered
      span = Math.max(span, (filtered[0]?.points.length ?? 0) * step)
    }
    return HttpResponse.json({
      resolution: 'raw',
      from: iso(-span),
      to: iso(0),
      series,
      skipped: [],
    })
  }),

  domainRoute('get', '/metrics/bot-runtime', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const nodeId = Number(url.searchParams.get('nodeId') ?? 1)
    return HttpResponse.json({
      resolution: 'raw',
      from: iso(-3600),
      to: iso(0),
      sharedRuntime: true,
      notice: 'Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。',
      nodes: [{ nodeId, nodeName: '北京节点', series: [] }],
      unavailable: [],
    })
  }),

  // ===== FR-463/464/465/469：SLO 可用性 / 容量预测 / 性能归因 / 跨实例排行与玩家趋势 =====

  // 性能归因（FR-465）：假后端给一个「GC 暂停占用为主因」的结论，让归因卡有内容可渲染。
  domainRoute('get', '/metrics/performance/attribution', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const targetId = url.searchParams.get('targetId') ?? ''
    const range = url.searchParams.get('range') ?? '7d'
    const target = url.searchParams.get('metric') ?? 'inst_tps'
    // 无 targetId 与真后端一致拒绝（INVALID_TARGET）。
    if (!targetId) {
      return HttpResponse.json({ error: 'INVALID_TARGET', message: 'targetId 非空' }, { status: 400 })
    }
    const body: AttributionInfo = {
      target,
      window: { from: iso(-rangeSpanMs(range)), to: iso(0) },
      status: 'ok',
      tldr: '劣化主因：GC 暂停占用（权重 0.62）',
      factors: [
        { metricKey: 'inst_gc_time_ms', label: 'GC 暂停占用', correlation: -0.81, weight: 0.62, note: '相关性非因果' },
        { metricKey: 'world_loaded_chunks', label: '已加载区块', correlation: -0.44, weight: 0.23, note: '相关性非因果' },
        { metricKey: 'world_entities', label: '实体数', correlation: -0.31, weight: 0.15, note: '相关性非因果' },
      ],
      samples: 2016,
    }
    return HttpResponse.json(body)
  }),

  // 跨实例全局排行（FR-469）：镜像真后端契约（items 排序 + rank + skippedNoData）。
  domainRoute('get', '/metrics/instances/ranking', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const metricKey = url.searchParams.get('metric') ?? 'inst_tps'
    const order = url.searchParams.get('order') ?? (metricKey === 'inst_tps' ? 'asc' : 'desc')
    const window = url.searchParams.get('window') ?? '5m'
    const limit = Math.min(100, Math.max(1, Number(url.searchParams.get('limit') ?? '20')))
    // nodeId 是**节点 UUID**（真后端 `c.Query("nodeId")` → RankingQuery.NodeUUID）。
    // 节点不存在时真后端返 404 TARGET_NOT_FOUND，此处同口径（否则前端筛了个不存在的节点会静默拿到全量榜）。
    const nodeUUID = url.searchParams.get('nodeId') ?? ''
    if (nodeUUID) {
      const known = db<{ id: number; uuid: string }>('nodes')
        .list()
        .some((n) => n.uuid === nodeUUID)
      if (!known) {
        return HttpResponse.json({ error: 'TARGET_NOT_FOUND', message: '节点不存在' }, { status: 404 })
      }
    }
    const rows = rankingSeed(metricKey, nodeUUID || undefined).sort((a, b) =>
      order === 'asc' ? a.value - b.value : b.value - a.value,
    )
    const items = rows.slice(0, limit).map((r, i) => ({ ...r, rank: i + 1, sampledAt: iso(-60_000) }))
    const body: RankingResultInfo = {
      metricKey,
      order: order === 'asc' ? 'asc' : 'desc',
      windowSeconds: windowSeconds(window),
      scoped: false,
      skippedNoData: 2,
      items,
    }
    return HttpResponse.json(body)
  }),

  // 玩家在线趋势与时段分析（FR-469）：24 时段分布按 tz 参数整体平移，便于验证时区口径。
  domainRoute('get', '/metrics/players/trend', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const range = url.searchParams.get('range') ?? '7d'
    const tz = url.searchParams.get('tz') ?? 'UTC'
    const { count, step } = rangePlan(range)
    const trend = makeSeriesPoints(count, step, 220, 160)
    const shift = tzHourOffset(tz)
    const dist = Array.from({ length: 24 }, (_, h) => {
      const local = (h + shift + 24) % 24
      // 双峰：晚间 20 点与午后 14 点高峰。
      return Number((120 + 90 * Math.exp(-((local - 20) ** 2) / 8) + 45 * Math.exp(-((local - 14) ** 2) / 12)).toFixed(1))
    })
    const peak = Math.max(...dist)
    const body: PlayerTrendInfo = {
      resolution: '5m',
      timezone: tz,
      trend,
      hourlyDist: dist,
      peakValue: peak,
      peakAt: iso(-3600_000),
      dailyAvg: Number((dist.reduce((a, b) => a + b, 0) / 24).toFixed(1)),
    }
    return HttpResponse.json(body)
  }),

  // 可用性 / SLO（FR-463）：平台与实例维度同构；MTTR/MTBF 用 null 演示「无故障」语义。
  domainRoute('get', '/metrics/slo', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const scope = url.searchParams.get('scope') ?? 'platform'
    const target = Number(url.searchParams.get('target') ?? '0') || 0.995
    const range = url.searchParams.get('range') ?? '24h'
    const targetId = url.searchParams.get('targetId') ?? ''
    if (scope !== 'platform' && !targetId) {
      return HttpResponse.json({ error: 'INVALID_SCOPE', message: 'node/instance 维度要求 targetId 非空' }, { status: 400 })
    }
    const spanSec = rangeSpanMs(range) / 1000
    const scopeNorm = scope === 'node' || scope === 'instance' ? scope : 'platform'
    // 「有无可用证据」由 **seed 数据** 决定，而不是恒为「有」——
    // 真后端 applicable=false 的条件是「窗口内该维度查不到可用证据序列」
    // （slo.go:213-217：平台维 len(series)==0 → 不适用；spec §4 验收点 1 要求前端渲染「不适用」）。
    // 对应到 mock：证据序列等价于「目标在跑」——实例维看 status==='RUNNING'（探针只在运行时上报
    // inst_uptime），节点维看 status===1（在线节点才上报 node_cpu_pct）。若一律给 applicable=true，
    // `npm run dev:mock` 下这条关键语义永远复现不出，而真机沙箱恰好就是该态（spec 备注：inst_uptime 序列数 = 0）。
    const evidence = hasSLOEvidence(scopeNorm, targetId)
    if (!evidence) {
      // 与真后端同构：分母为 0 时不给可用率/误差预算数字，否则前端会读成「预算充裕」
      // 或「100% 已消耗」（m1）。故障/恢复仍按窗口给值——它们来自 AlertEvent，与证据无关。
      const body: SLOInfo = {
        scope: scopeNorm,
        availability: 0,
        totalSamples: 0,
        upSamples: 0,
        incidents: 3,
        activeIncidents: 1,
        mttrSeconds: 412.5,
        mtbfSeconds: spanSec / 3,
        budgetAllowedSec: 0,
        budgetBurnedSec: 0,
        target,
        approximatedBuckets: false,
        applicable: false,
      }
      return HttpResponse.json(body)
    }
    const totalSamples = Math.max(1, Math.round(spanSec / 30))
    const upSamples = Math.round(totalSamples * 0.9975)
    const availability = upSamples / totalSamples
    const allowed = spanSec * (1 - target)
    const body: SLOInfo = {
      scope: scopeNorm,
      availability,
      totalSamples,
      upSamples,
      incidents: 3,
      activeIncidents: 1,
      mttrSeconds: 412.5,
      mtbfSeconds: spanSec / 3,
      budgetAllowedSec: allowed,
      budgetBurnedSec: spanSec * (1 - availability),
      target,
      approximatedBuckets: rangeSpanMs(range) > 48 * 3600_000,
      applicable: true,
    }
    return HttpResponse.json(body)
  }),

  // 容量预测（FR-464）：磁盘/堆给「有增长」结果，节点内存给「无增长不预测」负例。
  domainRoute('get', '/metrics/capacity/forecast', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const scope = url.searchParams.get('scope') ?? 'node'
    const targetId = url.searchParams.get('targetId') ?? ''
    if (!targetId) {
      return HttpResponse.json({ error: 'INVALID_SCOPE', message: 'scope 必须为 node 或 instance，且 targetId 非空' }, { status: 400 })
    }
    const wanted = (url.searchParams.get('metrics') ?? (scope === 'node' ? 'node_disk_used,node_mem_used' : 'inst_heap_used')).split(',').filter(Boolean)
    const range = url.searchParams.get('range') ?? '24h'
    const forecasts: CapacityForecastInfo['forecasts'] = wanted.map((metricKey) =>
      capacityForecastRow(scope, range, targetId, metricKey),
    )
    const body: CapacityForecastInfo = { forecasts }
    return HttpResponse.json(body)
  }),

  domainRoute('get', '/metrics/processes/top', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const now = new Date().toISOString()
    return HttpResponse.json([
      {
        instanceId: Number(new URL(info.request.url).searchParams.get('instanceId') ?? 1),
        instanceUuid: 'inst-1',
        nodeUuid: 'node-a',
        pid: 24512,
        name: 'java',
        cpuPercent: 42.5,
        rssBytes: 2.4 * GIB,
        readBytesPerSec: 1024 * 1024,
        writeBytesPerSec: 512 * 1024,
        user: 'minecraft',
        commandSummary: 'java -Xmx4G -jar server.jar',
        sampledAt: now,
      },
      {
        instanceId: Number(new URL(info.request.url).searchParams.get('instanceId') ?? 1),
        instanceUuid: 'inst-1',
        nodeUuid: 'node-a',
        pid: 24518,
        name: 'wrapper',
        cpuPercent: 3.1,
        rssBytes: 80 * MIB,
        readBytesPerSec: 64 * 1024,
        writeBytesPerSec: 16 * 1024,
        user: 'minecraft',
        commandSummary: 'jm-worker-daemon',
        sampledAt: now,
      },
    ])
  }),

  domainRoute('get', '/instances/:id/processes/:pid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number(info.params.id)
    const pid = Number(info.params.pid)
    const now = new Date().toISOString()
    return HttpResponse.json({
      instance: { id: instanceId, uuid: 'inst-1-uuid', name: 'survival', nodeId: 1, nodeUuid: 'node-a', nodeName: '北京节点' },
      rootPid: 24512,
      target: {
        pid,
        parentPid: pid === 24512 ? 0 : 24512,
        name: pid === 24512 ? 'java' : 'worker-helper',
        isRoot: pid === 24512,
        cpuPercent: 42.5,
        rssBytes: 2.4 * GIB,
        readBytesPerSec: 1024 * 1024,
        writeBytesPerSec: 512 * 1024,
        user: 'minecraft',
        commandSummary: pid === 24512 ? 'java -Xmx4G -jar server.jar' : 'worker-helper --child',
        uptimeSeconds: 3661,
        threadCount: 58,
        sampledAt: now,
        unavailableReason: '',
      },
      ancestors: pid === 24512 ? [] : [{
        pid: 24512,
        parentPid: 0,
        name: 'java',
        isRoot: true,
        cpuPercent: 42.5,
        rssBytes: 2.4 * GIB,
        readBytesPerSec: 1024 * 1024,
        writeBytesPerSec: 512 * 1024,
        user: 'minecraft',
        commandSummary: 'java -Xmx4G -jar server.jar',
        uptimeSeconds: 3661,
        threadCount: 58,
        sampledAt: now,
        unavailableReason: '',
      }],
      children: pid === 24512 ? [{
        pid: 24518,
        parentPid: 24512,
        name: 'wrapper',
        isRoot: false,
        cpuPercent: 3.1,
        rssBytes: 80 * MIB,
        readBytesPerSec: 64 * 1024,
        writeBytesPerSec: 16 * 1024,
        user: 'minecraft',
        commandSummary: 'jm-worker-daemon',
        uptimeSeconds: 3600,
        threadCount: 3,
        sampledAt: now,
        unavailableReason: '',
      }] : [],
      diagnostics: [{
        code: 'cpu_sustained_high',
        severity: 'warning',
        title: 'CPU 持续高占用',
        evidence: '最近窗口平均 CPU 42.5%',
        suggestion: '优先检查插件任务或脚本循环，再考虑处置子进程。',
      }],
      history: {
        windowSeconds: 1800,
        sampleCount: 12,
        latestSampledAt: now,
        rssDeltaBytes: 256 * MIB,
        avgCpuPercent: 40.2,
        avgWriteBytesPerSec: 512 * 1024,
      },
    })
  }),

  domainRoute('post', '/instances/:id/processes/:pid/actions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const body = await info.request.json().catch(() => ({})) as { action?: string; confirm?: boolean }
    if (url.searchParams.get('confirm') !== 'true' || body.confirm !== true) {
      return HttpResponse.json({ error: 'CONFIRM_REQUIRED', message: '破坏性操作需二次确认（confirm=true）' }, { status: 409 })
    }
    return HttpResponse.json({
      success: true,
      action: body.action ?? 'terminate',
      pid: Number(info.params.pid),
      affectedPids: [Number(info.params.pid)],
      message: body.action === 'kill_tree' ? '已终止受管子进程树' : '已终止受管子进程',
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
      // MC 直探可用性位与来源位（FR-446/447）：三源皆命中的满配种子，
      // 供前端渲染「不可用」语义与数据来源标注（1=探针 2=SLP 4=Query）。
      playersAvailable: true,
      motd: 'A Mock Server',
      motdAvailable: true,
      version: '1.20.4',
      versionAvailable: true,
      favicon: '',
      maxPlayers: 20,
      maxPlayersAvailable: true,
      playerNames: ['Steve', 'Alex'],
      playerNamesAvailable: true,
      playerNamesPartial: false,
      plugins: ['Paper', 'Vault'],
      pluginsAvailable: true,
      map: 'world',
      mapAvailable: true,
      slpAvailable: true,
      queryAvailable: true,
      sourceMask: 7,
    })
  }),

  // 实例环境（FR-344）：configured=自定义启动 env、runtime=运行时进程实际环境。
  domainRoute('get', '/instances/:id/env', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      configured: { PAPER_VELOCITY: 'true', MY_FLAG: '1' },
      runtime: {
        JAVA_HOME: '/opt/jdk-21',
        PATH: '/opt/jdk-21/bin:/usr/bin:/bin',
        PAPER_VELOCITY: 'true',
        MY_FLAG: '1',
        LANG: 'en_US.UTF-8',
      },
      runtimeAvailable: true,
      note: '',
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

  // ===== alerts 事件（FR-149/FR-085） =====
  domainRoute('get', '/alerts/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const level = url.searchParams.get('level')
    const triggerType = url.searchParams.get('triggerType')
    const resolvedParam = url.searchParams.get('resolved')
    const ackParam = url.searchParams.get('acknowledged')
    const ruleId = url.searchParams.get('ruleId')
    const keyword = url.searchParams.get('keyword')
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    const page = Math.max(1, Number(url.searchParams.get('page') ?? '1'))
    const pageSize = Math.max(1, Number(url.searchParams.get('pageSize') ?? '50'))

    let items = alertEvents.list()
    if (level) items = items.filter((e) => e.level === level)
    if (triggerType) items = items.filter((e) => e.triggerType === triggerType)
    if (resolvedParam != null) items = items.filter((e) => e.resolved === (resolvedParam === 'true'))
    if (ackParam != null) items = items.filter((e) => e.acknowledged === (ackParam === 'true'))
    if (ruleId) items = items.filter((e) => e.ruleId === Number(ruleId))
    if (keyword) items = items.filter((e) => e.message.includes(keyword))
    if (from) items = items.filter((e) => e.firedAt >= from)
    if (to) items = items.filter((e) => e.firedAt <= to)
    items = [...items].sort((a, b) => (a.firedAt < b.firedAt ? 1 : -1))

    const total = items.length
    const start = (page - 1) * pageSize
    return HttpResponse.json({ items: items.slice(start, start + pageSize), total })
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

  domainRoute('post', '/alerts/events/:id/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    alertEvents.update(Number(info.params.id), { read: true })
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
    const id = Number(info.params.id)
    const inUse = alertRules.list().some((rule) => parseChannelIds(rule.channelIds).includes(id))
    if (inUse) {
      return HttpResponse.json({ error: 'CHANNEL_IN_USE', message: '通道仍被告警规则引用' }, { status: 409 })
    }
    alertChannels.remove(id)
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

  // ===== 统一通知中心 feed（FR-216，见 ADR-048）：聚合 notifications + alertEvents =====
  domainRoute('get', '/notifications/feed', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const source = url.searchParams.get('source') ?? ''
    const onlyUnread = url.searchParams.get('unread') === 'true'
    const keyword = url.searchParams.get('keyword') ?? ''
    const page = Number(url.searchParams.get('page') ?? '1')
    const pageSize = Number(url.searchParams.get('pageSize') ?? '50')

    let merged = mergedFeed()
    if (source === 'message' || source === 'alert') merged = merged.filter((i) => i.source === source)
    if (onlyUnread) merged = merged.filter((i) => !i.read)
    if (keyword) merged = merged.filter((i) => i.title.includes(keyword) || i.body.includes(keyword))
    merged.sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))

    const total = merged.length
    const start = (page - 1) * pageSize
    return HttpResponse.json({ items: merged.slice(start, start + pageSize), total })
  }),

  domainRoute('get', '/notifications/feed/unread-count', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const unread =
      notifications.list((n) => !n.readAt).length + alertEvents.list((e) => !e.read).length
    return HttpResponse.json({ unread })
  }),

  domainRoute('post', '/notifications/feed/read-all', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    notifications.list().forEach((n) => notifications.update(n.id, { readAt: new Date().toISOString() }))
    alertEvents.list().forEach((e) => alertEvents.update(e.id, { read: true }))
    return HttpResponse.json({ updated: true })
  }),

  domainRoute('post', '/notifications/feed/:source/:id/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (info.params.source === 'message') {
      notifications.update(id, { readAt: new Date().toISOString() })
    } else if (info.params.source === 'alert') {
      alertEvents.update(id, { read: true })
    } else {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '非法通知来源' }, { status: 400 })
    }
    return HttpResponse.json({ message: '已标记已读' })
  }),

  // ===== tasks 任务中心（FR-183；FR-337 分页信封 {items,total,limit,offset}） =====
  domainRoute('get', '/tasks', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    // 窗口钳制与真后端一致（FR-337）：limit 缺省 100、钳制 [1,500]；offset 缺省 0、负值/非法归 0。
    const rawLimit = Number(url.searchParams.get('limit') ?? '100')
    const limit = Number.isFinite(rawLimit) && rawLimit > 0 ? Math.min(Math.trunc(rawLimit), 500) : 100
    const rawOffset = Number(url.searchParams.get('offset') ?? '0')
    const offset = Number.isFinite(rawOffset) && rawOffset > 0 ? Math.trunc(rawOffset) : 0
    const kind = url.searchParams.get('kind')
    const state = url.searchParams.get('state')
    const nodeId = url.searchParams.get('nodeId')
    const keyword = url.searchParams.get('keyword')
    const since = url.searchParams.get('since')
    let items = [...artifactMigrationTasks.list(), ...tasks.list()]
    if (kind) items = items.filter((t) => t.kind === kind)
    if (state) items = items.filter((t) => t.state === state)
    if (nodeId) items = items.filter((t) => t.nodeId === Number(nodeId))
    if (keyword) items = items.filter((t) => t.title.includes(keyword) || t.detail.includes(keyword))
    if (since) items = items.filter((t) => t.createdAt >= since)
    return HttpResponse.json({ items: items.slice(offset, offset + limit), total: items.length, limit, offset })
  }),

  domainRoute('get', '/tasks/:taskId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const taskId = String(info.params.taskId)
    const task = artifactMigrationTasks.find((t) => t.taskId === taskId)
      ?? tasks.find((t) => t.taskId === taskId)
    if (!task) return HttpResponse.json({ error: 'NOT_FOUND', message: '任务不存在' }, { status: 404 })
    return HttpResponse.json({ task, logs: taskLogs.list((l) => l.taskId === taskId) })
  }),

  // 强制停止（FR-227）：mock 直接置 canceled（演示强停结果）；终态 409。
  domainRoute('post', '/tasks/:taskId/cancel', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const taskId = String(info.params.taskId)
    const migrationTask = artifactMigrationTasks.find((t) => t.taskId === taskId)
    const task = migrationTask ?? tasks.find((t) => t.taskId === taskId)
    if (!task) return HttpResponse.json({ error: 'NOT_FOUND', message: '任务不存在' }, { status: 404 })
    if (task.state === 'succeeded' || task.state === 'failed' || task.state === 'canceled') {
      return HttpResponse.json({ error: 'ALREADY_TERMINAL', message: '任务已结束，无法停止' }, { status: 409 })
    }
    const collection = migrationTask ? artifactMigrationTasks : tasks
    collection.update(task.id, { state: 'canceled', cancelRequested: true })
    return HttpResponse.json({ message: '已请求停止' })
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
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    const page = Number(url.searchParams.get('page') ?? '1')
    const pageSize = Number(url.searchParams.get('pageSize') ?? '100')

    let items = logs.list()
    if (source) items = items.filter((l) => l.source === source)
    if (level) items = items.filter((l) => l.level === level)
    if (nodeId) items = items.filter((l) => l.nodeId === Number(nodeId))
    if (instanceId) items = items.filter((l) => l.instanceId === Number(instanceId))
    if (keyword) items = items.filter((l) => l.message.includes(keyword))
    if (from) items = items.filter((l) => l.time >= from)
    if (to) items = items.filter((l) => l.time <= to)

    // 对齐真后端 log.go 的 time DESC 稳定排序（真后端有序，mock 此前漏排造成「实时正序+历史倒序」假象）。
    items = [...items].sort((a, b) => (a.time < b.time ? 1 : a.time > b.time ? -1 : 0))

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
