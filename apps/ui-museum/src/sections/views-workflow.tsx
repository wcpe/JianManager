/**
 * 博物馆「业务视图 · 任务与工作流」分区：18 个受控复合视图（@jianmanager/ui · components/views）。
 *
 * 它们共享同一套受控契约（ADR-097 a/b 范式）：
 * - **不取数**：数据由外壳（主控台容器）取好后经 props 注入；这里给的是有意义的静态样例；
 * - **不碰路由**：跳转走 `renderLink` / `renderHealthWall` / `renderInstancePicker` 等插槽，
 *   或只回调 `onOpenSession` / `onNavigate` / `onImported`；缺省插槽时组件退化为原生 `<a href>`；
 * - **不发请求、不弹 toast**：写动作以回调上报（部分回传 Promise 供组件决定是否关窗/复位），
 *   成功与失败文案由外壳决定；
 * - **状态归属**：决定取数的状态（查询键、筛选、页码、展开目标）归外壳持有；纯 UI 状态
 *   （弹窗开合、勾选集合、卡片/列表视图切换、虚拟滚动窗口）留在组件内。
 *
 * 弹窗类视图（BotLoadWizard / TemplateDialog / ImportServerWizardView / 两个搭建对话框）在
 * `open=false` 时不产生 DOM（Radix Dialog 不挂载内容），故沿用博物馆既有惯例给一个开场按钮：
 * 点开后渲染的是真实组件本体，而不是静态截图。
 */
import { useState } from 'react'
import { Button, Panel } from '@jianmanager/ui'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import type { AlertChannelInfo, AlertEventInfo, AlertRuleInfo } from '@jianmanager/ui/lib/alert-contracts'
import type {
  BotLoadFailure,
  BotLoadMetricPoint,
  BotLoadNodeCapacity,
  BotLoadRunBot,
  BotLoadRunEvent,
  BotLoadRunV2,
  BotLoadTemplate,
  BotLoadThresholds,
} from '@jianmanager/ui/lib/bot-load-types'
import type { InstanceGroupNode } from '@jianmanager/ui/lib/instance-group'
import type { InstanceInfo } from '@jianmanager/ui/lib/instance-types'
import type { ScheduleInfo } from '@jianmanager/ui/lib/schedule'
import type { Task, TaskLog } from '@jianmanager/ui/lib/task-status'

import { AlertsPageView, AlertChannelsTabView, AlertRulesTabView } from '@jianmanager/ui/components/views/alerts/AlertsPageView'
import { BotLoadWizard, type InstancePickerSlotProps } from '@jianmanager/ui/components/views/bot-load/BotLoadWizard'
import { SessionsTabView, type SessionsTabViewItem } from '@jianmanager/ui/components/views/bot-load/SessionsTabView'
import { TemplateDialog } from '@jianmanager/ui/components/views/bot-load/TemplateDialog'
import { TemplatesTabView } from '@jianmanager/ui/components/views/bot-load/TemplatesTabView'
import { SessionBots } from '@jianmanager/ui/components/views/bot-load/session/SessionBots'
import { SessionEvents } from '@jianmanager/ui/components/views/bot-load/session/SessionEvents'
import { SessionFailures } from '@jianmanager/ui/components/views/bot-load/session/SessionFailures'
import { SessionMetrics } from '@jianmanager/ui/components/views/bot-load/session/SessionMetrics'
import { SessionConfig, SessionOverview } from '@jianmanager/ui/components/views/bot-load/session/SessionOverviewParts'
import ImportServerWizardView from '@jianmanager/ui/components/views/import-server/ImportServerWizardView'
import { OverviewPageView } from '@jianmanager/ui/components/views/overview/OverviewPageView'
import ProvisionProxyDialogView from '@jianmanager/ui/components/views/provision/ProvisionProxyDialogView'
import ProvisionServerDialogView from '@jianmanager/ui/components/views/provision/ProvisionServerDialogView'
import { SchedulesPageView, type ScheduleLogRow } from '@jianmanager/ui/components/views/schedules/SchedulesPageView'
import { SystemUpdatePageView } from '@jianmanager/ui/components/views/system-update/SystemUpdatePageView'
import { TasksPageView } from '@jianmanager/ui/components/views/tasks/TasksPageView'
import { ConfigBaselinesPageView, type ConfigBaselineRow } from '@jianmanager/ui/components/views/config-baselines/ConfigBaselinesPageView'

// ── 告警域样例 ────────────────────────────────────────────────────────

const demoChannels: AlertChannelInfo[] = [
  {
    id: 1,
    uuid: 'ch-0001',
    name: '运维值班群 webhook',
    type: 'webhook',
    enabled: true,
    config: '{"url":"https://hook.example.com/jianmanager"}',
    createdAt: '2026-07-01T02:00:00Z',
  },
  {
    id: 2,
    uuid: 'ch-0002',
    name: '平台邮件通知',
    type: 'email',
    enabled: false,
    config: '{"from":"alert@example.com","to":"ops@example.com"}',
    createdAt: '2026-07-02T02:00:00Z',
  },
]

const demoRules: AlertRuleInfo[] = [
  {
    id: 1,
    uuid: 'rule-0001',
    name: '节点 CPU 持续偏高',
    triggerType: 'metric',
    level: 'warn',
    targetType: 'node',
    targetId: 1,
    metric: 'node_cpu_pct',
    operator: '>',
    threshold: 85,
    durationSec: 300,
    keyword: '',
    eventMatch: '',
    channelIds: '[1]',
    dedupWindowSec: 900,
    silenceStart: '',
    silenceEnd: '',
    notifyRecover: true,
    notifyType: '',
    notifyTarget: '',
    enabled: true,
    createdAt: '2026-07-03T08:00:00Z',
  },
  {
    id: 2,
    uuid: 'rule-0002',
    name: '实例日志出现 panic',
    triggerType: 'log_keyword',
    level: 'critical',
    targetType: 'instance',
    targetId: 3,
    metric: '',
    operator: '',
    threshold: 0,
    durationSec: 0,
    keyword: 'panic',
    eventMatch: '',
    channelIds: '[1,2]',
    dedupWindowSec: 300,
    silenceStart: '23:00',
    silenceEnd: '07:00',
    notifyRecover: false,
    notifyType: '',
    notifyTarget: '',
    enabled: false,
    createdAt: '2026-07-04T08:00:00Z',
  },
]

const demoAlertEvents: AlertEventInfo[] = [
  {
    id: 11,
    ruleId: 1,
    targetId: 1,
    instanceName: 'survival-01',
    level: 'warn',
    triggerType: 'metric',
    value: 91.4,
    message: 'node-bj-01 CPU 91.4%，持续 5 分钟',
    count: 3,
    resolved: false,
    firedAt: '2026-07-05T09:12:00Z',
    lastFiredAt: '2026-07-05T09:22:00Z',
    acknowledged: false,
    read: false,
    rule: { name: '节点 CPU 持续偏高' },
  },
  {
    id: 12,
    ruleId: 2,
    targetId: 3,
    instanceName: 'survival-01',
    level: 'critical',
    triggerType: 'log_keyword',
    value: 0,
    message: 'latest.log 命中 panic：Chunk save 线程异常退出',
    count: 1,
    resolved: false,
    firedAt: '2026-07-05T09:05:00Z',
    acknowledged: true,
    acknowledgedBy: 1,
    acknowledgedAt: '2026-07-05T09:07:00Z',
    read: true,
    rule: { name: '实例日志出现 panic' },
  },
  {
    id: 13,
    ruleId: 1,
    targetId: 2,
    instanceName: 'creative-01',
    level: 'info',
    triggerType: 'metric',
    value: 62.1,
    message: 'node-sh-01 CPU 回落至 62.1%',
    count: 1,
    resolved: true,
    firedAt: '2026-07-05T08:40:00Z',
    resolvedAt: '2026-07-05T08:52:00Z',
    acknowledged: true,
    read: true,
    rule: { name: '节点 CPU 持续偏高' },
  },
]

