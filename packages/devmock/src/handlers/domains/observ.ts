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
import { mockDataScaleProfile } from '@jianmanager/devmock/runtime-control'
import { nodeIdByUuid, nodeUuidById } from '@jianmanager/devmock/handlers/domains/node'

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
  /** 实例展示名（真后端已回填；仅 rule.targetType === 'instance' 时存在，实例被删则缺省）。 */
  instanceName?: string
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

/** QQ 已发现群（FR-495：对齐 web/src/api/alerts.ts QQDiscoveredGroup，camelCase JSON）。 */
interface QQDiscoveredGroup {
  id: number
  groupOpenid: string
  opMemberOpenid: string
  firstSeenAt: string
  lastSeenAt: string
  sourceAppId: string
}

/** QQ 扫码绑定任务（FR-494）：内存态，polls 记录被轮询次数以模拟「先 pending 后 completed」。 */
interface QQBindTask {
  id: number
  polls: number
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

/**
 * 任务从创建到完成的模拟时长（FR-502 阶段 6 补丁）。
 *
 * 【为什么必须让任务状态随时间推进】任务表的状态原先是一份**静态**随机值：种子里的
 * `running` 与批量生成里的 pending/running 都永不改变。而前端
 * `api/tasks.ts` 的 `tasksRefetchInterval` 是「**只要有非终态任务就轮询**」——
 * 于是轮询**永远**停不下来：实测静止无操作时，`tasks?limit=100` 与 `tasks?limit=5`
 * 每 2 秒各发一次请求，页面持续重渲染（6 秒内 37 请求 / 132 次 DOM 变更）。
 *
 * 真实后端里任务必然走向终态，因此这里按「创建时刻 + 本时长」推导当前状态，
 * 到期即 succeeded，轮询自然停止。
 *
 * 注意与实例的处置**不同**：实例有 SSE 推送（`api/events.ts`），所以那边是把轮询
 * 降级成兜底；任务没有推送通道，轮询是它唯一的刷新手段，不能删——只能让数据会收敛。
 */
const TASK_MOCK_DURATION_MS = 90_000

/** 按经过时间推导任务的当前状态；已处于终态的任务原样返回。 */
function advanceTaskState(task: Task, now: number): Task['state'] {
  if (task.state !== 'pending' && task.state !== 'running') return task.state
  const created = new Date(task.createdAt).getTime()
  if (!Number.isFinite(created)) return task.state
  const elapsed = now - created
  if (elapsed < 0) return task.state
  if (elapsed >= TASK_MOCK_DURATION_MS) return 'succeeded'
  return elapsed >= TASK_MOCK_DURATION_MS * 0.2 ? 'running' : 'pending'
}

const TASK_KIND_POOL = ['jdk_install', 'instance_backup', 'runtime_install', 'client_publish', 'node_repair'] as const
/**
 * 历史批量任务的状态池：**只含终态**。
 *
 * 【为什么不放 pending/running】真后端里一年前的任务必然已收敛，读作 running 是假象。
 * 更关键的是前端 `tasksRefetchInterval` 是「只要有一条非终态就一直 2s 轮询」——旧实现按
 * 5 态轮转生成，large 档 1500 条里就有 600 条被种成非终态。`advanceTaskState` 虽能按
 * 时间把它们推导回 succeeded，但代价是**每个** `/tasks` 请求都要遍历全量重算一遍，
 * 而 MSW 跑在浏览器主线程上，等于把这份成本转嫁给了页面。
 *
 * 活跃态只留给「近期」的少数任务（见 RECENT_ACTIVE_TASK_SLOTS），它们在 90s 内自然收敛。
 * 分布偏重 succeeded，贴合任务中心「绝大多数已成功、失败/取消是少数」的真实观感。
 * 池长 6 与 5 元的 TASK_KIND_POOL 错开周期，避免 kind 与 state 绑死（同一类任务永远同状态）。
 */
const TASK_TERMINAL_POOL: Task['state'][] = ['succeeded', 'succeeded', 'failed', 'succeeded', 'canceled', 'succeeded']
/** 近窗口内保留非终态的批量任务条数：够 UI 看到「有任务在跑」，且 90s 内全部收敛。 */
const RECENT_ACTIVE_TASK_SLOTS = 3
const LOG_LEVEL_POOL = ['debug', 'info', 'warn', 'error'] as const
const LOG_SOURCE_POOL = ['instance', 'control_plane', 'worker'] as const
const NOTIFICATION_LEVEL_POOL: Notification['level'][] = ['info', 'success', 'warning', 'error']

// ── 集合声明 + 种子（import 即播种，resetDb 重播；唯一声明处） ──

/*
  进程清单要按「该节点的真实实例」生成，故直接读同一份 instances 命名空间。
  种子由 instance.ts 首次访问时写入，这里用惰性默认值保持两侧同源。
*/
const instanceDb = db<{ id: number; uuid: string; nodeId: number; name: string }>('instances', () => [])

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

/** QQ 已发现群集合（FR-495：两条假群，支撑表单下拉与接入视图 DOM 回归）。 */
const qqDiscoveredGroups = db<QQDiscoveredGroup>('qqDiscoveredGroups', () => [
  {
    id: 1,
    groupOpenid: 'GROUP_OPENID_DEMO_1',
    opMemberOpenid: 'OP_MEMBER_DEMO_1',
    firstSeenAt: iso(-172800000),
    lastSeenAt: iso(-3600000),
    sourceAppId: '102000001',
  },
  {
    id: 2,
    groupOpenid: 'GROUP_OPENID_DEMO_2',
    opMemberOpenid: 'OP_MEMBER_DEMO_2',
    firstSeenAt: iso(-86400000),
    lastSeenAt: iso(-600000),
    sourceAppId: '102000001',
  },
])

/** 扫码绑定任务的假 appId / 目标 openid / 密钥引用名（DEMO 值，绝不用真实标识）。 */
const QQ_DEMO_APP_ID = '102000001'
const QQ_DEMO_USER_OPENID = 'USER_OPENID_DEMO_1'
/** 与后端 `secretEnv` 同形状：`${QQ-<appId>}`（连字符是后端放宽字符集的原因）。 */
const QQ_DEMO_SECRET_ENV = '${QQ-102000001}'
/** 同一 taskId 前 N 次轮询返回 pending，第 N+1 次起返回 completed。 */
const QQ_BIND_PENDING_POLLS = 1
/** taskId 形状：由集合自增 id 派生（resetDb 后首个任务恒为 1，测试可复现）。 */
const qqBindTaskId = (id: number) => `qq-bind-demo-${id}`
/** 扫码绑定任务集合（空种子：每次测试从零开始，taskId 由自增 id 派生，可复现）。 */
const qqBindTasks = db<QQBindTask>('qqBindTasks', () => [])

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

