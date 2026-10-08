/**
 * @file 组件博物馆「业务视图 · 客户端分发」分区：登记客户端分发运维（页面 B）与频道工作台下
 *       11 个受控复合视图。它们同属 ADR-097 受控范式——a 范式：数据经 props 注入、组件不取数、
 *       不读路由；b 范式：动作经 onXxx 上报并由返回值决定视图是否复位（关窗 / 保留输入）。
 *       包内不引用 router / react-query / sonner / zustand，也不弹 toast，
 *       故可在没有应用外壳的博物馆里独立渲染。
 *
 * 两点环境事实（不是组件缺陷）：
 * - 博物馆未初始化 i18next，`useTranslation` 取不到实例，视图内文案会以 i18n key 原样显示
 *   （少数带 fallback 的 t() 显示 fallback 文案）；主控台不受影响。
 * - 模态对话框经 Radix Portal 挂到 document.body 并铺全屏遮罩：本分区所有弹窗都保持受控关闭态
 *   （由视图内按钮触发挂载），不常挂。
 * @input @jianmanager/ui（Panel）、components/views/client-dist/*（11 个视图）、
 *        lib/client-*-contracts（注入数据类型）
 * @output ViewsClientDist
 * @sync apps/ui-museum/src/App.tsx（既有 views 分区，后续并入同一导航段）
 */
import type { ReactNode } from 'react'
import { Panel } from '@jianmanager/ui'
import {
  ClientIntegrationGuideView,
  type ClientUpdaterJarsInfo,
} from '@jianmanager/ui/components/views/client-dist/ClientIntegrationGuideView'
import {
  ClientDistAggregateLogsView,
  ClientDistRequestLogsView,
} from '@jianmanager/ui/components/views/client-dist/ClientDistLogsTabView'
import { ClientPublishPageView } from '@jianmanager/ui/components/views/client-dist/ClientPublishPageView'
import { ClientStatsPanelView } from '@jianmanager/ui/components/views/client-dist/ClientStatsPanelView'
import {
  ClientUpdaterCoreSelectorView,
  type ClientUpdaterCoreVersion,
} from '@jianmanager/ui/components/views/client-dist/ClientUpdaterCoreSelectorView'
import {
  ClientVersionsPanelView,
  type ClientVersionsPanelVersion,
} from '@jianmanager/ui/components/views/client-dist/ClientVersionsPanelView'
import { OpsOverviewTabView } from '@jianmanager/ui/components/views/client-dist/OpsOverviewTabView'
import { SecurityActionsTabView } from '@jianmanager/ui/components/views/client-dist/SecurityActionsTabView'
import {
  IpAnalysisTabView,
  PlayerAnalysisTabView,
} from '@jianmanager/ui/components/views/client-dist/SecurityAnalysisTabsView'
// 注：文件名是 SecurityGroupsTabView.tsx，但导出名是 GroupsTabView（按实际导出导入）。
import { GroupsTabView } from '@jianmanager/ui/components/views/client-dist/SecurityGroupsTabView'
import {
  SecurityProfilesTabView,
  type ProfileMaskers,
} from '@jianmanager/ui/components/views/client-dist/SecurityProfilesTabView'
import type { ClientChannel, ClientPullKey } from '@jianmanager/ui/lib/client-channel-types'
import type { ClientDistEvent } from '@jianmanager/ui/lib/client-dist-events-contracts'
import type { ClientRuntimeOverview } from '@jianmanager/ui/lib/client-runtime-contracts'
import type {
  ClientDistIpAnalysis,
  ClientDistPlayerAnalysis,
  ClientDistSecurityLogItem,
  ClientDistSecurityOverview,
  ClientDistSecurityProfile,
  ClientProtectionAction,
  ClientSecurityGroup,
} from '@jianmanager/ui/lib/client-dist-security-contracts'
import type { ClientDistObservability, ClientDistStats } from '@jianmanager/ui/lib/client-dist-stats-contracts'

/** OpsShared 的「全部」哨兵值（受控筛选的“不筛选”取值）。 */
const OPS_ALL = '__all__'

/**
 * 页面级视图的预览框：给页面壳（PageShell 的 `flex-1` + 内部 `overflow-auto`）一个确定高度，
 * 让它真按「页面」滚动；裸放进 Panel 时这些类失效，长页会一路撑开博物馆。
 * 高度取 720px ≈ 主控台内容区高度（与 views-admin 分区的裁剪框同款）。
 */
function PageFrame({ children }: { children: ReactNode }) {
  return <div className="flex h-[720px] flex-col overflow-hidden rounded-md border">{children}</div>
}

/** 插槽静态占位：博物馆没有应用侧接线层（取数容器 / 导出按钮 / FileBrowser），
 *  用虚线框灰字块示意外壳注入物的落位。 */
function SlotPlaceholder({ label }: { label: string }) {
  return (
    <span className="inline-flex h-7 items-center rounded-md border border-dashed px-2 text-[11px] text-muted-foreground">
      {label}
    </span>
  )
}