// ── 压测（bot-load）域样例 ─────────────────────────────────────────────

const demoThresholds: BotLoadThresholds = {
  minOnlineRate: 0.98,
  minCommandSentRate: 0.99,
  minScheduleCompletionRate: 0.95,
  minWorkerHealthRate: 0.9,
  minBarrierArrivalRate: 0.95,
  maxScheduleLagP95Ms: 250,
  maxProcessCrashes: 0,
  safety: { maxExecutorMemoryRate: 0.85, maxEventLoopP95Ms: 120, sustainSeconds: 30 },
}

const demoTemplates: BotLoadTemplate[] = [
  {
    id: 7,
    uuid: 'tpl-0007',
    name: '登录风暴 200 Bot',
    description: '稳定负载 200 Bot，复现开服登录洪峰',
    commandSchedule: {
      commands: [
        { id: 'login', atMs: 0, command: '/login loadtest' },
        { id: 'heartbeat', atMs: 5000, command: '/ping', repeat: { intervalMs: 30000, count: 20 } },
      ],
      durationMs: 600000,
      jitterMs: 50,
    },
    loadProfile: { type: 'stable', targetBots: 200, rampUpSeconds: 30, durationSeconds: 600 },
    thresholds: demoThresholds,
    tags: ['压测', '登录'],
    createdBy: 1,
    createdAt: '2026-07-01T10:00:00Z',
    updatedAt: '2026-07-04T10:00:00Z',
  },
  {
    id: 8,
    uuid: 'tpl-0008',
    name: '阶梯加压 50→200',
    description: '三阶段阶梯，阈值失败即停',
    commandSchedule: { commands: [{ id: 'login', atMs: 0, command: '/login step' }], durationMs: 900000 },
    loadProfile: {
      type: 'step',
      stages: [
        { targetBots: 50, holdSeconds: 120 },
        { targetBots: 120, holdSeconds: 120 },
        { targetBots: 200, holdSeconds: 180 },
      ],
      stopOnThresholdFailure: true,
    },
    thresholds: demoThresholds,
    tags: ['压测', '阶梯'],
    createdBy: 1,
    createdAt: '2026-07-02T10:00:00Z',
    updatedAt: '2026-07-03T10:00:00Z',
  },
]

const demoSessions: SessionsTabViewItem[] = [
  {
    id: 101,
    namePrefix: 'login-storm',
    instanceId: 1,
    status: 'completed',
    count: 200,
    counts: { total: 200, byStatus: { connected: 200, failed: 0 } },
  },
  {
    id: 102,
    namePrefix: 'hold-50',
    instanceId: 3,
    status: 'running',
    count: 50,
    counts: { total: 50, byStatus: { connected: 47, connecting: 2, failed: 1 } },
  },
  {
    id: 103,
    namePrefix: 'spike-500',
    instanceId: 2,
    status: 'failed',
    count: 500,
    counts: { total: 500, byStatus: { failed: 112, connected: 388 } },
  },
]

/** 会话详情（概览 / 配置快照共用同一份快照）。 */
const demoRun: BotLoadRunV2 = {
  schemaVersion: 2,
  id: 102,
  uuid: 'run-0102',
  instanceId: 3,
  instanceName: 'survival-01',
  name: '持续在线保持 50 Bot',
  namePrefix: 'hold-50',
  count: 50,
  behavior: 'hold',
  config: { server: '10.0.0.11', port: 25565, auth: 'offline', version: '1.20.4' },
  orchestrationSummary: { steps: 2, barriers: 0 },
  status: 'running',
  counts: { total: 50, byStatus: { connected: 47, connecting: 2, failed: 1 } },
  allocations: [
    {
      batchId: 'batch-1',
      ordinal: 1,
      executorNodeId: 2,
      executorNodeUuid: 'node-uuid-2',
      executorNodeName: 'node-bj-02',
      plannedCount: 30,
      connectStartAt: '2026-07-05T09:00:00Z',
      connectIntervalMs: 50,
      idempotencyKey: 'ik-batch-1',
    },
    {
      batchId: 'batch-2',
      ordinal: 2,
      executorNodeId: 3,
      executorNodeUuid: 'node-uuid-3',
      executorNodeName: 'node-sh-01',
      plannedCount: 20,
      connectStartAt: '2026-07-05T09:00:02Z',
      connectIntervalMs: 50,
      idempotencyKey: 'ik-batch-2',
    },
  ],
  batches: [
    {
      id: 1,
      uuid: 'batch-uuid-1',
      executorNodeId: 2,
      ordinal: 1,
      plannedCount: 30,
      acceptedCount: 30,
      connectedCount: 29,
      failedCount: 1,
      state: 'completed',
      startedAt: '2026-07-05T09:00:00Z',
      endedAt: '2026-07-05T09:00:20Z',
    },
    {
      id: 2,
      uuid: 'batch-uuid-2',
      executorNodeId: 3,
      ordinal: 2,
      plannedCount: 20,
      acceptedCount: 20,
      connectedCount: 18,
      failedCount: 0,
      state: 'running',
      startedAt: '2026-07-05T09:00:02Z',
    },
  ],
  startedAt: '2026-07-05T09:00:00Z',
  createdAt: '2026-07-05T08:59:00Z',
  updatedAt: '2026-07-05T09:26:00Z',
  templateId: 7,
  targetBots: 50,
  runState: 'running',
  verdict: 'pending',
  verdictReasons: [
    {
      key: 'online_rate',
      state: 'pass',
      expected: 0.98,
      actual: 0.94,
      unit: 'ratio',
      message: '在线率 94%，尚在爬坡窗口内',
    },
    {
      key: 'process_crashes',
      state: 'pass',
      expected: 0,
      actual: 0,
      unit: 'count',
      message: '执行器无崩溃',
    },
    {
      key: 'schedule_lag_p95_ms',
      state: 'fail',
      expected: 250,
      actual: 318,
      unit: 'ms',
      message: '调度延迟 P95 318ms，超出阈值 250ms',
    },
  ],
  currentStage: 1,
  loadProfile: { type: 'stable', targetBots: 50, rampUpSeconds: 20, durationSeconds: 1800 },
  thresholds: demoThresholds,
  loadCounts: { planned: 50, accepted: 50, connecting: 2, connected: 47, disconnected: 0, failed: 1, stopped: 0 },
  commandCounts: {
    login: { planned: 50, sent: 50, failed: 0, timedOut: 0, cancelled: 0 },
    heartbeat: { planned: 120, sent: 86, failed: 1, timedOut: 2, cancelled: 0 },
  },
  barrier: { waiting: 0, arrived: 0, released: 0, timedOut: 0 },
  maxStableBots: 48,
  failureSummary: { target: 1, executor: 0, network: 0, scenario: 0, internal: 0 },
  commandSchedule: { commands: [{ id: 'login', atMs: 0, command: '/login loadtest' }], durationMs: 1800000 },
}