  // 生成规模在**播种时**读运行时档位（FR-502 阶段 6 补丁）：模块级常量会被 import 时捕获，
  // 调试面板改档位后重播种也无效。循环外取一次，别在循环条件里每次都读。
  const { feedCount, instanceCount } = mockDataScaleProfile()
  const generated: AlertEvent[] = []
  for (let i = 3; i <= feedCount + 2; i++) {
    const critical = i % 9 === 0
    const offset = -Math.floor(((i - 2) / feedCount) * YEAR_MS)
    generated.push({
      id: i,
      ruleId: critical ? 2 : 1,
      targetId: ((i - 1) % instanceCount) + 1,
      level: critical ? 'critical' : 'warn',
      triggerType: critical ? 'instance_crash' : 'metric',
      value: critical ? 0 : 75 + (i % 25),
      message: critical
        ? `实例 server-${String(((i - 1) % instanceCount) + 1).padStart(4, '0')} 异常退出`
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

  // 同上：规模取运行时档位（FR-502 阶段 6 补丁）
  const { feedCount, instanceCount } = mockDataScaleProfile()
  const generated: Notification[] = []
  for (let i = 4; i <= feedCount + 3; i++) {
    const offset = -Math.floor(((i - 3) / feedCount) * YEAR_MS)
    const level = NOTIFICATION_LEVEL_POOL[i % NOTIFICATION_LEVEL_POOL.length]
    generated.push({
      id: i,
      userId: 1,
      level,
      title: `批量运维事件 ${i - 3}`,
      body: `server-${String(((i - 1) % instanceCount) + 1).padStart(4, '0')} 的 ${TASK_KIND_POOL[i % TASK_KIND_POOL.length]} 已记录`,
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
      // 刻意留在「尚未到期」的窗口内：页面加载后约 60 秒内它仍是 running，让 mock 有
      // 真实的活跃任务可看；到点自动收敛为 succeeded，`tasksRefetchInterval` 随之返回 false。
      createdAt: iso(-30_000),
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

  // 同上：任务规模取运行时档位（FR-502 阶段 6 补丁）
  const { taskCount, instanceCount } = mockDataScaleProfile()
  const generated: Task[] = []
  for (let i = 4; i <= taskCount + 3; i++) {
    const seq = i - 3
    // 开头 RECENT_ACTIVE_TASK_SLOTS 条留在 90s 活跃窗口内（createdAt 落在 15s~45s 前），
    // 其余历史任务一律终态——理由见 TASK_TERMINAL_POOL 的说明。
    // 放「开头」而不是末尾：生成序与 createdAt 同向（seq 越小越新），所以开头就是最新的
    // 几条，能被 /tasks 首窗（limit≤100）取到，任务中心与顶栏看板都看得到活跃态。
    const isRecent = seq <= RECENT_ACTIVE_TASK_SLOTS
    const state: Task['state'] = isRecent
      ? seq % 2 === 0
        ? 'running'
        : 'pending'
      : TASK_TERMINAL_POOL[i % TASK_TERMINAL_POOL.length]
    const kind = TASK_KIND_POOL[i % TASK_KIND_POOL.length]
    const offset = isRecent ? -(15_000 * seq) : -Math.floor((seq / taskCount) * YEAR_MS)
    const progress = state === 'succeeded' ? 100 : state === 'pending' ? 0 : 10 + (i % 85)
    generated.push({
      // 生成段与上面的静态种子共用同一个 id 空间：静态种子已占到 4，而循环从 i=4 起，
      // 直接写 `id: i` 会让第一条批量任务与静态种子的 id 4 重复（实测 /tasks 返回
      // [1,2,3,4,4]）。前端按 id 做列表 key，重复会触发 React key 警告并可能错渲染。
      // 真后端只有一个自增序列，故这里把生成段整体后移，保持全局唯一。
      id: i + 3,
      taskId: `task-scale-${i}`,
      nodeId: i % 2 === 0 ? 2 : 1,
      kind,
      state,
      progress,
      title: `${kind} 批量任务 ${i - 3}`,
      detail: `server-${String(((i - 1) % instanceCount) + 1).padStart(4, '0')}`,
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

  // 同上：日志规模取运行时档位（FR-502 阶段 6 补丁）
  const { logCount, instanceCount } = mockDataScaleProfile()
  const generated: LogRow[] = []
  for (let i = 5; i <= logCount + 4; i++) {
    const instanceId = ((i - 1) % instanceCount) + 1
    const source = LOG_SOURCE_POOL[i % LOG_SOURCE_POOL.length]
    const level = LOG_LEVEL_POOL[i % LOG_LEVEL_POOL.length]
    const offset = -Math.floor(((i - 4) / logCount) * YEAR_MS)
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
  /*
    每台实例一个 seed：相位与振幅都随它变化。
    没有 seed 时所有实例的曲线逐点相同——多条线在图上完全重叠，
    看起来就像「只画了一条浅线」甚至「空白图」，调试时极易误判成图表坏了。
  */
  seed = 0,
): { ts: string; avg: number; min: number; max: number }[] {
  const out: { ts: string; avg: number; min: number; max: number }[] = []
  const phase = seed * 0.9
  const ampScale = 0.55 + (seed % 5) * 0.12
  for (let i = count - 1; i >= 0; i--) {
    const v = base + amp * ampScale * (0.5 + 0.5 * Math.sin(i / 3 + phase))
    out.push({
      ts: iso(-i * stepMs),
      avg: Number(v.toFixed(2)),
      min: Number((v * 0.92).toFixed(2)),
      max: Number((v * 1.08).toFixed(2)),
    })
  }
  return out
}

/**
 * 自定义窗口（from/to）下的选档，镜像真后端 `selectResolution`：
 * 跨度 ≤6h 走原始 1m；≤30d 走 5m；更长走 1h。点数封顶 1000，避免假后端生成上万点。
 *
 * 【为什么假后端也要认 from/to】节点监控页的时间窗支持自定义起止；
 * 假后端若只认 range，这个功能在 dev / e2e 下永远看不出效果，成了无法验证的死角。
 */
function windowPlan(fromMs: number, toMs: number): { count: number; step: number } {
  const spanMs = Math.max(60_000, toMs - fromMs)
  const step = spanMs <= 6 * 3_600_000 ? 60_000 : spanMs <= 30 * 24 * 3_600_000 ? 5 * 60_000 : 3_600_000
  return { count: Math.max(2, Math.min(3000, Math.round(spanMs / step))), step }
}

/** 由步长反推档位名，供响应体 `resolution` 使用（真后端按档位回填该字段）。 */
function resolutionOf(stepMs: number): string {
  if (stepMs <= 60_000) return 'raw'
  return stepMs <= 5 * 60_000 ? '5m' : '1h'
}

/** 把 range 字符串映射为（点数, 步长 ms）。覆盖前端 RangePicker 常见档。 */
/*
  步长必须与上报的 `resolution` 严格对应（raw=60s / 5m=300s / 1h=3600s）。

  原先各档用的是 15m、4h、12h、1d 这类「方便凑整」的步长，却又固定上报 `resolution:'raw'`。
  前端据此积分「区间总消耗」（速率 × 桶宽）时会按 60s 去乘 15m 的点，算出只有真值 1/15 的
  假数字——假数据自相矛盾比缺数据更坏，排查时会以为积分实现有问题。
*/
function rangePlan(range: string): { count: number; step: number } {
  switch (range) {
    case '1h':
      return { count: 60, step: 60_000 }
    case '6h':
      return { count: 360, step: 60_000 }
    case '7d':
      return { count: 2016, step: 5 * 60_000 }
    case '30d':
      // 真后端此档是 5m；假后端取 1h 只为压点数（档位与步长仍自洽）。
      return { count: 720, step: 3600_000 }
    case '90d':
      return { count: 2160, step: 3600_000 }
    case '1y':
      return { count: 8760, step: 3600_000 }
    case '24h':
    default:
      return { count: 288, step: 5 * 60_000 }
  }
}

/**
 * 把 raw 点按 `bucketMs` 合并成预聚合桶（同桶求均值，与后端 rollup 的 `avg` 列同义）。
 *
 * `stepMs` 是原始采样间隔：真实后端读的是 rollup 表，而 mock 没有那张表，
 * 只能就地聚合；两者对「桶内均值」的口径一致，因此前端看到的行为相同。
 * 缺测（avg 为 null）的点不参与均值；整桶都缺测时输出 null，保持「缺测不是 0」的语义。
 */
function bucketPoints<T extends { avg: number | null }>(
  points: T[],
  bucketMs: number,
  stepMs: number,
): T[] {
  if (bucketMs <= stepMs || points.length === 0) return points
  const perBucket = Math.max(1, Math.round(bucketMs / stepMs))
  const out: T[] = []
  for (let i = 0; i < points.length; i += perBucket) {
    const slice = points.slice(i, i + perBucket)
    const values = slice.map((p) => p.avg).filter((v): v is number => v != null)
    const avg = values.length > 0 ? values.reduce((a, b) => a + b, 0) / values.length : null
    const first = slice[0]
    if (first) out.push({ ...first, avg })
  }
  return out
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
  const plan = rangePlan(range)
  const all = scope === 'instance' ? instanceSeries(plan) : nodeSeries(plan)
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
function nodeSeries(plan: { count: number; step: number }) {
  const { count, step } = plan
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
function instanceSeries(plan: { count: number; step: number }, seed = 0) {
  const { count, step } = plan
  const mk = (key: string, unit: string, base: number, amp: number, world = '') => ({
    metricKey: key,
    unit,
    world,
    points: makeSeriesPoints(count, step, base, amp, seed),
  })
  return [
    mk('inst_tps', '', 19.5, 0.5),
    mk('inst_mspt', 'ms', 30, 20),
    mk('inst_heap_used', 'bytes', 2 * GIB, 1 * GIB),
    mk('inst_heap_max', 'bytes', 4 * GIB, 0),
    mk('inst_threads', '', 80, 20),
    mk('inst_players_online', '', 12, 8),
    // 在线人数上限（FR-511）：假数据固定 50，供「当前/最大」显示。
    mk('inst_players_max', '', 50, 0),
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
    const nodes = db<ObsNode>('nodes').list((n) => !n.deletedAt)
    const insts = db<ObsInstance>('instances').list()
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
    const nodes = db<ObsNode>('nodes').list((n) => !n.deletedAt)
    const insts = db<ObsInstance>('instances').list()
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
    /*
      totals 必须与 `/observability/overview` 的 health 同源。

      此前这里是写死的 `nodeCount: 2 / onlineNodeCount: 2 / runningInstances: 720`，
      而平台健康那侧是按 db('nodes') + db('instances') 真算的（1/2 在线、242 运行中）。
      结果同一屏上「运行实例」同时显示 720 和 242、「节点」同时显示 2/2 和 1/2——
      运营看到两个互相打架的数字，会直接不信这一页。

      真实后端 `/metrics/overview` 是据 Node/Instance 当前值汇总的
      （见 service/metric.go 的 overviewTotals），故 mock 也照同一口径派生，
      而不是另写一份假数。
    */
    const nodes = db<ObsNode>('nodes').list((n) => !n.deletedAt)
    const insts = db<ObsInstance>('instances').list()
    const onlineNodes = nodes.filter((n) => n.status === 1)
    const runningInstances = insts.filter((i) => i.status === 'RUNNING').length
    return HttpResponse.json({
      totals: {
        nodeCount: nodes.length,
        onlineNodeCount: onlineNodes.length,
        runningInstances,
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
        // Bot Worker 共享进程两条：后端 `bot_runtime_metric_sampler` 会写入，首页趋势默认隐藏、勾选可见。
        // 量级取「百 MB 级 RSS / 十几个百分点 CPU」，与真机读数同阶，便于验证「隐藏项不压扁主线」。
        { metricKey: 'bot_worker_rss_bytes', unit: 'bytes', points: makeSeriesPoints(count, step, 96 * MIB, 8 * MIB) },
        { metricKey: 'bot_worker_cpu_pct', unit: '%', points: makeSeriesPoints(count, step, 12, 6) },
      ],
    })
  }),

  domainRoute('get', '/metrics/series', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const scope = url.searchParams.get('scope') ?? 'node'
    const range = url.searchParams.get('range') ?? '24h'
    /*
      自定义窗口优先于 range（与真后端 parseMetricRange 的优先级一致）：
      节点监控页填了起止时间后，曲线与「本范围消耗」都必须跟着这个窗口走。
    */
    const fromMs = Number(url.searchParams.get('from') ? Date.parse(url.searchParams.get('from')!) : Number.NaN)
    const toMs = Number(url.searchParams.get('to') ? Date.parse(url.searchParams.get('to')!) : Number.NaN)
    const plan =
      Number.isFinite(fromMs) && Number.isFinite(toMs) && toMs > fromMs
        ? windowPlan(fromMs, toMs)
        : rangePlan(range)
    const all = scope === 'instance' ? instanceSeries(plan) : nodeSeries(plan)
    const wanted = url.searchParams.get('metrics')
    const series = wanted
      ? all.filter((s) => wanted.split(',').includes(s.metricKey))
      : all
    const span = (series[0]?.points.length ?? 0) * plan.step
    return HttpResponse.json({
      resolution: resolutionOf(plan.step),
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
    const resolution = body.resolution ?? 'auto'
    const { step } = rangePlan(range)
    /*
      【必须真的按 resolution 分档】后端 `queryPoints` 三档分别读 raw / rollup5m / rollup1h，
      桶数 = 区间 ÷ 粒度。此前 devmock 只按 range 出点、完全忽略 resolution，
      于是「1h + 5m」和「1h + raw」返回一样多的点——前端据此判断减小载荷的改动**在 mock 下
      根本测不出来**（实测两者都是 575KB，是假象）。
      这里只实现 5m 档（rollup5m）的等价语义：把同桶内的点求均值合并；1h 档同理放大桶宽。
    */
    const bucketMs = resolution === '1h' ? 3600_000 : resolution === '5m' ? 5 * 60_000 : 0
    const series: Record<string, ReturnType<typeof instanceSeries>> = {}
    let span = 0
    for (const [index, id] of targetIds.entries()) {
      // 逐目标不同 seed：同一批里 12 条线各走各的，图上才看得出「谁快谁慢」。
      const all = instanceSeries(rangePlan(range), index + 1)
      const filtered = wanted?.length ? all.filter((s) => wanted.includes(s.metricKey)) : all
      series[id] = bucketMs > 0
        ? filtered.map((s) => ({ ...s, points: bucketPoints(s.points, bucketMs, step) }))
        : filtered
      span = Math.max(span, (filtered[0]?.points.length ?? 0) * step)
    }
    return HttpResponse.json({
      resolution: bucketMs > 0 ? resolution : 'raw',
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
    /*
      玩家在线曲线单独生成，**不复用 makeSeriesPoints**。

      后者用 `sin(i / 3)` 造周期：每 19 个点就一个完整正弦周期，96 点会画出 5 个
      规整的波峰——看起来像示波器信号，而不是玩家在线数。真实玩家数随昼夜节律
      单峰起伏（晚上高、凌晨低）叠加随机抖动，故这里按「时间戳 → 当地小时」造曲线：
      以 20 点为峰、6 点为谷，按时间跨度铺满一个昼夜周期。
    */
    const nowMs = Date.now()
    const trend = Array.from({ length: count }, (_, idx) => {
      const tMs = nowMs - (count - 1 - idx) * step
      const hour = new Date(tMs).getUTCHours() + new Date(tMs).getUTCMinutes() / 60
      // 昼夜节律：20 点峰、6 点谷（cos 在 hour=20 取 +1，hour=8 取 -1）。
      const diurnal = Math.cos(((hour - 20) / 24) * 2 * Math.PI)
      // 固定种子的伪随机抖动，保证同一请求内可复现、不同时段不呆板。
      const jitter = Math.sin(idx * 12.9898) * 0.35
      const v = 220 + 160 * diurnal * 0.9 + 30 * jitter
      const avg = Math.max(10, Number(v.toFixed(2)))
      return {
        ts: new Date(tMs).toISOString(),
        avg,
        min: Number((avg * 0.92).toFixed(2)),
        max: Number((avg * 1.08).toFixed(2)),
      }
    })
    const shift = tzHourOffset(tz)
    const dist = Array.from({ length: 24 }, (_, h) => {
      const local = (h + shift + 24) % 24
      // 双峰：晚间 20 点与午后 14 点高峰。
      return Number((120 + 90 * Math.exp(-((local - 20) ** 2) / 8) + 45 * Math.exp(-((local - 14) ** 2) / 12)).toFixed(1))
    })
    /*
      peakValue 必须与 `trend` 同源。

      此前它取的是 **hourlyDist（24 时段分布）的峰值**，而曲线画的是 `trend` 序列——
      两个不同来源，数值自然对不上（实测标签显示峰值 212，曲线却冲到 380）。
      前端于是出现「标签低于曲线最高点」的自相矛盾读数。
      真实后端同样以曲线序列为准，故这里也按 `trend` 求峰值与日均。
    */
    const trendVals = trend.map((p) => p.avg).filter((v) => typeof v === 'number')
    const peak = trendVals.length ? Math.max(...trendVals) : 0
    const body: PlayerTrendInfo = {
      resolution: '5m',
      timezone: tz,
      trend,
      hourlyDist: dist,
      peakValue: Number(peak.toFixed(1)),
      peakAt: iso(-3600_000),
      dailyAvg: Number((trendVals.reduce((a, b) => a + b, 0) / (trendVals.length || 1)).toFixed(1)),
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
    const url = new URL(info.request.url)
    const now = new Date().toISOString()
    /*
      返回**该节点真实实例**的进程，而不是写死 instanceId=1。
      写死时前端按 instanceId 映射实例名会全部落到 `#1`（实测：进程行显示「#1」），
      看着像归属解析坏了——其实是假数据与实例清单对不上。
      每台实例给两个进程（游戏服本体 + 包装器），与真实探针的层级一致。
    */
    /*
      前端的 nodeId 是**节点 UUID**（与真后端 `ProcessTopQuery.NodeUUID` 同口径），
      不是数字 id：这里必须先把 UUID 翻成 id 再过滤实例，否则过滤条件恒假/恒真，
      进程行就会挂到别的节点的实例上（实测出现过「#11」这种本节点不存在的实例号）。
    */
    const nodeUuidParam = url.searchParams.get('nodeId')
    const onlyInstance = Number(url.searchParams.get('instanceId') ?? Number.NaN)
    const nodeIdParam = nodeUuidParam ? (nodeIdByUuid(nodeUuidParam) ?? Number.NaN) : Number.NaN
    /*
      口径与前端 `useInstanceSearch({nodeId, sort:'name'})` 对齐：按名称排序取前 12 台。
      若这里改用无序前 12 台，本节点实例一多（假后端 node-alpha 有 600+ 台），
      进程行就会挂到「实例清单里查不到名字」的实例上，看起来像归属解析坏了。
    */
    const rows = instanceDb
      .list((i) =>
        Number.isFinite(onlyInstance)
          ? i.id === onlyInstance
          : Number.isFinite(nodeIdParam)
            ? i.nodeId === nodeIdParam
            : true,
      )
      .sort((a, b) => a.name.localeCompare(b.name))
      .slice(0, 12)
    const out: Record<string, unknown>[] = []
    for (const [idx, inst] of rows.entries()) {
      const basePid = 24000 + idx * 100
      out.push(
        {
          instanceId: inst.id,
          instanceUuid: inst.uuid,
          nodeUuid: nodeUuidParam ?? 'node-alpha',
          pid: basePid + 12,
          name: 'java',
          cpuPercent: Number((8 + ((inst.id * 7) % 40)).toFixed(1)),
          rssBytes: (1.2 + ((inst.id % 5) * 0.4)) * GIB,
          readBytesPerSec: 256 * 1024 + (inst.id % 4) * 128 * 1024,
          writeBytesPerSec: 96 * 1024 + (inst.id % 3) * 32 * 1024,
          user: 'minecraft',
          commandSummary: `java -Xmx4G -jar ${inst.name}.jar nogui`,
          sampledAt: now,
        },
        {
          instanceId: inst.id,
          instanceUuid: inst.uuid,
          nodeUuid: nodeUuidParam ?? 'node-alpha',
          pid: basePid + 18,
          name: 'wrapper',
          cpuPercent: Number((0.8 + (inst.id % 3) * 0.4).toFixed(1)),
          rssBytes: 72 * MIB + (inst.id % 4) * 8 * MIB,
          readBytesPerSec: 48 * 1024,
          writeBytesPerSec: 12 * 1024,
          user: 'minecraft',
          commandSummary: 'jm-worker-daemon',
          sampledAt: now,
        },
      )
    }
    // 真后端按 sort 排序后截断；假后端只在内存里排序，保证「按内存」切换看得出效果。
    const sortKey = url.searchParams.get('sort') ?? 'cpu'
    const limit = Math.min(Number(url.searchParams.get('limit') ?? 100) || 100, 200)
    out.sort((a, b) => {
      const pick = (r: Record<string, unknown>) =>
        sortKey === 'memory' ? Number(r.rssBytes) : sortKey === 'io' ? Number(r.readBytesPerSec) + Number(r.writeBytesPerSec) : Number(r.cpuPercent)
      return pick(b) - pick(a)
    })
    return HttpResponse.json(out.slice(0, limit))
  }),

  domainRoute('get', '/instances/:id/processes/:pid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number(info.params.id)
    const pid = Number(info.params.pid)
    const now = new Date().toISOString()
    /*
      详情要回显「所点的那个进程」，不能写死一组不存在的 PID/名称。
      写死时列表行是 java、弹窗里却写着 worker-helper，看着像归属校验出错。
      这里按列表同一套编号约定反推：+12 是游戏服本体（根），+18 是包装器（子进程）。
    */
    const rootPid = pid - (pid % 100) + 12
    const isRoot = pid === rootPid
    const inst = instanceDb.list((i) => i.id === instanceId)[0]
    const nodeUuid = inst ? (nodeUuidById(inst.nodeId) ?? 'node-alpha') : 'node-alpha'
    const baseProc = {
      pid: rootPid,
      parentPid: 0,
      name: 'java',
      isRoot: true,
      cpuPercent: 42.5,
      rssBytes: 1.6 * GIB,
      readBytesPerSec: 384 * 1024,
      writeBytesPerSec: 128 * 1024,
      user: 'minecraft',
      commandSummary: `java -Xmx4G -jar ${inst?.name ?? 'server'}.jar nogui`,
      uptimeSeconds: 3689,
      threadCount: 58,
      sampledAt: now,
      unavailableReason: '',
    }
    const childProc = {
      pid: rootPid + 6,
      parentPid: rootPid,
      name: 'wrapper',
      isRoot: false,
      cpuPercent: 1.2,
      rssBytes: 72 * MIB,
      readBytesPerSec: 48 * 1024,
      writeBytesPerSec: 12 * 1024,
      user: 'minecraft',
      commandSummary: 'jm-worker-daemon',
      uptimeSeconds: 3600,
      threadCount: 3,
      sampledAt: now,
      unavailableReason: '',
    }
    const target = isRoot ? baseProc : { ...childProc, pid }
    return HttpResponse.json({
      instance: {
        id: instanceId,
        uuid: inst?.uuid ?? '',
        name: inst?.name ?? '',
        nodeId: inst?.nodeId ?? 0,
        nodeUuid,
        nodeName: nodeUuid,
      },
      rootPid,
      target,
      ancestors: isRoot ? [] : [baseProc],
      children: isRoot ? [childProc] : [],
      diagnostics: isRoot
        ? [{
            code: 'cpu_sustained_high',
            severity: 'warning',
            title: 'CPU 持续高占用',
            evidence: '最近窗口平均 CPU 42.5%',
            suggestion: '优先检查插件任务或脚本循环，再考虑处置子进程。',
          }]
        : [],
      history: {
        windowSeconds: 900,
        sampleCount: 30,
        latestSampledAt: now,
        rssDeltaBytes: 64 * MIB,
        avgCpuPercent: isRoot ? 40.2 : 1.0,
        avgWriteBytesPerSec: isRoot ? 512 * 1024 : 12 * 1024,
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
    const pageItems = items.slice(start, start + pageSize)

    // 回填实例名（对齐真后端 AlertEventView.instanceName）。**必须按规则的 targetType 判定**：
    // 事件的 targetId 含义随规则维度而变，node 维度的值不是实例 id，混用会查到错误的实例名。
    const ruleTargetType = new Map<number, string>()
    for (const r of alertRules.list()) ruleTargetType.set(r.id, r.targetType)
    const names = new Map<number, string>()
    for (const inst of db<{ id: number; name: string }>('instances').list()) names.set(inst.id, inst.name)

    return HttpResponse.json({
      items: pageItems.map((e) =>
        ruleTargetType.get(e.ruleId) === 'instance' ? { ...e, instanceName: names.get(e.targetId) } : e,
      ),
      total,
    })
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

  // ===== QQ 扫码接入与已发现群（FR-495） =====
  // 响应均不含 appSecret；三端点与通道 CRUD 一致挂 protected 分组（requireAuth 首行）。
  // 响应形状必须是分页信封 {items,total}（照后端 QQGroups / docs/API.md）：返回纯数组会让
  // 前端的双兼容归一化只走到数组分支，信封分支失去覆盖（真实后端契约回归测不出来）。
  domainRoute('get', '/alerts/qq/groups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const items = qqDiscoveredGroups.list()
    return HttpResponse.json({ items, total: items.length })
  }),

  domainRoute('post', '/alerts/qq/share-link', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      url: 'https://qun.qq.com/qunpro/robot/qunshare?robot_appid=102000001&callback_data=jm%3Ademo',
    })
  }),

  // ===== QQ 扫码绑定任务（FR-494 接入体验改造） =====
  // 契约（internal/controlplane/router/alert.go）：
  //   POST /alerts/qq/bind-task         → {taskId, qrUrl}
  //   GET  /alerts/qq/bind-task/:taskId → {status}；仅 completed 追加 {appId, userOpenid, secretEnv}
  // 响应**绝不含 appSecret 明文**（与后端一致的密钥安全验收）；前端只把 secretEnv 填进表单。
  // 轮询节奏：同一 taskId 首次返回 pending、之后返回 completed——让 DOM 回归能覆盖
  // 「等待扫码 → 自动回填」的完整过渡，而不是一上来就完成（那样轮询分支等于没测）。
  domainRoute('post', '/alerts/qq/bind-task', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const row = qqBindTasks.insert({ polls: 0 })
    const taskId = qqBindTaskId(row.id)
    return HttpResponse.json({
      taskId,
      qrUrl: `https://q.qq.com/qqbot/openclaw/connect.html?task_id=${taskId}&source=JianManager&_wv=2`,
    })
  }),

  domainRoute('get', '/alerts/qq/bind-task/:taskId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const taskId = String(info.params.taskId)
    const row = qqBindTasks.find((t) => qqBindTaskId(t.id) === taskId)
    // 未知 taskId = 本地密钥已过期（后端 404 BIND_TASK_EXPIRED 语义），前端据此重新申请。
    if (!row) {
      return HttpResponse.json(
        { error: 'BIND_TASK_EXPIRED', message: '绑定任务已过期，请重新扫码' },
        { status: 404 },
      )
    }
    row.polls += 1
    if (row.polls <= QQ_BIND_PENDING_POLLS) return HttpResponse.json({ status: 'pending' })
    return HttpResponse.json({
      status: 'completed',
      appId: QQ_DEMO_APP_ID,
      userOpenid: QQ_DEMO_USER_OPENID,
      secretEnv: QQ_DEMO_SECRET_ENV,
    })
  }),

  domainRoute('get', '/alerts/qq/gateway/status', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      appId: '102000001',
      status: 'connected',
      lastEventAt: new Date().toISOString(),
      lastError: '',
    })
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
    // 先按经过时间推进活跃任务的状态，再过滤：否则 `state=running` 这类筛选会依据
    // 那份静态旧值，与真正返回给前端的状态自相矛盾。
    const now = Date.now()
    // 两个集合各自从 1 分配 id，直接并起来会出现重复 id（实测 `/tasks?limit=5`
    // 返回 [1,2,3,4,4]）：前端按 id 做列表 key，重复会触发
    // "Encountered two children with the same key" 并可能错渲染。
    // 真后端只有一个自增序列，故这里把迁移任务整体偏移到一个不会与普通任务相撞的分段。
    // （取 100000 是因为任务量级远小于它，且不与历史 ID 冲突。）
    const ARTIFACT_TASK_ID_BASE = 100_000
    let items = [
      ...artifactMigrationTasks.list().map((t) => ({ ...t, id: ARTIFACT_TASK_ID_BASE + t.id })),
      ...tasks.list(),
    ].map((t) => {
      const state = advanceTaskState(t, now)
      if (state === t.state) return t
      return { ...t, state, progress: state === 'succeeded' ? 100 : t.progress, updatedAt: new Date(now).toISOString() }
    })
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