/** 深链渲染占位：真实 router Link 由外壳注入（保持 SPA 导航）；博物馆用原生 <a> 并阻止跳转。 */
type DemoLinkArgs = { to: string; className: string; children: ReactNode }
const demoRenderLink = ({ to, className, children }: DemoLinkArgs) => (
  <a href={to} className={className} onClick={(event) => event.preventDefault()}>
    {children}
  </a>
)

/**
 * 假脱敏：真实脱敏函数（应用侧 maskPlayerName / maskMachineId / maskInstallId）由外壳按隐私策略注入，
 * 博物馆只演示「视图把外壳给的函数作用到展示字段上」这一接线形态。
 */
const maskHead = (value: string | null | undefined): string => (value ? `${value.slice(0, 3)}***` : '')
const DEMO_MASKERS: ProfileMaskers = {
  playerName: maskHead,
  machineId: maskHead,
  installId: maskHead,
}

// ── 全量日志：请求事件（加厚请求表）────────────────────────────────────

/** 三行覆盖：成功 manifest（200）、密钥已吊销（401/INVALID_CLIENT_KEY）、断点续传制品（206）。 */
const DEMO_EVENTS: ClientDistEvent[] = [
  {
    id: 9101,
    channelId: 'survival-1',
    machineId: 'm-3f8c21a0',
    playerName: 'Steve',
    coreVersion: '1.20.4',
    ip: '203.0.113.24',
    kind: 'manifest',
    version: 42,
    artifactSha: '',
    bytes: 18432,
    status: 200,
    errCode: '',
    method: 'GET',
    path: '/api/v1/client-dist/survival-1/manifest',
    etag: 'W/"v42-6f1a"',
    durationMs: 37,
    createdAt: '2025-06-18T09:12:04Z',
  },
  {
    id: 9102,
    channelId: 'survival-1',
    machineId: 'm-77b0e4c9',
    playerName: 'Alex',
    coreVersion: '1.20.1',
    ip: '198.51.100.7',
    kind: 'manifest',
    version: 42,
    artifactSha: '',
    bytes: 512,
    status: 401,
    errCode: 'INVALID_CLIENT_KEY',
    errReason: '拉取密钥已吊销',
    method: 'GET',
    path: '/api/v1/client-dist/survival-1/manifest',
    durationMs: 12,
    createdAt: '2025-06-18T09:14:31Z',
  },
  {
    id: 9103,
    channelId: 'survival-1',
    machineId: 'm-3f8c21a0',
    playerName: 'Steve',
    coreVersion: '1.20.4',
    ip: '203.0.113.24',
    kind: 'artifact',
    version: 42,
    artifactSha: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
    bytes: 4194304,
    status: 206,
    errCode: '',
    method: 'GET',
    path: '/api/v1/client-dist/survival-1/artifacts/mods/example-mod-1.2.0.jar',
    etag: '"sha256-a1b2c3"',
    durationMs: 428,
    createdAt: '2025-06-18T09:15:02Z',
  },
]

/** 聚合日志行（安全侧 6 类型）：风险、握手、自动处置各一行，detail 为结构化摘要。 */
const DEMO_AGGREGATE_LOGS: ClientDistSecurityLogItem[] = [
  {
    id: 'risk-9001',
    type: 'risk',
    title: '同一 IP 在 10 分钟内触发 12 次无效密钥',
    channelId: 'survival-1',
    machineId: 'm-77b0e4c9',
    playerName: 'Alex',
    ip: '198.51.100.7',
    status: 'open',
    errCode: 'INVALID_CLIENT_KEY',
    createdAt: '2025-06-18T09:16:10Z',
    detail: { ruleCode: 'INVALID_KEY_BURST', scoreDelta: 20, windowMinutes: 10 },
  },
  {
    id: 'hello-9002',
    type: 'hello',
    title: '客户端首次握手（wedge 1.4.0）',
    channelId: 'survival-1',
    machineId: 'm-3f8c21a0',
    playerName: 'Steve',
    ip: '203.0.113.24',
    status: 'ok',
    createdAt: '2025-06-18T09:12:00Z',
    detail: { platform: 'windows', coreVersion: '1.20.4' },
  },
  {
    id: 'action-9003',
    type: 'action',
    title: '自动封禁 198.51.100.7（15 分钟）',
    channelId: 'survival-1',
    ip: '198.51.100.7',
    status: 'active',
    createdAt: '2025-06-18T09:16:12Z',
    detail: { action: 'block', durationMinutes: 15, auto: true },
  },
]

// ── 统计看板 / 运维总览：请求侧统计、更新侧观测、运行态、安全态势 ──────────

/** 请求侧统计（/client-dist/stats）：7 日下载曲线 + 版本/结果分布 + 来源 IP 排行。 */
const DEMO_STATS: ClientDistStats = {
  channelId: 'survival-1',
  days: 7,
  downloads: [
    { day: '2025-06-12', requests: 1180, bytes: 57_671_680 },
    { day: '2025-06-13', requests: 1326, bytes: 64_749_568 },
    { day: '2025-06-14', requests: 1502, bytes: 73_400_320 },
    { day: '2025-06-15', requests: 968, bytes: 47_185_920 },
    { day: '2025-06-16', requests: 1284, bytes: 62_914_560 },
    { day: '2025-06-17', requests: 1398, bytes: 68_157_440 },
    { day: '2025-06-18', requests: 1104, bytes: 53_477_376 },
  ],
  versions: [
    { version: 42, requests: 6842 },
    { version: 41, requests: 1920 },
  ],
  results: [
    { result: 'success', count: 8584 },
    { result: 'failure', count: 178 },
  ],
  successRate: 0.9797,
  failureRate: 0.0203,
  rollbackRate: 0.0042,
  activeMachines: 312,
  topIps: [
    { ip: '203.0.113.24', count: 428 },
    { ip: '198.51.100.7', count: 216 },
    { ip: '192.0.2.51', count: 132 },
  ],
}