const demoSessionBots: BotLoadRunBot[] = [
  {
    id: 1,
    uuid: 'bot-0001',
    name: 'hold-50-0001',
    status: 'connected',
    executorNodeId: 2,
    stepId: 'login',
    reconnectCount: 0,
    lastSeenAt: '2026-07-05T09:25:40Z',
  },
  {
    id: 2,
    uuid: 'bot-0002',
    name: 'hold-50-0002',
    status: 'connecting',
    executorNodeId: 3,
    stepId: 'login',
    reconnectCount: 1,
    lastSeenAt: '2026-07-05T09:25:38Z',
  },
  {
    id: 3,
    uuid: 'bot-0003',
    name: 'hold-50-0003',
    status: 'failed',
    executorNodeId: 3,
    stepId: 'login',
    commandId: 'login',
    reconnectCount: 3,
    lastSeenAt: '2026-07-05T09:24:10Z',
    lastError: 'ECONNREFUSED 10.0.0.11:25565',
  },
]

const demoFailures: BotLoadFailure[] = [
  {
    id: 'f-1',
    runUuid: 'run-0102',
    botUuid: 'bot-0003',
    executorNodeId: 3,
    stepId: 'login',
    category: 'network',
    errorCode: 'ECONNREFUSED',
    message: '连接被拒绝：目标端口未监听或连接数达上限',
    retryable: true,
    occurredAt: '2026-07-05T09:04:10Z',
  },
  {
    id: 'f-2',
    runUuid: 'run-0102',
    botUuid: 'bot-0017',
    executorNodeId: 2,
    category: 'target',
    errorCode: 'KICKED_WHITELIST',
    message: '被服务器踢出：白名单未包含该 Bot 账号',
    retryable: false,
    occurredAt: '2026-07-05T09:03:52Z',
  },
]

const demoEvents: BotLoadRunEvent[] = [
  {
    eventId: 'ev-1',
    runId: 102,
    runUuid: 'run-0102',
    timestamp: '2026-07-05T09:00:00Z',
    type: 'run-state',
    payload: { from: 'starting', to: 'running' },
  },
  {
    eventId: 'ev-2',
    runId: 102,
    runUuid: 'run-0102',
    timestamp: '2026-07-05T09:00:05Z',
    type: 'command-send',
    botUuid: 'bot-0001',
    commandId: 'login',
    executorNodeId: 2,
    stageIndex: 1,
    payload: { command: '/login loadtest', ok: true },
  },
  {
    eventId: 'ev-3',
    runId: 102,
    runUuid: 'run-0102',
    timestamp: '2026-07-05T09:04:10Z',
    type: 'executor-crash',
    executorNodeId: 3,
    payload: { reason: 'Bot Worker 子进程退出码 1', restarted: true },
  },
]

/** 会话指标：三个采样点（含执行器健康与连接时延分位）。 */
const demoMetricPoints: BotLoadMetricPoint[] = [
  {
    timestamp: '2026-07-05T09:20:00Z',
    stageIndex: 1,
    counts: { planned: 50, connected: 44, connecting: 5 },
    command: { sent: 44, failed: 0 },
    barrier: { waiting: 0, arrived: 0 },
    executor: [
      { nodeId: 2, activeBots: 26, rssBytes: 536870912, eventLoopP95Ms: 42, cpuPercent: 31, health: 'healthy' },
      { nodeId: 3, activeBots: 18, rssBytes: 402653184, eventLoopP95Ms: 68, cpuPercent: 27, health: 'healthy' },
    ],
    latency: {
      connectP50Ms: 210,
      connectP95Ms: 640,
      connectP99Ms: 980,
      scheduleLagP50Ms: 120,
      scheduleLagP95Ms: 318,
      scheduleLagP99Ms: 540,
      barrierReleaseLagP50Ms: null,
      barrierReleaseLagP95Ms: null,
      barrierReleaseLagP99Ms: null,
    },
    errors: { ECONNREFUSED: 1 },
  },
  {
    timestamp: '2026-07-05T09:21:00Z',
    stageIndex: 1,
    counts: { planned: 50, connected: 46, connecting: 3 },
    command: { sent: 62, failed: 1 },
    barrier: { waiting: 0, arrived: 0 },
    executor: [
      { nodeId: 2, activeBots: 27, rssBytes: 540016640, eventLoopP95Ms: 45, cpuPercent: 33, health: 'healthy' },
      { nodeId: 3, activeBots: 19, rssBytes: 408944640, eventLoopP95Ms: 74, cpuPercent: 29, health: 'healthy' },
    ],
    latency: {
      connectP50Ms: 195,
      connectP95Ms: 610,
      connectP99Ms: 920,
      scheduleLagP50Ms: 110,
      scheduleLagP95Ms: 296,
      scheduleLagP99Ms: 512,
      barrierReleaseLagP50Ms: null,
      barrierReleaseLagP95Ms: null,
      barrierReleaseLagP99Ms: null,
    },
    errors: {},
  },
  {
    timestamp: '2026-07-05T09:22:00Z',
    stageIndex: 1,
    counts: { planned: 50, connected: 47, connecting: 2 },
    command: { sent: 86, failed: 1 },
    barrier: { waiting: 0, arrived: 0 },
    executor: [
      { nodeId: 2, activeBots: 28, rssBytes: 545259520, eventLoopP95Ms: 41, cpuPercent: 30, health: 'healthy' },
      { nodeId: 3, activeBots: 19, rssBytes: 411041792, eventLoopP95Ms: 70, cpuPercent: 28, health: 'healthy' },
    ],
    latency: {
      connectP50Ms: 188,
      connectP95Ms: 590,
      connectP99Ms: 880,
      scheduleLagP50Ms: 104,
      scheduleLagP95Ms: 302,
      scheduleLagP99Ms: 498,
      barrierReleaseLagP50Ms: null,
      barrierReleaseLagP95Ms: null,
      barrierReleaseLagP99Ms: null,
    },
    errors: {},
  },
]

/** 实时流指标（SSE 推来的最新一点；与历史页按 timestamp 合并，实时点覆盖同刻历史点）。 */
const demoLiveMetricPoints: BotLoadMetricPoint[] = [
  {
    timestamp: '2026-07-05T09:23:00Z',
    stageIndex: 1,
    counts: { planned: 50, connected: 48, connecting: 1 },
    command: { sent: 104, failed: 1 },
    barrier: { waiting: 0, arrived: 0 },
    executor: [
      { nodeId: 2, activeBots: 29, rssBytes: 549453824, eventLoopP95Ms: 40, cpuPercent: 31, health: 'healthy' },
      { nodeId: 3, activeBots: 19, rssBytes: 413138944, eventLoopP95Ms: 66, cpuPercent: 27, health: 'healthy' },
    ],
    latency: {
      connectP50Ms: 182,
      connectP95Ms: 574,
      connectP99Ms: 861,
      scheduleLagP50Ms: 98,
      scheduleLagP95Ms: 288,
      scheduleLagP99Ms: 470,
      barrierReleaseLagP50Ms: null,
      barrierReleaseLagP95Ms: null,
      barrierReleaseLagP99Ms: null,
    },
    errors: {},
  },
]

/** 发压节点容量（向导与预检共用）。 */
const demoNodeCapacities: BotLoadNodeCapacity[] = [
  {
    nodeId: 2,
    nodeUuid: 'node-uuid-2',
    nodeName: 'node-bj-02',
    online: true,
    tunnelConnected: true,
    botWorkerReady: true,
    legacy: false,
    maxBots: 500,
    activeBots: 30,
    reservedBots: 0,
    availableBots: 470,
    capacityGeneration: 12,
    botWorkerVersion: '1.8.0',
    rssBytes: 545259520,
    eventLoopP95Ms: 41,
    lastHeartbeatAt: '2026-07-05T09:25:30Z',
  },
  {
    nodeId: 3,
    nodeUuid: 'node-uuid-3',
    nodeName: 'node-sh-01',
    online: true,
    tunnelConnected: true,
    botWorkerReady: true,
    legacy: false,
    maxBots: 300,
    activeBots: 20,
    reservedBots: 0,
    availableBots: 280,
    capacityGeneration: 9,
    botWorkerVersion: '1.8.0',
    rssBytes: 411041792,
    eventLoopP95Ms: 70,
    lastHeartbeatAt: '2026-07-05T09:25:31Z',
  },
]