/** 更新侧观测（/client-dist/observability）：小时桶时序 + 汇总 + 同环比基数 + 三路分布。 */
const DEMO_OBSERVABILITY: ClientDistObservability = {
  channelId: 'survival-1',
  from: '2025-06-12T00:00:00Z',
  to: '2025-06-19T00:00:00Z',
  series: [
    {
      ts: '2025-06-12T00:00:00Z',
      manifestPulls: 96,
      artifactPulls: 210,
      downloadBytes: 1_048_576_000,
      activeMachines: 140,
      updateTotal: 88,
      updateSuccess: 80,
      updateFailStatic: 4,
      updateRolledBack: 2,
      updateError: 2,
    },
    {
      ts: '2025-06-13T00:00:00Z',
      manifestPulls: 118,
      artifactPulls: 246,
      downloadBytes: 1_153_433_600,
      activeMachines: 158,
      updateTotal: 96,
      updateSuccess: 88,
      updateFailStatic: 4,
      updateRolledBack: 2,
      updateError: 2,
    },
    {
      ts: '2025-06-14T00:00:00Z',
      manifestPulls: 132,
      artifactPulls: 284,
      downloadBytes: 1_258_291_200,
      activeMachines: 176,
      updateTotal: 108,
      updateSuccess: 99,
      updateFailStatic: 5,
      updateRolledBack: 2,
      updateError: 2,
    },
  ],
  summary: {
    manifestPulls: 812,
    artifactPulls: 1740,
    downloadBytes: 7_340_032_000,
    updateTotal: 690,
    updateSuccess: 631,
    updateFailStatic: 32,
    updateRolledBack: 14,
    updateError: 13,
    successRate: 0.9145,
    failStaticRate: 0.0464,
    rollbackRate: 0.0203,
    activeMachines: 312,
    activeMachinesExact: true,
  },
  compare: {
    manifestPulls: 780,
    artifactPulls: 1655,
    downloadBytes: 7_012_000_000,
    updateTotal: 660,
    updateSuccess: 590,
    updateFailStatic: 40,
    updateRolledBack: 18,
    updateError: 12,
    activeMachines: 300,
  },
  versionDist: [
    { version: 42, count: 540 },
    { version: 41, count: 132 },
  ],
  platformDist: [
    { os: 'windows', count: 402 },
    { os: 'macos', count: 96 },
    { os: 'linux', count: 64 },
  ],
  lagDist: [
    { lag: 0, count: 480 },
    { lag: 1, count: 62 },
    { lag: 2, count: 20 },
  ],
}

/** 运行态总览（客户端心跳 + 更新结果遥测）：含一条机器明细用于分布/排行的口径展示。 */
const DEMO_RUNTIME: ClientRuntimeOverview = {
  channelId: 'survival-1',
  from: '2025-06-12T00:00:00Z',
  to: '2025-06-19T00:00:00Z',
  summary: {
    recentStarted: 128,
    todayStarted: 402,
    recentStarts: 96,
    todayStarts: 288,
    updateSuccessRate: 0.9145,
    updateFailureRate: 0.0855,
  },
  items: [
    {
      id: 1,
      channelId: 'survival-1',
      machineId: 'm-3f8c21a0',
      ip: '203.0.113.24',
      platform: 'windows',
      javaVersion: '17.0.11',
      launcher: 'official',
      coreVersion: '1.20.4',
      localVersion: 42,
      firstSeenAt: '2025-06-01T03:20:00Z',
      lastHeartbeatAt: '2025-06-18T09:15:00Z',
    },
  ],
  runtimeVersionDist: [
    { version: 42, count: 540 },
    { version: 41, count: 132 },
  ],
  coreVersionDist: [
    { value: '1.20.4', count: 402 },
    { value: '1.20.1', count: 86 },
  ],
  platformDist: [
    { value: 'windows', count: 402 },
    { value: 'macos', count: 96 },
    { value: 'linux', count: 64 },
  ],
  launcherDist: [
    { value: 'official', count: 380 },
    { value: 'third-party', count: 90 },
  ],
  lagDist: [
    { lag: 0, count: 480 },
    { lag: 1, count: 62 },
    { lag: 2, count: 20 },
  ],
  updateResultSeries: [
    { ts: '2025-06-12T00:00:00Z', success: 80, failStatic: 4, rolledBack: 2, error: 2 },
    { ts: '2025-06-13T00:00:00Z', success: 88, failStatic: 4, rolledBack: 2, error: 2 },
    { ts: '2025-06-14T00:00:00Z', success: 99, failStatic: 5, rolledBack: 2, error: 2 },
  ],
}

/** 安全态势快照：异常请求计数 + 四个排行（IP / 密钥 / 频道 / 玩家）。 */
const DEMO_SECURITY_OVERVIEW: ClientDistSecurityOverview = {
  activeDownloads: 18,
  downloadBytesPerSecond: 3_412_000,
  abnormalRequests: 42,
  unauthorizedRequests: 17,
  forbiddenRequests: 5,
  rateLimitedRequests: 20,
  blockedIpCount: 3,
  throttledKeyCount: 1,
  protectedChannelCount: 2,
  topIps: [
    { subject: '198.51.100.7', count: 216, bytes: 0, riskScore: 78 },
    { subject: '203.0.113.24', count: 428, bytes: 1_284_096_000, riskScore: 8 },
  ],
  topKeys: [
    { subject: 'jmck_ab12', count: 512, bytes: 1_686_749_184, riskScore: 24 },
    { subject: 'jmck_7f3c', count: 64, bytes: 0, riskScore: 62 },
  ],
  topChannels: [
    { subject: 'survival-1', count: 812 },
    { subject: 'creative-1', count: 184 },
  ],
  topPlayers: [
    { subject: 'Steve', count: 268, bytes: 1_284_096_000, riskScore: 10 },
    { subject: 'Alex', count: 64, bytes: 0, riskScore: 76 },
  ],
}

// ── 安全侧：处置流水、IP / 玩家剖析、分组、画像 ─────────────────────────

/** 处置流水：自动封禁 IP（active）、手动限速密钥（active）、频道降并发（expired）各一条。 */
const DEMO_ACTIONS: ClientProtectionAction[] = [
  {
    id: 501,
    targetType: 'ip',
    targetValue: '198.51.100.7',
    action: 'block',
    status: 'active',
    policy: { durationMinutes: 15 },
    reason: '10 分钟内 12 次无效拉取密钥',
    auto: true,
    expiresAt: '2025-06-18T09:31:12Z',
    createdBy: 0,
    createdAt: '2025-06-18T09:16:12Z',
    updatedAt: '2025-06-18T09:16:12Z',
  },
  {
    id: 502,
    targetType: 'key',
    targetValue: 'jmck_ab12',
    action: 'throttle',
    status: 'active',
    policy: { rateLimitPerSecond: 2 },
    reason: '运营手动限速（异常下载峰值）',
    auto: false,
    expiresAt: null,
    createdBy: 1,
    createdAt: '2025-06-17T13:02:00Z',
    updatedAt: '2025-06-17T13:02:00Z',
  },
  {
    id: 503,
    targetType: 'channel',
    targetValue: 'creative-1',
    action: 'concurrency',
    status: 'expired',
    policy: { maxConcurrency: 4 },
    reason: '发布会话临时降并发',
    auto: false,
    expiresAt: '2025-06-16T12:00:00Z',
    createdBy: 1,
    createdAt: '2025-06-16T10:00:00Z',
    updatedAt: '2025-06-16T12:00:00Z',
  },
]

/** IP 剖析行：正常高流量、已被封禁的高风险来源、跨频道来源各一行。 */
const DEMO_IP_ROWS: ClientDistIpAnalysis[] = [
  {
    ip: '203.0.113.24',
    requestCount: 428,
    rejectCount: 2,
    invalidKeyCount: 0,
    notFoundCount: 1,
    rangeCount: 12,
    downloadBytes: 1_284_096_000,
    keyCount: 2,
    channelCount: 1,
    riskScore: 8,
    blocked: false,
    lastSeen: '2025-06-18T09:15:02Z',
  },
  {
    ip: '198.51.100.7',
    requestCount: 216,
    rejectCount: 64,
    invalidKeyCount: 58,
    notFoundCount: 4,
    rangeCount: 0,
    downloadBytes: 0,
    keyCount: 1,
    channelCount: 1,
    riskScore: 78,
    blocked: true,
    lastSeen: '2025-06-18T09:16:10Z',
  },
  {
    ip: '192.0.2.51',
    requestCount: 132,
    rejectCount: 6,
    invalidKeyCount: 3,
    notFoundCount: 2,
    rangeCount: 1,
    downloadBytes: 402_653_184,
    keyCount: 1,
    channelCount: 2,
    riskScore: 24,
    blocked: false,
    lastSeen: '2025-06-18T08:58:44Z',
  },
]

/** 玩家名剖析行：第三行刻意留空玩家名，演示空值占位（—）。 */
const DEMO_PLAYER_ROWS: ClientDistPlayerAnalysis[] = [
  {
    playerName: 'Steve',
    installCount: 2,
    machineCount: 2,
    ipCount: 2,
    keyCount: 2,
    channelCount: 1,
    downloadBytes: 1_686_749_184,
    abnormalRequests: 1,
    riskScore: 10,
    lastSeen: '2025-06-18T09:15:02Z',
  },
  {
    playerName: 'Alex',
    installCount: 1,
    machineCount: 1,
    ipCount: 3,
    keyCount: 1,
    channelCount: 1,
    downloadBytes: 0,
    abnormalRequests: 58,
    riskScore: 76,
    lastSeen: '2025-06-18T09:16:10Z',
  },
  {
    playerName: '',
    installCount: 4,
    machineCount: 4,
    ipCount: 4,
    keyCount: 0,
    channelCount: 1,
    downloadBytes: 96_468_992,
    abnormalRequests: 0,
    riskScore: 0,
    lastSeen: '2025-06-18T07:30:00Z',
  },
]