// ── 定时任务 / 任务中心 / 配置基线样例 ──────────────────────────────────

const demoSchedules: ScheduleInfo[] = [
  {
    id: 1,
    uuid: 'sch-0001',
    instanceId: 3,
    instanceName: 'survival-01',
    name: '每日 04:00 重启',
    cronExpr: '0 4 * * *',
    action: 'restart',
    payload: '',
    enabled: true,
    lastRun: '2026-07-05T04:00:00Z',
    createdAt: '2026-06-20T12:00:00Z',
  },
  {
    id: 2,
    uuid: 'sch-0002',
    instanceId: 3,
    instanceName: 'survival-01',
    name: '每 30 分钟存档提示',
    cronExpr: '*/30 * * * *',
    action: 'command',
    payload: 'say 服务器将在 5 分钟后自动存档',
    enabled: true,
    lastRun: '2026-07-05T09:00:00Z',
    createdAt: '2026-06-22T12:00:00Z',
  },
  {
    id: 3,
    uuid: 'sch-0003',
    instanceId: 1,
    instanceName: 'lobby-01',
    name: '每周日 03:00 备份',
    cronExpr: '0 3 * * 0',
    action: 'backup',
    payload: '',
    enabled: false,
    lastRun: null,
    createdAt: '2026-06-25T12:00:00Z',
  },
]

const demoScheduleLogs: ScheduleLogRow[] = [
  { id: 1, action: 'restart', status: 'success', error: '', startedAt: '2026-07-05T04:00:00Z' },
  { id: 2, action: 'restart', status: 'success', error: '', startedAt: '2026-07-04T04:00:00Z' },
  {
    id: 3,
    action: 'restart',
    status: 'failed',
    error: '实例处于维护模式，停止指令被拒绝',
    startedAt: '2026-07-03T04:00:00Z',
  },
]

const demoTasks: Task[] = [
  {
    id: 1,
    taskId: 'task-0001',
    nodeId: 2,
    kind: 'provision',
    state: 'running',
    progress: 62,
    title: '搭建子服 survival-02',
    detail: 'paper 1.20.4 build 496',
    error: '',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '2026-07-05T09:10:00Z',
    updatedAt: '2026-07-05T09:25:30Z',
  },
  {
    id: 2,
    taskId: 'task-0002',
    nodeId: 3,
    kind: 'runtime_install',
    state: 'failed',
    progress: 38,
    title: '安装节点运行时库 JDK 21',
    detail: 'temurin-21.0.4+7',
    error: 'dial tcp github.com:443: i/o timeout（下载源疑似网络受限）',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '2026-07-05T08:40:00Z',
    updatedAt: '2026-07-05T08:52:10Z',
  },
  {
    id: 3,
    taskId: 'task-0003',
    nodeId: 1,
    kind: 'backup_create',
    state: 'succeeded',
    progress: 100,
    title: '创建实例备份 survival-01',
    detail: '快照 backup-20260705-0300',
    error: '',
    result: '{"snapshotId":"backup-20260705-0300","sizeBytes":12884901888}',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '2026-07-05T03:00:00Z',
    updatedAt: '2026-07-05T03:06:40Z',
  },
]

const demoTaskLogs: TaskLog[] = [
  { id: 1, taskId: 'task-0002', seq: 1, line: '开始解析运行时清单：jdk 21 / linux amd64', ts: '2026-07-05T08:40:01Z' },
  { id: 2, taskId: 'task-0002', seq: 2, line: '从镜像源下载 temurin-21.0.4+7.tar.gz …', ts: '2026-07-05T08:40:05Z' },
  { id: 3, taskId: 'task-0002', seq: 3, line: '重试 1/3：dial tcp github.com:443: i/o timeout', ts: '2026-07-05T08:47:22Z' },
  { id: 4, taskId: 'task-0002', seq: 4, line: '任务失败：出站网络不可达，建议配置代理或更换镜像源', ts: '2026-07-05T08:52:10Z' },
]

const demoBaselines: ConfigBaselineRow[] = [
  {
    id: 1,
    scopeKey: 'group:11',
    filePath: 'server.properties',
    content: 'online-mode=false\nmax-players=100\nview-distance=8\n',
    contentHash: '9f2c1b7d4e6a8c0f3b5d7e9a1c2f4b6d8e0a2c4f6b8d0e2a4c6f8b0d2e4a6c8f',
    message: '统一生存服视图距离与人数上限',
  },
  {
    id: 2,
    scopeKey: 'tag:proxy',
    filePath: 'config/paper-global.yml',
    content: 'proxies:\n  velocity:\n    enabled: true\n',
    contentHash: '0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9',
    message: '代理就绪：开启 Velocity 转发',
  },
]

const demoGroups: InstanceGroupNode[] = [
  { id: 11, uuid: 'grp-0011', name: '生存服', parentId: null, sort: 1, instanceCount: 4, memberInstanceIds: [3, 4] },
  { id: 12, uuid: 'grp-0012', name: '生存服 / 二区', parentId: 11, sort: 1, instanceCount: 2, memberInstanceIds: [4] },
  { id: 13, uuid: 'grp-0013', name: '大厅服', parentId: null, sort: 2, instanceCount: 1, memberInstanceIds: [1] },
]

// ── 概览 / 系统更新样例 ───────────────────────────────────────────────

const demoInstances: InstanceInfo[] = [
  {
    id: 1,
    uuid: 'inst-0001',
    nodeId: 1,
    name: 'lobby-01',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -Xmx2G -jar paper.jar nogui',
    jdkId: 3,
    workDir: '/srv/mc/lobby-01',
    serverPort: 25566,
    queryPort: 25566,
    probePort: 25576,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod"]',
    createdAt: '2026-05-01T02:00:00Z',
  },
  {
    id: 3,
    uuid: 'inst-0003',
    nodeId: 2,
    name: 'survival-01',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -Xmx8G -jar paper.jar nogui',
    jdkId: 3,
    workDir: '/srv/mc/survival-01',
    serverPort: 25565,
    queryPort: 25565,
    probePort: 25575,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod","region:bj"]',
    createdAt: '2026-04-11T02:00:00Z',
  },
  {
    id: 7,
    uuid: 'inst-0007',
    nodeId: 3,
    name: 'creative-02',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'CRASHED',
    statusReason: '实例未绑定 JDK，启动失败',
    startCommand: 'java -Xmx4G -jar paper.jar nogui',
    workDir: '/srv/mc/creative-02',
    serverPort: 25568,
    queryPort: 0,
    autoStart: false,
    autoRestart: false,
    tags: null,
    createdAt: '2026-06-30T02:00:00Z',
  },
]

const demoSystemCheck = {
  configured: true,
  latestVersion: 'v0.25.0',
  notes: '### v0.25.0\n\n- 新增全网金丝雀分批升级\n- 修复离线节点版本对比误报',
  source: 'github:wcpe/JianManager@stable',
  controlPlane: {
    name: 'control-plane',
    online: true,
    currentVersion: 'v0.24.3',
    os: 'linux',
    arch: 'amd64',
    updateAvailable: true,
    artifactAvailable: true,
    backupVersion: 'v0.24.2',
  },
  nodes: [
    {
      nodeId: 1,
      name: 'node-bj-01',
      online: true,
      currentVersion: 'v0.24.3',
      os: 'linux',
      arch: 'amd64',
      updateAvailable: true,
      artifactAvailable: true,
      backupVersion: 'v0.24.2',
    },
    {
      nodeId: 2,
      name: 'node-sh-01',
      online: true,
      currentVersion: 'v0.24.3',
      os: 'linux',
      arch: 'arm64',
      updateAvailable: false,
      artifactAvailable: false,
    },
    {
      nodeId: 3,
      name: 'node-gz-01',
      online: false,
      currentVersion: 'v0.24.3',
      os: 'linux',
      arch: 'amd64',
      updateAvailable: true,
      artifactAvailable: true,
      backupVersion: 'v0.24.2',
    },
  ],
  checkedAt: '2026-07-05T09:00:00Z',
}