/** 安全分组：手动分组（启用）与动态规则分组（停用）各一条。 */
const DEMO_GROUPS: ClientSecurityGroup[] = [
  {
    id: 31,
    name: '短窗无效密钥来源',
    kind: 'manual',
    targetType: 'ip',
    rule: null,
    actionPolicy: null,
    enabled: true,
    createdBy: 1,
    createdAt: '2025-06-10T06:00:00Z',
    updatedAt: '2025-06-17T11:24:00Z',
  },
  {
    id: 32,
    name: '观察期密钥',
    kind: 'dynamic',
    targetType: 'key',
    rule: { windowMinutes: 60, minInvalidKeyCount: 5 },
    actionPolicy: { action: 'observe' },
    enabled: false,
    createdBy: 1,
    createdAt: '2025-06-12T02:40:00Z',
    updatedAt: '2025-06-16T08:05:00Z',
  },
]

/** 客户端画像：一台正常机器 + 一台观察期机器（用于演示脱敏后的展示口径）。 */
const DEMO_PROFILES: ClientDistSecurityProfile[] = [
  {
    id: 7001,
    channelId: 'survival-1',
    machineId: 'm-3f8c21a0d9e4',
    installId: 'inst-8b1f4c2a',
    playerName: 'Steve',
    keyId: 7,
    keyPrefix: 'jmck_ab12',
    firstSeen: '2025-06-01T03:20:00Z',
    lastSeen: '2025-06-18T09:15:00Z',
    lastIp: '203.0.113.24',
    userAgent: 'JMUpdater/1.4.0 (Windows 10; x86_64)',
    coreVersion: '1.20.4',
    wedgeVersion: '1.4.0',
    manifestVersion: 42,
    os: 'windows',
    osVersion: '10.0.19045',
    arch: 'x86_64',
    javaVendor: 'Eclipse Adoptium',
    javaVersion: '17.0.11',
    javaArch: 'x86_64',
    launcher: 'official',
    locale: 'zh-CN',
    timezone: 'Asia/Shanghai',
    memoryTier: '4G',
    riskScore: 12,
    riskLevel: 'info',
    protectionState: 'normal',
    labels: ['region:r1'],
    createdAt: '2025-06-01T03:20:00Z',
    updatedAt: '2025-06-18T09:15:00Z',
  },
  {
    id: 7002,
    channelId: 'survival-1',
    machineId: 'm-77b0e4c9f1a2',
    installId: 'inst-2d5e7f90',
    playerName: 'Alex',
    keyId: 8,
    keyPrefix: 'jmck_7f3c',
    firstSeen: '2025-06-09T11:02:00Z',
    lastSeen: '2025-06-18T09:16:10Z',
    lastIp: '198.51.100.7',
    userAgent: 'JMUpdater/1.2.0 (Windows 10; x86_64)',
    coreVersion: '1.20.1',
    wedgeVersion: '1.2.0',
    manifestVersion: 41,
    os: 'windows',
    osVersion: '10.0.19044',
    arch: 'x86_64',
    javaVendor: 'Oracle Corporation',
    javaVersion: '1.8.0_402',
    javaArch: 'x86_64',
    launcher: 'third-party',
    locale: 'zh-CN',
    timezone: 'Asia/Shanghai',
    memoryTier: '2G',
    riskScore: 46,
    riskLevel: 'warn',
    protectionState: 'throttled',
    labels: ['region:r1', 'watch'],
    createdAt: '2025-06-09T11:02:00Z',
    updatedAt: '2025-06-18T09:16:10Z',
  },
]

// ── 频道 / 密钥 / 版本：接入指引、版本面板、core 选择器、处置模态候选 ────────

/** 频道候选（三个处置模态的频道下拉与接入指引的目标频道）。 */
const DEMO_CHANNELS: ClientChannel[] = [
  {
    id: 1,
    channelId: 'survival-1',
    name: '生存服 · 主频道',
    description: '生存服整合包分发',
    currentVersion: 42,
    keyCount: 2,
    createdAt: '2025-05-01T00:00:00Z',
    updatedAt: '2025-06-15T09:30:00Z',
  },
  {
    id: 2,
    channelId: 'creative-1',
    name: '创造服 · 主频道',
    description: '创造服整合包分发',
    currentVersion: 8,
    keyCount: 1,
    createdAt: '2025-05-06T00:00:00Z',
    updatedAt: '2025-06-16T10:00:00Z',
  },
]