const demoWorkerAssets = [
  {
    version: 'v0.24.3',
    os: 'linux',
    arch: 'amd64',
    cached: true,
    sha256: '3f8a1c0d9b7e5a4c2f0d8e6b4a2c0f8e6d4b2a0c8f6e4d2b0a8c6f4e2d0b8a6c',
    size: 52428800,
    cachedAt: '2026-07-05T08:30:00Z',
  },
  {
    version: 'v0.24.3',
    os: 'linux',
    arch: 'arm64',
    cached: false,
    sha256: '',
    size: 0,
    lastError: '上次预缓存失败：制品库无该平台条目',
  },
]

const demoRollout = {
  targetVersion: 'v0.25.0',
  state: 'running',
  total: 3,
  succeeded: 1,
  failed: 0,
  pending: 2,
  phase: 'rolling',
  canarySize: 1,
  batchSize: 2,
  currentBatch: 2,
  nodes: [
    {
      nodeId: 1,
      name: 'node-bj-01',
      state: 'succeeded',
      fromVersion: 'v0.24.3',
      toVersion: 'v0.25.0',
      error: '',
    },
    {
      nodeId: 2,
      name: 'node-sh-01',
      state: 'upgrading',
      fromVersion: 'v0.24.3',
      toVersion: 'v0.25.0',
      error: '',
    },
    {
      nodeId: 3,
      name: 'node-gz-01',
      state: 'pending',
      fromVersion: 'v0.24.3',
      toVersion: 'v0.25.0',
      error: '',
    },
  ],
}

// ── 插槽的演示实现 ────────────────────────────────────────────────────

/**
 * 实例选择器插槽的演示实现（BotLoadWizard 用）。
 * 真实外壳用带服务端搜索的实例选择器（千级实例不能全量下发）；这里只满足受控契约
 * （`value` 进、`onChange` 出），因此选项是写死的。
 */
function DemoInstancePicker({ value, onChange, enabled = true, placeholder }: InstancePickerSlotProps) {
  return (
    <select
      className="h-9 w-full rounded-md border bg-background px-2 text-sm"
      value={value === null ? '' : String(value)}
      disabled={!enabled}
      aria-label={placeholder ?? '实例选择器'}
      onChange={(e) => onChange(e.target.value ? Number(e.target.value) : null)}
    >
      <option value="">{placeholder ?? '选择目标实例'}</option>
      <option value="3">survival-01（端口 25565）</option>
      <option value="1">lobby-01（端口 25566）</option>
    </select>
  )
}

const demoNodeOptions: ComboboxOption[] = [
  { value: '1', label: 'node-bj-01（在线）' },
  { value: '2', label: 'node-sh-01（在线）' },
  { value: '3', label: 'node-gz-01（启动中）' },
]

const demoGroupOptions: ComboboxOption[] = [
  { value: '11', label: '生存服' },
  { value: '12', label: '生存服 / 二区' },
  { value: '13', label: '大厅服' },
]

const demoJdkOptions: ComboboxOption[] = [
  { value: '3', label: 'Temurin 21 (21.0.4+7)' },
  { value: '4', label: 'Temurin 17 (17.0.11+9)' },
]