/** 密钥元数据（无明文）：一把可揭示、一把已吊销不可揭示。 */
const DEMO_PULL_KEYS: ClientPullKey[] = [
  {
    id: 7,
    name: '生存服发布密钥',
    keyPrefix: 'jmck_ab12',
    revoked: false,
    expiresAt: null,
    lastUsedAt: '2025-06-18T09:15:00Z',
    createdAt: '2025-05-02T01:10:00Z',
    revealable: true,
  },
  {
    id: 8,
    name: '旧启动器密钥（待轮换）',
    keyPrefix: 'jmck_7f3c',
    revoked: true,
    expiresAt: '2025-06-30T00:00:00Z',
    lastUsedAt: '2025-06-10T04:22:00Z',
    createdAt: '2025-05-02T01:12:00Z',
    revealable: false,
  },
]

/** 内嵌更新器 jar 信息（容器经 useUpdaterJarsInfo 注入）：wedge 与 core 均可用。 */
const DEMO_JARS: ClientUpdaterJarsInfo = {
  version: '1.4.0',
  coreVersion: '12',
  wedge: { available: true, size: 31_240 },
  core: { available: true, size: 8_421_376 },
}

/** 版本历史：latest、正常历史、无备注历史各一条。 */
const DEMO_VERSIONS: ClientVersionsPanelVersion[] = [
  { version: 42, note: '更新 mods 至 1.20.4', fileCount: 128, createdAt: '2025-06-15T09:30:00Z', isLatest: true },
  { version: 41, note: '回滚到 1.20.1 稳定集', fileCount: 126, createdAt: '2025-06-08T14:02:00Z', isLatest: false },
  { version: 40, note: '', fileCount: 124, createdAt: '2025-05-30T03:45:00Z', isLatest: false },
]

/** updater-core 归档版本：当前选定、可回滚、带 .dirty 标记的历史版本各一条。 */
const DEMO_CORE_VERSIONS: ClientUpdaterCoreVersion[] = [
  {
    version: 12,
    displayVersion: '12+9f3a1c2',
    gitCommit: '9f3a1c2',
    dirty: false,
    sha256: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
    size: 8_421_376,
    createdAt: '2025-06-15T08:00:00Z',
    selected: true,
  },
  {
    version: 11,
    displayVersion: '11+4c8d0e1',
    gitCommit: '4c8d0e1',
    dirty: false,
    sha256: 'ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100',
    size: 8_388_608,
    createdAt: '2025-06-02T05:30:00Z',
    selected: false,
  },
  {
    version: 10,
    displayVersion: '10+1a2b3c4.dirty',
    gitCommit: '1a2b3c4',
    dirty: true,
    sha256: '00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff',
    size: 8_355_840,
    createdAt: '2025-05-20T09:12:00Z',
    selected: false,
  },
]

/** 博物馆「业务视图 · 客户端分发」分区：11 个受控复合视图，数据经 props 注入，组件不取数、不碰路由。 */
export function ViewsClientDist() {
  return (
    <>
      <Panel title="ClientDistLogsTabView · 全量日志（加厚请求表 + 安全聚合表）">
        <div className="space-y-6">
          <ClientDistRequestLogsView
            type="request"
            onTypeChange={() => {}}
            link={{ machineId: 'm-3f8c21a0', errCode: 'INVALID_CLIENT_KEY' }}
            onClearLink={() => {}}
            events={DEMO_EVENTS}
            eventsLoading={false}
            eventsError={false}
            outcome={OPS_ALL}
            onOutcomeChange={() => {}}
            kind={OPS_ALL}
            onKindChange={() => {}}
            detailLoading={false}
            detailError={false}
            onDetailChange={() => {}}
            securityCenterHref="/client-dist/security?tab=logs&type=risk"
            channelWorkbenchHref="/client-channels/survival-1?tab=versions"
            renderLink={demoRenderLink}
            exportSlot={<SlotPlaceholder label="导出按钮（外壳插槽）" />}
          />
          <ClientDistAggregateLogsView
            type="risk"
            onTypeChange={() => {}}
            query={{ channelId: 'survival-1', machineId: 'm-77b0e4c9' }}
            onQueryChange={() => {}}
            playerName="Alex"
            onPlayerNameChange={() => {}}
            logs={DEMO_AGGREGATE_LOGS}
            logsLoading={false}
            logsError={false}
            exportSlot={<SlotPlaceholder label="导出按钮（外壳插槽）" />}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：请求事件行与聚合日志行、加载/错误态由外壳调 useClientDistEventSearch /
          useClientDistEventDetail / useClientDistSecurityLogs 注入；会触发重新取数的筛选（type / outcome / kind /
          路由查询串）经 props 受控，变更经 onXxxChange 上报。详情弹窗开合与当前查看的事件 id 留在组件内，
          仅上报 id 驱动外壳取详情。exportSlot 是导出按钮插槽（真实按钮由外壳接线），renderLink 是深链渲染插槽
          （外壳注入 router Link 保持 SPA 导航，此处以原生 a 占位）。样例里 OPS_ALL（__all__）是「不筛选」哨兵值。
        </p>
      </Panel>

      <Panel title="ClientIntegrationGuideView · 接入指引（含拉取密钥揭示边界）">
        <ClientIntegrationGuideView
          channelId="survival-1"
          keys={DEMO_PULL_KEYS}
          jars={DEMO_JARS}
          selectedKeyId="7"
          revealedKeyPlaintext="<demo-key>"
          revealing={false}
          onSelectKey={() => {}}
          onDownloadWedge={() => Promise.resolve()}
          onNotify={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：keys（密钥元数据）与 jars（内嵌更新器信息，经 useUpdaterJarsInfo 取数）由外壳注入；
          selectedKeyId 与 revealedKeyPlaintext 由外壳持有——明文的唯一来源是「用户主动选择某把密钥 + 后端 reveal
          成功」这一条路径，揭示时机完全由外壳控制，故样例用 &lt;demo-key&gt; 占位而不伪造真实密钥。
          onSelectKey / onDownloadWedge 上报动作，onNotify 上报轻提示（toast 由外壳发）。
          端点原文（表单草稿）与下载中标记留在组件内。
        </p>
      </Panel>

      <Panel title="ClientPublishPageView · 发布新版本向导">
        <PageFrame>
          <ClientPublishPageView
            channelId="survival-1"
            step="files"
            onStepChange={() => {}}
            publishing={false}
            progress={null}
            updaterInfo={{ version: '1.4.0', coreVersion: '12' }}
            previewTheme="light"
            onPublish={() => Promise.resolve({ ok: true })}
            onLeave={() => {}}
            onNotify={() => {}}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：步骤 step 由外壳持有并与 ?step= 双向同步，视图只经 onStepChange 上报；点「发布」时把草稿 +
          清理范围 + 备注 + 取消信号经 onPublish 交外壳（本地去重 / hash / 秒传预查 / 分块上传 / POST 全在外壳），
          批次进度经 progress 回流展示。草稿（含 zip 解包与拖拽摄入）、清理范围、备注、预览视图开关与放弃草稿守卫
          留在组件内。样例停在首步且未模拟在途批次（progress=null 即不渲染进度条）；页面壳是 flex-1 语义，
          故套 720px 裁剪框预览（与 views-admin 分区同款）。
        </p>
      </Panel>

      <Panel title="ClientStatsPanelView · 分发统计看板">
        <ClientStatsPanelView
          stats={DEMO_STATS}
          observability={DEMO_OBSERVABILITY}
          statsLoading={false}
          obsLoading={false}
          window={{ range: '7d' }}
          onWindowChange={() => {}}
          machineRankingSlot={<SlotPlaceholder label="机器更新排行容器（外壳取数）" />}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：请求侧统计与更新侧观测由外壳按窗口调 /client-dist/stats 与 /client-dist/observability 注入，
          加载态一并注入；时间窗 window 是外壳持有的受控值（它决定两个端点的查询参数），视图只经 onWindowChange
          上报选择。KPI 口径与空态判定留在组件内（更新侧率只信 observability，不拿 HTTP 成功率冒充）。
          machineRankingSlot 是应用侧机器排行取数容器的插槽（包内不 import 应用侧模块）。
        </p>
      </Panel>

      <Panel title="ClientUpdaterCoreSelectorView · updater-core 版本选择">
        <ClientUpdaterCoreSelectorView
          versions={DEMO_CORE_VERSIONS}
          loading={false}
          onSelect={() => {}}
          onUpload={() => Promise.resolve({ ok: true })}
          dangerAllowed
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：versions 由外壳调 useUpdaterCoreVersions 注入；行内「选定」只把目标 sha256 记进组件内的
          确认弹窗（弹窗开合属 UI 状态），确认后经 onSelect 上报（PUT 与成功/失败 toast 在外壳）。上传弹窗与表单草稿
          留在组件内，仅当 onUpload 回执 ok=true 才清空关窗、失败保留可重试。dangerAllowed 由外壳读登录态角色注入
          ——组件库不持鉴权状态，样例按「已放行」展示。
        </p>
      </Panel>

      <Panel title="ClientVersionsPanelView · 版本历史面板">
        <ClientVersionsPanelView
          versions={DEMO_VERSIONS}
          isLoading={false}
          detailVersion={null}
          onDetailVersionChange={() => {}}
          detailLoading={false}
          dangerAllowed
          onRollback={() => {}}
          onPublishNewVersion={() => {}}
          updaterSummarySlot={<SlotPlaceholder label="内嵌更新器摘要（外壳取数）" />}
          previewSlot={<SlotPlaceholder label="制品内容预览（外壳接线 FileBrowser）" />}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a+b 混合）：versions 与 detail 由外壳取数注入，detailVersion 是外壳持有的选中版本（它驱动详情取数），
          视图只上报开合意图；回滚经 onRollback 上报（useRollbackClientVersion 与 toast 在外壳），发布跳转经
          onPublishNewVersion 上报（路由语义在外壳）。回滚二次确认的目标与开合、详情弹窗的结构/预览视图切换留在组件内。
          updaterSummarySlot / previewSlot 是应用侧接线插槽（内嵌更新器摘要、制品内容浏览器）。样例停在列表态
          （detailVersion=null 不开弹窗）。
        </p>
      </Panel>

      <Panel title="OpsOverviewTabView · 运维总览">
        <OpsOverviewTabView
          stats={DEMO_STATS}
          runtime={DEMO_RUNTIME}
          observability={DEMO_OBSERVABILITY}
          obsLoading={false}
          obsError={false}
          security={DEMO_SECURITY_OVERVIEW}
          securityLoading={false}
          securityError={false}
          onLink={() => {}}
          rankLogsHref={() => '/client-dist?tab=logs&type=request'}
          renderLink={demoRenderLink}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：更新侧观测（useClientDistObservability）、安全态势（useClientDistSecurityOverview）与运行态 /
          请求侧数据均由外壳注入，加载与错误态分别驱动骨架卡与错误面板；分布面板下钻经 onLink 上报，排行深链目标由
          外壳按当前查询串构造（rankLogsHref），锚点渲染经 renderLink 注入（缺省退化为原生 a）。KPI 口径计算留在
          组件内（FR-356：更新侧率只信 observability，请求侧率只信 stats）。
        </p>
      </Panel>

      <Panel title="SecurityActionsTabView · 安全处置（流水 + 三个处置模态）">
        <SecurityActionsTabView
          actions={DEMO_ACTIONS}
          isLoading={false}
          isError={false}
          filterTarget=""
          onFilterTargetChange={() => {}}
          filterStatus=""
          onFilterStatusChange={() => {}}
          keyword=""
          onKeywordChange={() => {}}
          defaultChannelId="survival-1"
          channels={DEMO_CHANNELS}
          ipSuggestions={['198.51.100.7', '192.0.2.51']}
          onKeyChannelChange={() => {}}
          keys={DEMO_PULL_KEYS}
          onProtectionChannelChange={() => {}}
          protectionSummary={{
            channelId: 'survival-1',
            riskLevel: 'warn',
            abnormalRequests: 42,
            blockedIpCount: 3,
            restrictedKeyCount: 1,
            protectionMode: 'throttle',
            windowMinutes: 60,
          }}
          onBlockIp={() => Promise.resolve(true)}
          onSetKeyState={() => Promise.resolve(true)}
          onSetProtection={() => Promise.resolve(true)}
          onClearProtection={() => Promise.resolve(true)}
          onUnblock={() => Promise.resolve(true)}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a+b 双范式）：处置流水与加载/错误态（useClientDistSecurityActions）、三个筛选条件与路由频道
          defaultChannelId 归外壳；三个模态的开合与全部表单草稿留在组件内。改 key 态与频道防护模态的频道选择真源在
          视图内（表单草稿），仅在变化时上报给外壳镜像，以驱动密钥列表 / 频道安全摘要两路取数。四个写动作（封禁 IP /
          改 key 态 / 设置防护 / 清除防护）与行内解封经 onXxx 上报并返回成败，成功才关窗、失败保留输入便于重试；
          确认门禁属外壳职责，视图不在提交前追加或放宽。
        </p>
      </Panel>

      <Panel title="SecurityAnalysisTabsView · IP 剖析 / 玩家名剖析（同文件两个导出）">
        <div className="space-y-6">
          <IpAnalysisTabView rows={DEMO_IP_ROWS} isLoading={false} isError={false} />
          <PlayerAnalysisTabView rows={DEMO_PLAYER_ROWS} isLoading={false} isError={false} />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：两个只读聚合表的行与加载/错误态由外壳调 useClientDistIpAnalysis /
          useClientDistPlayerAnalysis 注入；组件只保留表格结构、列顺序、格式化与「错误优先于空态、加载仅无行时提示」
          的三态判定。本文件导出 IpAnalysisTabView 与 PlayerAnalysisTabView 两个组件（安全页的两个剖析 Tab）。
        </p>
      </Panel>

      <Panel title="SecurityGroupsTabView · 安全分组（导出名 GroupsTabView）">
        <GroupsTabView
          groups={DEMO_GROUPS}
          isLoading={false}
          isError={false}
          onCreate={() => Promise.resolve(true)}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：分组列表与加载/错误态由外壳调 useClientSecurityGroups 注入；创建动作经 onCreate 上报请求体，
          由返回值告知成败（成功才关模态、失败保留输入便于重试），成功/失败文案与 toast 属外壳决策。
          表单校验与「打开即重置」留在组件内（UI 状态）。注：源文件名是 SecurityGroupsTabView.tsx，但导出名是
          GroupsTabView，此处按实际导出名导入。
        </p>
      </Panel>

      <Panel title="SecurityProfilesTabView · 客户端画像（脱敏）">
        <SecurityProfilesTabView
          profiles={DEMO_PROFILES}
          isLoading={false}
          isError={false}
          query={{ channelId: 'survival-1' }}
          onQueryChange={() => {}}
          playerName=""
          onPlayerNameChange={() => {}}
          maskers={DEMO_MASKERS}
          detailLoading={false}
          detailError={false}
          onDetailChange={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：画像列表与详情、加载/错误态由外壳调 useClientDistSecurityProfiles /
          useClientDistSecurityProfile 注入；会触发重新取数的查询条件（channelId / machineId 来自路由查询串，
          playerName 为筛选）受控并上报。详情弹窗开合与「当前查看的画像 id」留在组件内，仅上报 id 驱动取详情。
          maskers 是脱敏函数插槽——样例用「前 3 位 + ***」的假实现，真实脱敏策略（maskPlayerName / maskMachineId /
          maskInstallId）由外壳按隐私策略注入，包内不持有明文展示策略。
        </p>
      </Panel>
    </>
  )
}