/** 博物馆「业务视图 · 任务与工作流」分区（18 个视图，逐个一个 Panel）。 */
export function ViewsWorkflow() {
  // 弹窗类视图的开合：博物馆里由访客手动打开（Radix 弹窗关闭时不挂载内容）。
  const [botLoadOpen, setBotLoadOpen] = useState(false)
  const [templateOpen, setTemplateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [provisionServerOpen, setProvisionServerOpen] = useState(false)
  const [provisionProxyOpen, setProvisionProxyOpen] = useState(false)

  return (
    <>
      <Panel title="AlertsPageView · 告警中心（规则 / 事件 / 渠道三 Tab 宿主）">
        {/* tab 受控（外壳据此门控事件查询），故此处固定 events；规则 / 渠道两 Tab 的内容
            由同模块导出的子视图另行展示——父页只挂载当前 Tab。 */}
        <AlertsPageView
          tab="events"
          onTabChange={() => {}}
          unreadCount={2}
          rules={demoRules}
          channels={demoChannels}
          events={demoAlertEvents}
          eventsTotal={37}
          eventFilter={{ page: 1, pageSize: 50, level: 'warn' }}
          onEventFilterChange={() => {}}
          onToggleRule={() => {}}
          onDeleteRule={() => {}}
          onTestChannel={() => {}}
          onDeleteChannel={() => {}}
          onAcknowledgeEvent={() => {}}
          onMarkAllRead={() => {}}
          renderRuleDialog={(args) => (
            <p className="rounded-md border border-dashed p-3 text-[11px] text-muted-foreground">
              规则对话框插槽（外壳渲染）：{args.rule ? `编辑「${args.rule.name}」` : '新建规则'}
              ，候选渠道 {args.channels.length} 个 —— 表单本体依赖应用侧的节点 / 实例候选与 mutation。
            </p>
          )}
          renderChannelDialog={(args) => (
            <p className="rounded-md border border-dashed p-3 text-[11px] text-muted-foreground">
              通道对话框插槽（外壳渲染）：{args.channel ? `编辑「${args.channel.name}」` : '新建通道'}
              ，QQ 扫码绑定等取数动作同属外壳。
            </p>
          )}
        />

        <div className="mt-4 space-y-3 rounded-md border p-3">
          <p className="text-[11px] text-muted-foreground">
            父页只挂载当前 Tab（Radix Tabs 未激活即不挂载），以下两个子视图与父页同模块导出，
            用来展示另外两 Tab 的真实内容：
          </p>
          <AlertRulesTabView
            rules={demoRules}
            channels={demoChannels}
            onToggle={() => {}}
            onDelete={() => {}}
            renderRuleDialog={(args) => (
              <p className="rounded-md border border-dashed p-3 text-[11px] text-muted-foreground">
                规则对话框插槽（外壳渲染）：{args.rule ? `编辑「${args.rule.name}」` : '新建规则'}
              </p>
            )}
          />
          <AlertChannelsTabView
            channels={demoChannels}
            onTest={() => {}}
            onDelete={() => {}}
            renderChannelDialog={(args) => (
              <p className="rounded-md border border-dashed p-3 text-[11px] text-muted-foreground">
                通道对话框插槽（外壳渲染）：{args.channel ? `编辑「${args.channel.name}」` : '新建通道'}
              </p>
            )}
          />
        </div>

        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：三块数据（规则 / 通道 / 事件）与未读计数经 props 注入，七个写动作以回调上报，
          mutation 与文案由外壳决定；事件筛选 <code>eventFilter</code> 是取数查询键故归外壳，
          而 Tab 选中、两个对话框的开合与编辑目标、规则汇总筛选与卡片/列表切换留在组件内。
          两个对话框走 <code>renderRuleDialog</code> / <code>renderChannelDialog</code> 插槽
          （实现依赖应用侧候选与 mutation），此处给静态占位。
        </p>
      </Panel>

      <Panel title="BotLoadWizard · 五步压测创建向导">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setBotLoadOpen(true)}>
            打开 BotLoadWizard
          </Button>
          <BotLoadWizard
            open={botLoadOpen}
            onOpenChange={setBotLoadOpen}
            template={demoTemplates[0]}
            InstancePicker={DemoInstancePicker}
            nodes={{ items: demoNodeCapacities, totalCapacity: 800, availableCapacity: 750 }}
            onInstanceChange={() => {}}
            onEnsureRun={async () => 102}
            onPreflight={async () => ({
              runId: 102,
              runUuid: 'run-0102',
              ready: true,
              planToken: 'plan-token-demo',
              expiresAt: '2026-07-05T09:31:00Z',
              targetBots: 50,
              totalAvailable: 750,
              allocations: demoRun.allocations,
              nodeCapacities: demoNodeCapacities,
              probe: {
                required: false,
                connected: true,
                instanceId: 3,
                instanceUuid: 'inst-0003',
                message: '探针连通',
              },
              estimatedDurationSeconds: 45,
              warnings: [],
              blockers: [],
            })}
            onStart={async () => ({ id: 102 })}
            onNotify={() => {}}
            onFinished={() => {}}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：草稿状态机（reducer）与全部字段校验留在组件内；四个服务端交互（建运行 / 预检 / 启动 / 仅保存）
          经 <code>onEnsureRun</code> / <code>onPreflight</code> / <code>onStart</code> 上报，失败文案由外壳抛出、
          组件就地展示；实例选择器走 <code>InstancePicker</code> 插槽（千级实例须服务端搜索），此处是写死选项的演示实现。
        </p>
      </Panel>

      <Panel title="SessionsTabView · 压测会话列表">
        <SessionsTabView
          data={{ items: demoSessions, total: 3 }}
          page={1}
          onRefresh={() => {}}
          onPageChange={() => {}}
          onOpenSession={() => {}}
          onCreateClick={() => {}}
          onStart={() => {}}
          onStop={() => {}}
          initialSearch=""
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：当前页数据、页码与三态经 props 注入，翻页 / 打开详情 / 建向导 / 启停全部以回调上报；
          只有「搜索草稿」留在组件内——该筛选当前不参与取数，故不属查询语义。
        </p>
      </Panel>

      <Panel title="TemplateDialog · 模板创建 / 编辑 / 复制">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setTemplateOpen(true)}>
            打开 TemplateDialog（edit 模式）
          </Button>
          <TemplateDialog
            open={templateOpen}
            onOpenChange={setTemplateOpen}
            mode="edit"
            template={demoTemplates[1]}
            onSubmit={async () => null}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：表单草稿、cron 之外的命令计划 / 负载曲线 / 阈值三块编辑与本地校验留在组件内；
          提交经 <code>onSubmit</code> 上报——返回 <code>null</code> 表示成功（组件随即关窗），
          返回字符串则作为错误文案就地展示，成功提示由外壳负责。
        </p>
      </Panel>

      <Panel title="TemplatesTabView · 压测模板列表">
        <TemplatesTabView
          data={{ items: demoTemplates, total: 2 }}
          page={1}
          search=""
          onSearchChange={() => {}}
          activeTag=""
          onTagChange={() => {}}
          onRefresh={() => {}}
          onPageChange={() => {}}
          onCreateClick={() => {}}
          onRunFromTemplate={() => {}}
          onEditTemplate={() => {}}
          onCopyTemplate={() => {}}
          onDeleteTemplate={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：搜索框（会触发取数，防抖与 URL 写回在外壳）、标签与页码都是受控查询语义；
          当前页标签集合与行摘要由组件派生；四个行操作（运行 / 编辑 / 复制 / 删除）以回调上报。
        </p>
      </Panel>

      <Panel title="SessionBots · 会话 Bot 明细">
        <SessionBots
          filter={{ page: 1, status: 'connected' }}
          onFilterChange={() => {}}
          data={{ items: demoSessionBots, total: 50 }}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：过滤条件（含页码）由外壳从 URL 解析后注入，变更经 <code>onFilterChange</code> 回写；
          三个过滤输入按失焦 / 回车提交。整页全选与逐行勾选是纯 UI 状态，不外传。
        </p>
      </Panel>

      <Panel title="SessionEvents · 会话事件表">
        <SessionEvents
          page={1}
          onPageChange={() => {}}
          typeFilter=""
          onTypeFilterChange={() => {}}
          data={{ items: demoEvents, total: 3, snapshotEventId: 'ev-1' }}
          liveHead={[
            {
              eventId: 'ev-9',
              runId: 102,
              runUuid: 'run-0102',
              timestamp: '2026-07-05T09:25:58Z',
              type: 'stage',
              stageIndex: 1,
              payload: { stage: 'hold', targetBots: 50 },
            },
          ]}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：分页、类型过滤与 HTTP 事件页经 props 注入，实时流头部事件（<code>liveHead</code>）
          由外壳的 SSE 取数提供——首屏且无过滤时按 <code>eventId</code> 合并去重；翻页后视图由
          <code>snapshotEventId</code> 冻结，避免新事件插入造成页漂移。
        </p>
      </Panel>

      <Panel title="SessionFailures · 会话失败明细">
        <SessionFailures
          filter={{ page: 1 }}
          onFilterChange={() => {}}
          data={{ items: demoFailures, total: 2 }}
          failureSummary={{ target: 1, executor: 0, network: 1, scenario: 0, internal: 0 }}
          onRetry={() => {}}
          retryResult={null}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：失败页、五类计数（取自 run.failureSummary）与重试结果文案经 props 注入；
          类别下钻改的是外壳的过滤条件，重试经 <code>onRetry</code> 上报（传 botUuid 列表=重试选中，
          不传=重试当前过滤全集）；勾选集合与失败追踪抽屉的开合是纯 UI 状态。
        </p>
      </Panel>

      <Panel title="SessionMetrics · 会话指标图表">
        <SessionMetrics
          range="15m"
          onRangeChange={() => {}}
          data={{ items: demoMetricPoints }}
          liveMetrics={demoLiveMetricPoints}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：时间窗 <code>range</code> 决定服务端聚合粒度（15m/1h/all → 15s/1m/5m），故上提外壳；
          历史页与实时流两路数据经 props 注入后在组件内合并（实时点覆盖同刻历史点）并降采样；
          屏障释放图仅当数据里出现过屏障时才渲染，legacy 目标指标区块同理。
        </p>
      </Panel>

      <Panel title="SessionOverviewParts · 会话概览与配置快照（同模块两个可独立渲染件）">
        <div className="space-y-6">
          <SessionOverview
            run={demoRun}
            live={{
              liveMetrics: demoMetricPoints,
              warnings: [
                {
                  code: 'worker-restarted',
                  message: 'node-sh-01 的 Bot Worker 子进程重启，已自动恢复',
                  timestamp: '2026-07-05T09:04:12Z',
                },
              ],
            }}
            onNavigate={() => {}}
          />
          <SessionConfig run={demoRun} onNotify={() => {}} />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          该模块多数导出是零件（Kpi / 漏斗 / 分布等内部件），此处渲染其中两个可独立使用的件：
          <code>SessionOverview</code>（KPI 栅格 + 连接漏斗 + 命令计划进度 + 阈值判定 + 执行器分布，
          跳转经 <code>onNavigate</code> 上报）与 <code>SessionConfig</code>（配置快照逐段可复制，
          复制回执经 <code>onNotify</code> 交给外壳弹 toast，包内不引 toast 实现）。
          两者数据同源于一份 run 快照，均不发请求。
        </p>
      </Panel>

      <Panel title="ImportServerWizardView · 导入现有服务器向导">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setImportOpen(true)}>
            打开 ImportServerWizardView
          </Button>
          <ImportServerWizardView
            open={importOpen}
            initialNodeId={1}
            nodeOptions={demoNodeOptions}
            jdkOptions={demoJdkOptions}
            onInspect={async () => ({
              ok: true,
              result: {
                jars: [
                  { path: 'paper.jar', size: 48234496, mainClassHint: 'io.papermc.paperclip.Paperclip' },
                  { path: 'plugins/old-core.jar', size: 1048576 },
                ],
                jdks: [
                  { path: '/srv/mc/survival-01/jdk-21', vendor: 'Temurin', version: '21.0.4+7', majorVersion: 21, arch: 'amd64' },
                ],
                serverPort: 25565,
                eulaAccepted: true,
                propsFound: true,
              },
            })}
            onCheckAccess={async () => ({ exists: true, readable: true, writable: true })}
            onFixPermission={async () => ({ ok: true })}
            onSubmit={async () => ({ ok: true, instanceId: 42 })}
            onImported={() => {}}
            onNotify={() => {}}
            onQueryChange={() => {}}
            onClose={() => setImportOpen(false)}
            renderDirectoryPicker={({ nodeId, onPick }) => (
              <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed p-2 text-[11px] text-muted-foreground">
                <span>目录选择器插槽（外壳渲染，自带取数）：node #{nodeId}</span>
                <Button size="xs" variant="outline" onClick={() => onPick('/srv/mc/survival-01')}>
                  选用 /srv/mc/survival-01
                </Button>
              </div>
            )}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：节点 / JDK 候选经 props 注入，探测、单路径可读写预检与权限修复分别经
          <code>onInspect</code> / <code>onCheckAccess</code> / <code>onFixPermission</code> 上报；
          步骤推进、表单草稿、探测结果与三个在途标志留在组件内（都不触发取数）；
          目录选择器走 <code>renderDirectoryPicker</code> 插槽（实现自带取数，此处是静态占位）；
          成功后只回调 <code>onImported</code>，跳转由外壳完成——视图不引入 react-router。
        </p>
      </Panel>

      <Panel title="OverviewPageView · 概览页（a 范式）">
        <OverviewPageView
          range="24h"
          onRangeChange={() => {}}
          nodeTotal={3}
          overview={{
            totals: {
              nodeCount: 3,
              onlineNodeCount: 2,
              runningInstances: 2,
              cpuPct: 41.6,
              loadAvg: 62.4,
              memUsedBytes: 38654705664,
              memTotalBytes: 68719476736,
              onlinePlayers: 128,
            },
            trends: [
              {
                metricKey: 'node_cpu_pct',
                points: [
                  { ts: '2026-07-05T07:00:00Z', avg: 38.2 },
                  { ts: '2026-07-05T08:00:00Z', avg: 46.9 },
                  { ts: '2026-07-05T09:00:00Z', avg: 41.6 },
                ],
              },
              {
                metricKey: 'node_load',
                points: [
                  { ts: '2026-07-05T07:00:00Z', avg: 58.1 },
                  { ts: '2026-07-05T08:00:00Z', avg: 71.3 },
                  { ts: '2026-07-05T09:00:00Z', avg: 62.4 },
                ],
              },
              {
                metricKey: 'node_mem_used',
                points: [
                  { ts: '2026-07-05T07:00:00Z', avg: 34359738368 },
                  { ts: '2026-07-05T08:00:00Z', avg: 36507222016 },
                  { ts: '2026-07-05T09:00:00Z', avg: 38654705664 },
                ],
              },
              {
                metricKey: 'inst_players_online',
                points: [
                  { ts: '2026-07-05T07:00:00Z', avg: 96 },
                  { ts: '2026-07-05T08:00:00Z', avg: 143 },
                  { ts: '2026-07-05T09:00:00Z', avg: 128 },
                ],
              },
            ],
          }}
          activeGauge={null}
          onActiveGaugeChange={() => {}}
          attributionLoading={false}
          attributionError={false}
          exceptionInstances={[demoInstances[2]!]}
          exceptionLoading={false}
          exceptionError={false}
          recentTasks={demoTasks}
          recentTasksLoading={false}
          recentTasksError={false}
          activeAlerts={demoAlertEvents}
          activeAlertsLoading={false}
          activeAlertsError={false}
          canSeePlatformObs
          platformOverview={{
            health: { nodeCount: 3, onlineNodeCount: 2, runningInstanceCount: 2 },
            resources: {
              cpuPct: 41.6,
              memoryUsedBytes: 38654705664,
              memoryTotalBytes: 68719476736,
              freshness: 'fresh',
            },
            bots: {
              notice: 'Bot Worker 为共享进程，指标按节点汇总',
              botWorkerRssBytes: 956301312,
              botWorkerCpuPct: 12.4,
              activeCount: 50,
              eventLoopP95Ms: 70,
              unavailable: [],
            },
            alerts: { length: 2 },
            tasks: { length: 1 },
            exceptions: [
              { kind: 'crash', instanceId: 7, title: 'creative-02 处于 CRASHED', href: '/instances/7' },
              { kind: 'stale-heartbeat', nodeId: 3, title: 'node-gz-01 心跳陈旧', href: '/nodes/3' },
            ],
          }}
          platformOverviewLoading={false}
          platformOverviewError={false}
          renderHealthWall={() => (
            <div className="rounded-md border border-dashed p-3 text-[11px] text-muted-foreground">
              集群健康墙插槽（外壳渲染，FR-461）：热力矩阵自带取数与排序状态，此处为静态占位。
            </div>
          )}
          instances={demoInstances}
          onNeedMoreInstances={() => {}}
          renderLink={(args) => (
            <a href={args.href} className={args.className} data-testid={args.testId}>
              {args.children}
            </a>
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          a 范式：六个查询的结果与错误态经 props 注入（本示例把加载/错误态都置为就绪，只展示数据形态）；
          统计窗口 <code>range</code> 与归因 Tooltip 的 <code>activeGauge</code> 属取数口径故归外壳，
          实例表虚拟窗口与 Esc 键盘交互留在组件内；集群健康墙走 <code>renderHealthWall</code> 插槽
          （本体是应用侧接线层），跳转走 <code>renderLink</code> 插槽（缺省退化为原生 a 标签）。
        </p>
      </Panel>

      <Panel title="ProvisionServerDialogView · 一键搭建后端子服">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setProvisionServerOpen(true)}>
            打开 ProvisionServerDialogView
          </Button>
          <ProvisionServerDialogView
            open={provisionServerOpen}
            nodeOptions={demoNodeOptions}
            versions={['1.21.4', '1.21.1', '1.20.4', '1.20.1']}
            jdks={[
              { id: 3, vendor: 'Temurin', majorVersion: 21, version: '21.0.4+7' },
              { id: 4, vendor: 'Temurin', majorVersion: 17, version: '17.0.11+9' },
            ]}
            resolved={{ filename: 'paper-1.21.4-232.jar', build: 232, javaMajorRequired: 21 }}
            groupOptions={demoGroupOptions}
            onQueryChange={() => {}}
            onClose={() => setProvisionServerOpen(false)}
            onSubmit={async () => true}
            renderLink={(args) => (
              <a href={args.to} className={args.className}>
                {args.children}
              </a>
            )}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：四份候选（节点 / 版本 / JDK / 用户组）与解析预览经 props 注入，
          取数输入经 <code>onQueryChange</code> 上报（节点变→取 JDK、核心与版本变→重解析）；
          表单草稿与字段错误门控留在组件内；<code>jdks</code> 为 <code>undefined</code> 才表示「尚未取到」，
          这里给了空列表以外的真实记录，故不会触发「无 JDK 阻断」；
          外链走 <code>renderLink</code> 插槽，此处渲染为原生链接。
        </p>
      </Panel>

      <Panel title="ProvisionProxyDialogView · 一键搭建代理">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setProvisionProxyOpen(true)}>
            打开 ProvisionProxyDialogView
          </Button>
          <ProvisionProxyDialogView
            open={provisionProxyOpen}
            nodeOptions={demoNodeOptions}
            versions={['3.4.0-SNAPSHOT', '3.3.0', '3.2.0']}
            jdks={[{ id: 3, vendor: 'Temurin', majorVersion: 21, version: '21.0.4+7' }]}
            resolved={{ filename: 'velocity-3.3.0-1.jar', build: 1, javaMajorRequired: 17 }}
            groupOptions={demoGroupOptions}
            onQueryChange={() => {}}
            onClose={() => setProvisionProxyOpen(false)}
            onSubmit={async () => ({ forwardingSecret: 'demo-forwarding-secret-9f2c1b7d' })}
            onCopyResult={() => {}}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：候选与解析预览经 props 注入；提交经 <code>onSubmit</code> 上报——返回结果对象即成功
          （带 <code>forwardingSecret</code> 时切到 secret 步骤留待复制留存），返回 <code>null</code> 即失败、
          保留草稿与窗口；复制回执经 <code>onCopyResult</code> 交给外壳决定文案。表单草稿、错误门控与
          步骤推进留在组件内；bungeecord 不需要版本选择由包内 <code>needsProxyVersion</code> 判定。
        </p>
      </Panel>

      <Panel title="SchedulesPageView · 定时任务页">
        <SchedulesPageView
          schedules={demoSchedules}
          filter={null}
          onFilterChange={() => {}}
          logsId={1}
          onToggleLogs={() => {}}
          logs={demoScheduleLogs}
          dangerAllowed
          onSubmit={async () => true}
          onToggleEnabled={() => {}}
          onDelete={() => {}}
          renderInstancePicker={({ value, onChange, enabled, invalid }) => (
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed p-2 text-[11px] text-muted-foreground">
              <span>实例选择器插槽（外壳渲染）</span>
              <select
                className="h-8 rounded-md border bg-background px-2 text-sm"
                value={value === null ? '' : String(value)}
                disabled={!enabled}
                aria-invalid={invalid}
                aria-label="选择实例"
                onChange={(e) => onChange(e.target.value ? Number(e.target.value) : null)}
              >
                <option value="">选择实例</option>
                <option value="3">survival-01</option>
                <option value="1">lobby-01</option>
              </select>
            </div>
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：列表、执行日志与加载态经 props 注入；四个写动作以回调上报（提交回传 Promise，
          组件据返回值决定是否关窗）；汇总筛选 <code>filter</code> 与日志展开 <code>logsId</code>
          （后者是日志取数键）归外壳，弹窗开合 / 编辑目标 / 删除目标与卡片列表切换留在组件内；
          实例选择器走 <code>renderInstancePicker</code> 插槽——「何时发请求、防抖多久」是外壳策略。
          本示例把 1 号任务的执行日志置为展开，故可见日志三态（成功 / 失败 / 输出）。
        </p>
      </Panel>

      <Panel title="SystemUpdatePageView · 面板自更新">
        <SystemUpdatePageView
          isPlatformAdmin
          dangerAllowed
          check={demoSystemCheck}
          refreshing={false}
          onRefresh={() => {}}
          cpUpgrading={false}
          cpRollingBack={false}
          onUpgradeControlPlane={() => {}}
          onRollbackControlPlane={() => {}}
          nodePending={{ nodeId: 2, action: 'upgrade' }}
          onUpgradeNode={() => {}}
          onRollbackNode={() => {}}
          onUpgradeAll={() => {}}
          rollout={demoRollout}
          workerAssets={demoWorkerAssets}
          workerAssetsFetching={false}
          pendingWorkerAsset={null}
          onCacheWorkerAsset={() => {}}
          onNotify={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：检查结果、四处在途标志、rollout 进度与 Worker 缓存条目经 props 注入；
          升级 / 回滚 / 全网编排以回调上报（全网草稿原样传出，折算请求体在外壳）；
          二次确认框、金丝雀草稿（金丝雀数 / 每批数 / 失败即中止）与复制回执留在组件内，
          toast 经 <code>onNotify</code> 交给外壳；<code>isPlatformAdmin=false</code> 时整页渲染 forbidden 提示，
          这是路由守卫之外的纵深防御。
        </p>
      </Panel>

      <Panel title="TasksPageView · 任务中心">
        <TasksPageView
          tasks={demoTasks}
          total={3}
          limit={100}
          nodes={[
            { id: 1, name: 'node-bj-01' },
            { id: 2, name: 'node-sh-01' },
            { id: 3, name: 'node-gz-01' },
          ]}
          filters={{ state: '', kind: '', nodeId: '', keyword: '', time: '' }}
          expandedId="task-0002"
          detail={{ logs: demoTaskLogs, isLoading: false }}
          cancelingIds={[]}
          onChangeFilters={() => {}}
          onResetFilters={() => {}}
          onLoadMore={() => {}}
          onToggleExpand={() => {}}
          onCancelTask={async () => true}
          renderLink={(args) => (
            <a href={args.to} className={args.className}>
              {args.children}
            </a>
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：任务行、命中总数、三态、节点候选、展开详情与取消在途 id 全部经 props 注入
          （本示例把 <code>task-0002</code> 置为展开：可见错误正文、FR-279 网络失败引导与滚动日志）；
          筛选五维、增长窗口 <code>limit</code>、展开目标 <code>expandedId</code> 都会触发取数故归外壳，
          组件只上报意图；展开行的实测高度与取消二次确认框是纯 UI 状态。
          引导里的两处入口走 <code>renderLink</code> 插槽，此处渲染为原生链接。
        </p>
      </Panel>

      <Panel title="ConfigBaselinesPageView · 配置基线">
        <ConfigBaselinesPageView
          baselines={demoBaselines}
          groups={demoGroups}
          driftBaselineId={1}
          onToggleDrift={() => {}}
          drift={{
            items: [
              { instanceId: 3, instanceName: 'survival-01', drift: false, currentHash: '9f2c1b7d4e6a8c0f' },
              { instanceId: 4, instanceName: 'survival-02', drift: true, currentHash: '0a1b2c3d4e5f6071' },
              { instanceId: 5, instanceName: 'survival-03', drift: false, currentHash: '', error: '读取失败：权限不足' },
            ],
            drifted: 1,
          }}
          onRefreshDrift={() => {}}
          dangerAllowed
          onSubmit={async () => true}
          onDelete={async () => true}
          onConverge={async () => ({ targeted: 1, succeeded: 1, failed: 0 })}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          b 范式：基线列表、漂移明细、分组候选与全部在途态经 props 注入；保存 / 删除回传
          <code>Promise&lt;boolean&gt;</code>（组件据返回值决定是否关窗），收敛回传汇总供面板内联回显；
          <code>driftBaselineId</code> 是漂移取数的查询键故归外壳，编辑弹窗开合与草稿留在组件内。
          本示例把 1 号基线的漂移面板置为展开：三行明细里有一行「读取失败」，故不会给出「已全部一致」的结论。
          删除二次确认的 <code>scope</code> 固定 group、是否放行由外壳注入 <code>dangerAllowed</code>
          ——组件库不持鉴权状态，故可在博物馆这类无登录态环境独立渲染。
        </p>
      </Panel>
    </>
  )
}
