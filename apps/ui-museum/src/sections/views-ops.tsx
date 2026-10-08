/**
 * @file 组件博物馆「业务视图 · 运维对象」分区。
 *
 * 登记的是一批**受控复合组件**（ADR-097 a/b 范式）：由设计系统原语拼装、带自身交互状态，
 * 但不取数、不发请求、不碰路由、不弹 toast——数据经 props 注入，动作经回调上报给外壳。
 * 因此这里每个面板都构造了有意义的样例数据，并把「哪些状态留在组件内、哪些归外壳」写在说明里。
 *
 * 环境约束：博物馆没有 react-router / react-query / sonner / zustand，本文件也不引用它们；
 * 路由跳转类插槽（页头入口）一律省略，插槽类 props 传包内真实实现或静态占位。
 */

import { useState } from 'react'

import { Panel } from '@jianmanager/ui'

import { AgentCallLogsPageView } from '@jianmanager/ui/components/views/agent/AgentCallLogsPageView'
import type {
  AgentCallLogRow,
  AgentTokenOption as AgentCallLogTokenOption,
} from '@jianmanager/ui/components/views/agent/AgentCallLogsPageView'
import { AgentTokensPageView } from '@jianmanager/ui/components/views/agent/AgentTokensPageView'
import type {
  AgentTokenOption,
  AgentTokenRow,
} from '@jianmanager/ui/components/views/agent/AgentTokensPageView'
import { McpActivityPageView } from '@jianmanager/ui/components/views/agent/McpActivityPageView'
import type { McpActivityRow } from '@jianmanager/ui/components/views/agent/McpActivityPageView'
import { ArtifactReconcileSectionView } from '@jianmanager/ui/components/views/artifacts/ArtifactReconcileSectionView'
import type {
  ArtifactChannelRefView,
  ArtifactReconcileRunView,
} from '@jianmanager/ui/components/views/artifacts/ArtifactReconcileSectionView'
import { ArtifactStoragesPageView } from '@jianmanager/ui/components/views/artifacts/ArtifactStoragesPageView'
import type { ArtifactStorageChannelView } from '@jianmanager/ui/components/views/artifacts/ArtifactStoragesPageView'
import { ArtifactVersionsPageView } from '@jianmanager/ui/components/views/artifacts/ArtifactVersionsPageView'
import type { ArtifactCatalogView } from '@jianmanager/ui/components/views/artifacts/ArtifactVersionsPageView'
import { BackupsPageView } from '@jianmanager/ui/components/views/backups/BackupsPageView'
import type { BackupStorageChoice } from '@jianmanager/ui/components/views/backups/BackupsPageView'
import { BackupStoragesPageView } from '@jianmanager/ui/components/views/backups/BackupStoragesPageView'
import type { BackupStorageRow } from '@jianmanager/ui/components/views/backups/BackupStoragesPageView'
import { LogsPageView } from '@jianmanager/ui/components/views/logs/LogsPageView'
import type { LogsFilterState } from '@jianmanager/ui/components/views/logs/LogsPageView'
import {
  NetworkInstancePickerView,
  NetworksPageView,
} from '@jianmanager/ui/components/views/networks/NetworksPageView'
import type {
  NetworkCandidateView,
  NetworkDetailView,
  NetworkSummaryView,
  NetworkView,
} from '@jianmanager/ui/components/views/networks/NetworksPageView'
import { PlayersPageView } from '@jianmanager/ui/components/views/players/PlayersPageView'
import type {
  PlayerEventRow,
  RosterEntry,
} from '@jianmanager/ui/components/views/players/PlayersPageView'
import {
  RuntimeAssetsInstancePickerView,
  RuntimeAssetsPageView,
} from '@jianmanager/ui/components/views/runtime-assets/RuntimeAssetsPageView'
import type {
  PluginBatchDeployResultView,
  RuntimeAssetsInstanceCandidateView,
  RuntimeAssetsOverviewView,
} from '@jianmanager/ui/components/views/runtime-assets/RuntimeAssetsPageView'
import { InstancePicker } from '@jianmanager/ui/components/views/instances/InstancePicker'

import type { AssetInfo } from '@jianmanager/ui/lib/asset-contracts'
import type { BackupInfo } from '@jianmanager/ui/lib/backup'
import type { LogEntry } from '@jianmanager/ui/lib/console-log-types'
import type { InstanceInfo } from '@jianmanager/ui/lib/instance-types'
import type { BanRecord, OnlinePlayersResult, WhitelistResult } from '@jianmanager/ui/lib/player'
import type {
  AssetTypeGroup,
  JDKMatrixItem,
  RuntimeMatrixEntry,
} from '@jianmanager/ui/lib/runtime-assets-contracts'

/**
 * 64 位十六进制摘要占位：全 0 + 序号。一眼可辨是示例值，不冒充任何真实文件的校验和。
 */
const demoDigest = (n: number) => String(n).padStart(64, '0')

// ── Agent 观测：调用流水 / Token 管理 / MCP 活动 ──

/** 调用流水的 Token 下拉候选（只有非明文前缀，后端从不返回明文）。 */
const AGENT_CALL_TOKEN_OPTIONS: AgentCallLogTokenOption[] = [
  { id: 1, name: '部署机器人（只读）', tokenPrefix: 'jm_agent_ab12…' },
  { id: 2, name: '巡检脚本', tokenPrefix: 'jm_agent_cd34…' },
]

/**
 * 调用流水样例行。
 * 来源地址用 RFC 5737 的文档专用网段（192.0.2.0/24），不是任何真实主机。
 */
const AGENT_CALL_ROWS: AgentCallLogRow[] = [
  {
    id: 9001,
    tokenId: 1,
    tokenName: '部署机器人（只读）',
    action: 'instance.list',
    client: 'mcp',
    transport: 'http',
    success: true,
    latencyMs: 42,
    ip: '192.0.2.10',
    createdAt: '2025-03-18T09:12:04Z',
  },
  {
    id: 9002,
    tokenId: 1,
    tokenName: '部署机器人（只读）',
    action: 'instance.get',
    client: 'mcp',
    transport: 'http',
    targetType: 'instance',
    targetId: '42',
    success: true,
    latencyMs: 37,
    ip: '192.0.2.10',
    createdAt: '2025-03-18T09:12:11Z',
  },
  {
    id: 9003,
    tokenId: 2,
    tokenName: '巡检脚本',
    action: 'instance.restart',
    client: 'jmagent',
    transport: 'http',
    targetType: 'instance',
    targetId: '42',
    success: false,
    error: '实例未绑定 JDK，拒绝重启',
    latencyMs: 512,
    ip: '192.0.2.24',
    createdAt: '2025-03-18T09:13:02Z',
  },
]

/** Token 列表样例：覆盖 active / expired / revoked 与 V2 能力、V1 写白名单两种策略形态。 */
const AGENT_TOKEN_ROWS: AgentTokenRow[] = [
  {
    id: 1,
    name: '部署机器人（只读）',
    tokenPrefix: 'jm_agent_ab12…',
    status: 'active',
    policyVersion: 2,
    capabilities: ['node.read', 'instance.read', 'observability.read'],
    writeAllowlist: [],
    scopedInstanceIds: [],
    scopedNodeIds: [],
    expiresAt: '2025-06-30T00:00:00Z',
    lastUsedAt: '2025-03-18T09:12:04Z',
    callCount24h: 128,
  },
  {
    id: 2,
    name: '应急运维（限单实例）',
    tokenPrefix: 'jm_agent_cd34…',
    status: 'expired',
    policyVersion: 2,
    capabilities: ['node.read', 'instance.life'],
    writeAllowlist: [],
    scopedInstanceIds: [42],
    scopedNodeIds: [1],
    expiresAt: '2025-03-01T00:00:00Z',
    lastUsedAt: null,
    callCount24h: 0,
  },
  {
    id: 3,
    name: '旧版脚本（V1 兼容）',
    tokenPrefix: 'jm_agent_ef56…',
    status: 'revoked',
    policyVersion: 1,
    capabilities: [],
    writeAllowlist: ['instance.life'],
    scopedInstanceIds: [],
    scopedNodeIds: [],
    expiresAt: '2024-12-31T00:00:00Z',
    lastUsedAt: '2024-11-02T18:20:00Z',
    callCount24h: 0,
  },
]

/** V2 能力候选（与后端能力枚举对齐，实际由外壳注入应用侧同一份映射表）。 */
const AGENT_CAPABILITY_OPTIONS: AgentTokenOption[] = [
  { value: 'node.read', labelKey: 'agentTokens.capability.nodeRead' },
  { value: 'node.operate', labelKey: 'agentTokens.capability.nodeOperate' },
  { value: 'instance.read', labelKey: 'agentTokens.capability.instanceRead' },
  { value: 'instance.life', labelKey: 'agentTokens.capability.instanceLife' },
  { value: 'instance.command', labelKey: 'agentTokens.capability.instanceCommand' },
  { value: 'observability.read', labelKey: 'agentTokens.capability.observabilityRead' },
  { value: 'bot.manage', labelKey: 'agentTokens.capability.botManage' },
]

/** V1 写白名单展示映射（仅历史行回显用）。 */
const AGENT_WRITE_ALLOWLIST_OPTIONS: AgentTokenOption[] = [
  { value: 'instance.life', labelKey: 'agentTokens.write.instanceLife' },
  { value: 'node.maintenance', labelKey: 'agentTokens.write.nodeMaintenance' },
]

/** MCP 活动聚合样例：两个 Token、各自的客户端构成与失败数。 */
const MCP_ACTIVITY_ROWS: McpActivityRow[] = [
  {
    tokenId: 1,
    tokenName: '部署机器人（只读）',
    tokenPrefix: 'jm_agent_ab12…',
    lastActivityAt: '2025-03-18T09:12:04Z',
    lastAction: 'instance.list',
    callCount: 128,
    failureCount: 2,
    clientIPs: ['192.0.2.10'],
    clients: { mcp: 120, curl: 8 },
  },
  {
    tokenId: 2,
    tokenName: '巡检脚本',
    tokenPrefix: 'jm_agent_cd34…',
    lastActivityAt: '2025-03-18T09:13:02Z',
    lastAction: 'instance.restart',
    callCount: 36,
    failureCount: 5,
    clientIPs: ['192.0.2.24'],
    clients: { jmagent: 36 },
  },
]

// ── 制品库：存储渠道 / 版本目录 / 一致性对账 ──

/** 制品渠道引用（对账区块用它筛出 S3 渠道）。 */
const ARTIFACT_CHANNEL_REFS: ArtifactChannelRefView[] = [
  { id: 1, name: '本机存储', type: 'local' },
  { id: 2, name: '主对象存储（S3）', type: 's3' },
]

/** 对账运行样例：一次成功（有差异待处置）+ 一次进行中。 */
const ARTIFACT_RECONCILE_RUNS: ArtifactReconcileRunView[] = [
  {
    id: 101,
    channelName: '主对象存储（S3）',
    status: 'succeeded',
    triggeredBy: 'scheduled',
    startedAt: '2025-03-18T03:00:00Z',
    indexCount: 412,
    objectCount: 415,
    matchedCount: 410,
    missingCount: 2,
    orphanCount: 3,
  },
  {
    id: 102,
    channelName: '主对象存储（S3）',
    status: 'running',
    triggeredBy: 'manual',
    startedAt: '2025-03-18T09:10:00Z',
    indexCount: 412,
    objectCount: 300,
    matchedCount: 300,
    missingCount: 0,
    orphanCount: 0,
  },
]

/**
 * 制品存储渠道样例。
 * 凭证边界：后端从不返回 Access Key / Secret Key，视图只收 hasAccessKey / hasSecretKey
 * 两个存在标志，故这里也不构造任何凭据字符串。
 */
const ARTIFACT_STORAGE_CHANNELS: ArtifactStorageChannelView[] = [
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
    active: false,
    builtin: true,
    hasAccessKey: false,
    hasSecretKey: false,
    lastTestAt: '2025-03-17T21:00:00Z',
    lastTestOk: true,
    lastTestMessage: '本机目录可写',
  },
  {
    id: 2,
    name: '主对象存储（S3）',
    type: 's3',
    endpoint: 'https://s3.example.com',
    bucket: 'jianmanager-artifacts',
    region: 'us-east-1',
    prefix: 'artifacts/',
    useSsl: true,
    presignTtlSeconds: 600,
    active: true,
    builtin: false,
    hasAccessKey: true,
    hasSecretKey: true,
    lastTestAt: '2025-03-18T08:00:00Z',
    lastTestOk: true,
    lastTestMessage: '连通性正常',
  },
]

/** 制品版本目录样例：线上来源 + 本地上传，覆盖「已缓存 / 未缓存」两种可操作性。 */
const ARTIFACT_CATALOG: ArtifactCatalogView = {
  package: { defaultVersionId: 21 },
  sources: [
    {
      id: 1,
      provider: 'github-release',
      name: 'ServerProbe',
      enabled: true,
      lastSyncedAt: '2025-03-18T02:00:00Z',
      lastError: '',
    },
    {
      id: 2,
      provider: 'local-upload',
      name: '本地上传',
      enabled: true,
      lastSyncedAt: null,
      lastError: '',
    },
  ],
  versions: [
    {
      id: 21,
      sourceId: 1,
      version: '1.4.0',
      expectedSha256: demoDigest(21),
      assetId: 501,
      lastError: '',
    },
    {
      id: 22,
      sourceId: 1,
      version: '1.5.0-rc1',
      expectedSha256: demoDigest(22),
      assetId: 0,
      lastError: '',
    },
    {
      id: 23,
      sourceId: 2,
      version: '1.4.1-hotfix',
      expectedSha256: demoDigest(23),
      assetId: 502,
      lastError: '上次缓存中断，可重试',
    },
  ],
}

// ── 备份：远程存储后端 / 备份列表 ──

/**
 * 备份存储后端样例。
 * 凭证边界：accessKeyEnv / secretKeyEnv 是 `${ENV_VAR}` **引用**而非明文，后端从不返回明文，
 * 故这里照该形态书写，不伪造任何真实密钥。
 */
const BACKUP_STORAGE_ROWS: BackupStorageRow[] = [
  {
    id: 1,
    name: '本地',
    type: 'local',
    endpoint: '',
    bucket: '',
    region: '',
    prefix: '',
    accessKeyEnv: '',
    secretKeyEnv: '',
    useSsl: false,
    lastTestAt: '2025-03-17T21:00:00Z',
    lastTestOk: true,
    lastTestMessage: '本机目录可写',
    backupCount: 128,
    usedBytes: 42 * 1024 ** 3,
  },
  {
    id: 2,
    name: '备份对象存储（S3）',
    type: 's3',
    endpoint: 'https://s3.example.com',
    bucket: 'jianmanager-backups',
    region: 'us-east-1',
    prefix: 'backups/',
    accessKeyEnv: '${JIANMANAGER_BACKUP_S3_AK}',
    secretKeyEnv: '${JIANMANAGER_BACKUP_S3_SK}',
    useSsl: true,
    lastTestAt: '2025-03-18T08:00:00Z',
    lastTestOk: true,
    lastTestMessage: '连通性正常',
    backupCount: 36,
    usedBytes: 8.5 * 1024 ** 3,
  },
]

/** 备份存储位置下拉候选（本地 + 一个远程后端）。 */
const BACKUP_STORAGE_CHOICES: BackupStorageChoice[] = [
  { id: 1, name: '本地' },
  { id: 2, name: '备份对象存储（S3）' },
]

/** 备份列表样例：全量 + 增量（串成备份链）+ 一条进行中。 */
const BACKUP_ROWS: BackupInfo[] = [
  {
    id: 7001,
    uuid: 'bk-0001',
    instanceId: 42,
    name: 'survival-main 全量 2025-03-18',
    filePath: '/var/lib/jianmanager/backups/survival-main/full-20250318.tar.zst',
    fileSizeMb: 812.5,
    type: 0,
    mode: 0,
    status: 2,
    createdAt: '2025-03-18T03:05:00Z',
  },
  {
    id: 7002,
    uuid: 'bk-0002',
    instanceId: 42,
    name: 'survival-main 增量 2025-03-18',
    filePath: 'backups/survival-main/inc-20250318.tar.zst',
    fileSizeMb: 96.25,
    type: 1,
    mode: 1,
    parentId: 7001,
    status: 2,
    storageId: 2,
    storageKey: 'backups/survival-main/inc-20250318.tar.zst',
    checksum: demoDigest(7002),
    checksumAlgo: 'sha256',
    createdAt: '2025-03-18T09:00:00Z',
  },
  {
    id: 7003,
    uuid: 'bk-0003',
    instanceId: 42,
    name: 'survival-main 全量 2025-03-18（进行中）',
    filePath: '/var/lib/jianmanager/backups/survival-main/full-20250318b.tar.zst',
    fileSizeMb: 0,
    type: 0,
    mode: 0,
    status: 1,
    createdAt: '2025-03-18T09:14:00Z',
  },
]

// ── 日志中心 ──

/**
 * 日志样例行。
 * 正文一律为示例文本：不含真实 IP、玩家名或其他可识别标识；实例日志取原版服务端的通用提示语。
 */
const LOG_ROWS: LogEntry[] = [
  {
    id: 1,
    source: 'instance',
    level: 'info',
    instanceId: 42,
    instanceUuid: 'inst-0042',
    nodeId: 1,
    stream: 'stdout',
    message: 'Done (1.234s)! For help, type "help"',
    time: '2025-03-18T09:00:00Z',
  },
  {
    id: 2,
    source: 'instance',
    level: 'warn',
    instanceId: 42,
    instanceUuid: 'inst-0042',
    nodeId: 1,
    stream: 'stdout',
    message: "Can't keep up! Is the server overloaded? Running 2048ms behind",
    time: '2025-03-18T09:01:12Z',
  },
  {
    id: 3,
    source: 'worker',
    level: 'error',
    instanceId: 42,
    instanceUuid: 'inst-0042',
    nodeId: 1,
    stream: 'stderr',
    message: '实例进程以退出码 1 结束（示例日志，非真实运行记录）',
    time: '2025-03-18T09:02:40Z',
  },
  {
    id: 4,
    source: 'control_plane',
    level: 'info',
    instanceId: 0,
    instanceUuid: '',
    nodeId: 0,
    message: '调度任务 task-9f3c 已完成，耗时 3.2s',
    time: '2025-03-18T09:03:00Z',
  },
]

/** 日志中心的受控筛选态（全部维度都是查询键，实际由外壳持有）。 */
const LOGS_FILTER: LogsFilterState = {
  source: '',
  level: '',
  nodeId: null,
  instanceId: null,
  keyword: '',
  range: '24h',
}

/** 节点下拉候选。 */
const LOG_NODE_OPTIONS = [
  { id: 1, name: 'node-shanghai-1' },
  { id: 2, name: 'node-beijing-1' },
]

/** 实例选择器候选窗口（外壳按服务端搜索注入，这里给静态示例）。 */
const LOG_INSTANCE_CANDIDATES = [
  { id: 42, uuid: 'inst-0042', name: 'survival-main' },
  { id: 43, uuid: 'inst-0043', name: 'survival-resource' },
]

// ── 群组（Network 软标签） ──

/** 群组列表样例：一个健康、一个含崩溃成员。 */
const NETWORK_SUMMARIES: NetworkSummaryView[] = [
  {
    id: 1,
    name: '生存服主群组',
    description: '主城 + 资源世界 + 下界',
    memberCount: 3,
    memberStatus: { running: 2, stopped: 1, crashed: 0, starting: 0, stopping: 0 },
  },
  {
    id: 2,
    name: '活动服群组',
    description: '限时活动，活动结束即下线',
    memberCount: 2,
    memberStatus: { running: 1, stopped: 0, crashed: 1, starting: 0, stopping: 0 },
  },
]

/** 详情面板样例（detailId=1 时渲染）。 */
const NETWORK_DETAIL: NetworkDetailView = {
  id: 1,
  name: '生存服主群组',
  members: [
    { instanceId: 10, name: 'lobby-proxy', role: 'proxy', status: 'RUNNING' },
    { instanceId: 42, name: 'survival-main', role: 'backend', status: 'RUNNING' },
    { instanceId: 43, name: 'survival-resource', role: 'backend', status: 'STOPPED' },
  ],
}

/** 可加入群组的实例候选。 */
const NETWORK_CANDIDATES: NetworkCandidateView[] = [
  { id: 44, name: 'survival-nether', status: 'RUNNING', nodeId: 1, role: 'backend', serverPort: 25568 },
  { id: 45, name: 'minigame-1', status: 'STOPPED', nodeId: 2, role: 'backend', serverPort: 25570 },
]

/** 候选行显示所属节点名。 */
const NETWORK_NODE_REFS = [
  { id: 1, name: 'node-shanghai-1' },
  { id: 2, name: 'node-beijing-1' },
]

// ── 玩家管理 ──

/** 实时事件与白名单两个 Tab 的后端子服候选（完整 InstanceInfo，外壳取数后注入）。 */
const PLAYER_BACKEND_INSTANCES: InstanceInfo[] = [
  {
    id: 42,
    uuid: 'inst-0042',
    nodeId: 1,
    name: 'survival-main',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'direct',
    status: 'RUNNING',
    startCommand: 'java -Xms2G -Xmx4G -jar server.jar nogui',
    workDir: '/opt/jianmanager/instances/survival-main',
    serverPort: 25566,
    queryPort: 25576,
    probePort: 29100,
    autoStart: true,
    autoRestart: true,
    tags: ['env:prod', 'region:r1'],
    createdAt: '2025-01-06T10:00:00Z',
  },
  {
    id: 43,
    uuid: 'inst-0043',
    nodeId: 1,
    name: 'survival-resource',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'direct',
    status: 'RUNNING',
    startCommand: 'java -Xms1G -Xmx2G -jar server.jar nogui',
    workDir: '/opt/jianmanager/instances/survival-resource',
    serverPort: 25567,
    queryPort: 0,
    probePort: 29101,
    autoStart: true,
    autoRestart: false,
    tags: ['env:prod'],
    createdAt: '2025-01-06T10:05:00Z',
  },
]

/** 在线玩家聚合样例：一个后端探针可用、一个不可用（触发降级提示）。 */
const PLAYER_ONLINE: OnlinePlayersResult = {
  players: [
    { name: 'Steve', instanceId: 42, instanceName: 'survival-main' },
    { name: 'Alex', instanceId: 42, instanceName: 'survival-main' },
    { name: 'Nova', instanceId: 43, instanceName: 'survival-resource' },
  ],
  backends: [
    { instanceId: 42, instanceName: 'survival-main', available: true },
    {
      instanceId: 43,
      instanceName: 'survival-resource',
      available: false,
      error: '探针未连接，该后端名册缺失',
    },
  ],
}

/** 实时在线名册（订阅结果）。 */
const PLAYER_ROSTER: RosterEntry[] = [
  { name: 'Steve', server: 'survival-main' },
  { name: 'Alex', server: 'survival-resource' },
]

/** 实时事件流样例（时间为 Unix 秒）。 */
const PLAYER_EVENTS: PlayerEventRow[] = [
  { type: 'player_join', timestamp: 1742292000, playerName: 'Steve', server: 'survival-main' },
  { type: 'chat', timestamp: 1742292030, playerName: 'Alex', message: '大家好（示例消息）', server: 'survival-main' },
  { type: 'cross_server', timestamp: 1742292060, playerName: 'Alex', fromServer: 'survival-main', toServer: 'survival-resource' },
]

/** 封禁记录样例（玩家名为虚构示例）。 */
const PLAYER_BANS: BanRecord[] = [
  {
    id: 1,
    uuid: '00000000-0000-0000-0000-000000000001',
    playerName: 'ExampleGriefer',
    reason: '破坏他人建筑（示例记录）',
    scope: 'global',
    scopeId: 0,
    operatorId: 1,
    active: true,
    createdAt: '2025-03-10T12:00:00Z',
    unbannedAt: null,
    operator: { id: 1, username: 'admin' },
  },
]

/** 白名单查询结果样例。 */
const PLAYER_WHITELIST: WhitelistResult = {
  instanceId: 42,
  available: true,
  players: ['Steve', 'Alex'],
}

// ── 运行时与制品 ──

/** 跨节点 JDK 矩阵样例：一台在线节点 + 一台离线节点。 */
const RUNTIME_JDK_MATRIX: JDKMatrixItem[] = [
  {
    id: 1,
    nodeId: 1,
    nodeName: 'node-shanghai-1',
    nodeOnline: true,
    vendor: 'Eclipse Temurin',
    majorVersion: 21,
    version: '21.0.4+7',
    arch: 'x86_64',
    path: '/opt/jdks/temurin-21',
    managed: true,
    instances: [
      { id: 42, uuid: 'inst-0042', name: 'survival-main', status: 'RUNNING', binding: 'direct' },
    ],
    refCount: 1,
  },
  {
    id: 2,
    nodeId: 2,
    nodeName: 'node-beijing-1',
    nodeOnline: false,
    vendor: 'Amazon Corretto',
    majorVersion: 17,
    version: '17.0.12+7',
    arch: 'x86_64',
    path: '/opt/jdks/corretto-17',
    managed: false,
    instances: [],
    refCount: 0,
  },
]

/** 制品条目样例（core / plugin 各一条）。 */
const RUNTIME_ASSET_CORE: AssetInfo = {
  id: 501,
  type: 'core',
  name: 'Paper',
  version: '1.21.4',
  filename: 'paper-1.21.4.jar',
  sha256: demoDigest(501),
  md5: 'demo-md5-501',
  size: 52_428_800,
  contentType: 'application/java-archive',
  sourceUrl: 'https://example.com/paper-1.21.4.jar',
  metadata: '',
  storageState: 'hot',
  storageBackend: 's3',
  storageChannelId: 2,
  refCount: 1,
  relPath: 'core/paper-1.21.4.jar',
  createdAt: '2025-02-01T08:00:00Z',
  lastUsedAt: '2025-03-18T09:00:00Z',
}

const RUNTIME_ASSET_PLUGIN: AssetInfo = {
  id: 502,
  type: 'plugin',
  name: 'ServerProbe',
  version: '1.4.1-hotfix',
  filename: 'serverprobe-1.4.1-hotfix.jar',
  sha256: demoDigest(502),
  md5: 'demo-md5-502',
  size: 1_572_864,
  contentType: 'application/java-archive',
  sourceUrl: '',
  metadata: '',
  storageState: 'archived',
  storageBackend: 'local',
  storageChannelId: 1,
  refCount: 0,
  relPath: 'plugin/serverprobe-1.4.1-hotfix.jar',
  createdAt: '2025-03-01T08:00:00Z',
  lastUsedAt: null,
}

/** 制品按类型分组样例。 */
const RUNTIME_ASSET_GROUPS: AssetTypeGroup[] = [
  {
    type: 'core',
    items: [RUNTIME_ASSET_CORE],
    count: 1,
    totalSize: RUNTIME_ASSET_CORE.size,
    referencedCount: 1,
    hotCount: 1,
    archivedCount: 0,
    externalCount: 0,
    lostCount: 0,
  },
  {
    type: 'plugin',
    items: [RUNTIME_ASSET_PLUGIN],
    count: 1,
    totalSize: RUNTIME_ASSET_PLUGIN.size,
    referencedCount: 0,
    hotCount: 0,
    archivedCount: 1,
    externalCount: 0,
    lostCount: 0,
  },
]

/** 多运行时矩阵样例：jdk + nodejs 两类（FR-301 加性扩展）。 */
const RUNTIME_MATRIX: RuntimeMatrixEntry[] = [
  {
    id: 1,
    nodeId: 1,
    nodeName: 'node-shanghai-1',
    nodeOnline: true,
    type: 'jdk',
    name: 'Eclipse Temurin',
    majorVersion: 21,
    version: '21.0.4+7',
    arch: 'x86_64',
    path: '/opt/jdks/temurin-21',
    managed: true,
    instances: [
      { id: 42, uuid: 'inst-0042', name: 'survival-main', status: 'RUNNING', binding: 'direct' },
    ],
    refCount: 1,
  },
  {
    id: 2,
    nodeId: 1,
    nodeName: 'node-shanghai-1',
    nodeOnline: true,
    type: 'nodejs',
    name: 'Node.js',
    majorVersion: 20,
    version: '20.18.1',
    arch: 'x86_64',
    path: '/opt/nodejs/20.18.1',
    managed: true,
    instances: [],
    refCount: 0,
  },
]

/** 运行时与制品聚合载荷样例。 */
const RUNTIME_OVERVIEW: RuntimeAssetsOverviewView = {
  jdks: RUNTIME_JDK_MATRIX,
  jdkSummary: { nodeCount: 2, jdkCount: 2, referencedJdk: 1, instanceRefs: 1 },
  assets: RUNTIME_ASSET_GROUPS,
  assetSummary: {
    assetCount: 2,
    totalSize: RUNTIME_ASSET_CORE.size + RUNTIME_ASSET_PLUGIN.size,
    referencedCount: 1,
    hotCount: 1,
    archivedCount: 1,
    externalCount: 0,
    lostCount: 0,
  },
  runtimes: RUNTIME_MATRIX,
  artifactChannels: [
    { id: 1, name: '本机存储', type: 'local' },
    { id: 2, name: '主对象存储（S3）', type: 's3' },
  ],
  syncedAt: '2025-03-18T08:30:00Z',
}

/** 批量部署回执样例：2 成功 / 1 失败。 */
const RUNTIME_DEPLOY_RESULT: PluginBatchDeployResultView = {
  success: 2,
  skipped: 0,
  failed: 1,
  results: [
    { id: 42, name: 'survival-main' },
    { id: 43, name: 'survival-resource' },
    { id: 44, name: 'minigame-1', error: '目标目录不可写' },
  ],
}

/** 批量部署的实例候选（外壳按服务端搜索注入，这里给静态示例）。 */
const RUNTIME_INSTANCE_CANDIDATES: RuntimeAssetsInstanceCandidateView[] = [
  { id: 42, name: 'survival-main', status: 'RUNNING', nodeId: 1 },
  { id: 43, name: 'survival-resource', status: 'RUNNING', nodeId: 1 },
  { id: 44, name: 'minigame-1', status: 'STOPPED', nodeId: 2 },
]

/**
 * 博物馆「业务视图 · 运维对象」分区：12 个受控复合组件的真实渲染样例。
 *
 * 所有写动作都以空实现上报——真正的 mutation、成功/失败提示、路由跳转与重新取数策略
 * 都在外壳（应用侧接线层）里完成，博物馆没有那套运行时，故只展示视图自身的呈现与本地交互态。
 */
export function ViewsOps() {
  /*
   * 受控样例状态：博物馆在这里扮演「外壳」。
   *
   * 群组视图与详情开合在真实应用里由路由 / 查询参数派生（`?view=` 与 `?network=`），这里用 useState 代持。
   * **绝不能用固定值代替**：详情面板的 open 完全由 `detailId` 决定（本体不持 open），传常量 `1` 就等于
   * 把详情钉死——点「关闭」、按 Esc 都改不动它，弹出来就关不掉。这类「受控值恒定 + 回调空函数」的组合
   * 会让样例看起来正常、一交互就卡死，是本分区登记时踩过的坑。
   */
  const [networksView, setNetworksView] = useState<NetworkView>('list')
  const [networkDetailId, setNetworkDetailId] = useState<number | null>(1)

  return (
    <>
      <Panel title="AgentCallLogsPageView · Agent 调用流水">
        <AgentCallLogsPageView
          query={{ tokenId: '', action: '', client: '', success: '', page: 1 }}
          onQueryChange={() => {}}
          onClear={() => {}}
          tokens={AGENT_CALL_TOKEN_OPTIONS}
          items={AGENT_CALL_ROWS}
          total={AGENT_CALL_ROWS.length}
          pageSize={20}
          isLoading={false}
          isError={false}
          isFetching={false}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：筛选与分页都是查询键，由外壳持有；视图不发请求，只把逐字段改动经
          onQueryChange 上报，「改筛选即回第 1 页」的复位策略也归外壳。空/加载/错误三态的判定优先级
          留在组件内。来源地址用 RFC 5737 文档网段（192.0.2.0/24），非真实主机。
        </p>
      </Panel>

      <Panel title="AgentTokensPageView · Agent Token 管理（含一次性明文）">
        <AgentTokensPageView
          tokens={AGENT_TOKEN_ROWS}
          isLoading={false}
          isError={false}
          revokeAllowed
          notify={() => {}}
          capabilityOptions={AGENT_CAPABILITY_OPTIONS}
          defaultCapabilities={['node.read', 'instance.read', 'observability.read']}
          writeAllowlistOptions={AGENT_WRITE_ALLOWLIST_OPTIONS}
          instanceOptions={[
            { id: 42, name: 'survival-main', status: 'RUNNING' },
            { id: 43, name: 'survival-resource', status: 'STOPPED' },
          ]}
          nodeOptions={[
            { id: 1, name: 'node-shanghai-1' },
            { id: 2, name: 'node-beijing-1' },
          ]}
          onCreateDialogOpenChange={() => {}}
          onIssue={() =>
            Promise.resolve({
              name: '演示 Token（未落库）',
              // 占位明文：只为演示「一次性展示」交互，不是任何环境真实签发过的密钥。
              plaintext: 'jm_agent_demo_0000000000000000',
            })
          }
          onRevoke={() => Promise.resolve(true)}
          mcpUrl="https://jm.example.com/mcp"
          onCopyResult={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：列表与三态、能力候选映射表、实例/节点候选、MCP 基址全部注入；签发与吊销
          经回调上报，mutation 与提示文案由外壳执行。敏感边界：列表只展示后端给出的
          <span className="font-medium">非明文前缀</span>（jm_agent_ab12…）；一次性明文只在签发响应里
          由外壳传入视图，写进展示对话框后关窗即清空，视图不提供任何回取/重放入口。签发对话框开合、
          表单草稿、吊销确认目标是留在组件内的纯 UI 状态，其中「对话框打开」只单向通知外壳以收敛取数时机。
        </p>
      </Panel>

      <Panel title="McpActivityPageView · MCP 调用活动">
        <McpActivityPageView
          window="24h"
          onWindowChange={() => {}}
          items={MCP_ACTIVITY_ROWS}
          generatedAt="2025-03-18T09:15:00Z"
          isLoading={false}
          isError={false}
          isFetching={false}
          onRefresh={() => {}}
          endpoint="https://jm.example.com/mcp"
          onCopyEndpoint={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：统计窗口档位是受控值（切档会改查询键并重新取数，故 state 归外壳）；取数、
          手动刷新、端点基址推导与复制结果 toast 全在外壳。视图只留展示——档位选中态、端点说明、
          表格与三态判定优先级。客户端地址同为文档网段示例值。
        </p>
      </Panel>

      <Panel title="ArtifactReconcileSectionView · 制品索引与对象存储对账">
        <ArtifactReconcileSectionView
          channels={ARTIFACT_CHANNEL_REFS}
          runs={ARTIFACT_RECONCILE_RUNS}
          settings={{ enabled: true, intervalHours: 24, nextRunAt: '2025-03-19T03:00:00Z' }}
          onSaveSettings={() => Promise.resolve(true)}
          onTrigger={() => {}}
          onOpenReport={() => {}}
          onCloseReport={() => {}}
          onMissingPageChange={() => {}}
          onOrphanPageChange={() => {}}
          onResolveMissing={() => Promise.resolve(true)}
          onCleanupOrphans={() => Promise.resolve(true)}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：设置、运行列表、差异分页数据与全部在途标志注入；保存设置、立即触发、缺失标记失效、
          孤儿清理都以回调上报，由外壳执行 mutation 并决定提示。会触发重新取数的「报告弹窗页码」
          与「当前看哪次运行」归外壳，故这里未打开报告弹窗（点「查看报告」需外壳注入 report 才渲染）；
          弹窗开合、设置草稿与待确认侧留在组件内。无 S3 渠道时「立即运行」按设计禁用。
        </p>
      </Panel>

      <Panel title="ArtifactStoragesPageView · 制品存储渠道">
        <ArtifactStoragesPageView
          channels={ARTIFACT_STORAGE_CHANNELS}
          migrationTask={{ taskId: 'task-9f3c', state: 'succeeded', progress: 100, cancelRequested: false }}
          migrationInfo={{
            targetChannelId: 2,
            targetName: '主对象存储（S3）',
            total: 412,
            migrated: 410,
            failed: 2,
            skipped: 0,
          }}
          onSubmit={() => Promise.resolve(true)}
          onTestDraft={() => Promise.resolve({ ok: true, message: '连通性正常（示例结论）' })}
          onTest={() => {}}
          onActivate={() => {}}
          onDelete={() => {}}
          onStartMigration={() => {}}
          onStopMigration={() => {}}
          onOpenFailures={() => {}}
          onCloseFailures={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：渠道列表、迁移任务与计数、失败明细注入；提交表单与草稿测试回传 Promise，
          视图据此决定「成功才关窗清草稿」，失败时保留草稿便于重试。敏感边界：
          <span className="font-medium">后端从不返回 Access Key / Secret Key</span>
          ——视图只收 hasAccessKey / hasSecretKey 两个存在标志，编辑态留空即「保留原值」，故此处
          不构造也不回显任何凭据。失败明细模态的打开状态（failuresTaskId）同时是查询键，归外壳，
          所以本样例保持关闭；表单草稿、四个对话框开合与字段门控留在组件内。
        </p>
      </Panel>

      <Panel title="ArtifactVersionsPageView · 制品版本目录">
        <ArtifactVersionsPageView
          catalog={ARTIFACT_CATALOG}
          onSync={() => {}}
          onUpload={() => Promise.resolve(true)}
          onCache={() => {}}
          onSetGlobalDefault={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：目录（包 + 来源 + 版本）与加载/错误态注入；同步、上传、缓存、设默认四个写动作以回调
          上报，由外壳执行 mutation 并决定成败文案。上传的版本号草稿与文件选择是纯 UI 状态，留在
          组件内——onUpload 回传 Promise 是因为「成功才清空表单」。校验和用全 0 序号占位，非真实摘要。
        </p>
      </Panel>

      <Panel title="BackupStoragesPageView · 备份远程存储后端">
        <BackupStoragesPageView
          storages={BACKUP_STORAGE_ROWS}
          dangerAllowed
          onSubmit={() => Promise.resolve(true)}
          onTestDraft={() => Promise.resolve({ ok: true, message: '连通性正常（示例结论）' })}
          onTest={() => {}}
          onDelete={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：列表与加载态注入，四个写动作以回调上报（提交与草稿测试回传 Promise，
          视图据返回值决定是否复位本地编辑态）；行内测试按钮只认外壳注入的 rowTestingId，因为
          只有外壳知道 mutation 在测哪一行。敏感边界：凭证是
          <span className="font-medium">{'${ENV_VAR}'} 环境变量引用</span>
          而非明文，后端从不返回明文，故样例照该形态书写、不伪造密钥；编辑对话框、表单草稿、
          编辑目标与字段门控留在组件内。
        </p>
      </Panel>

      <Panel title="BackupsPageView · 实例备份列表">
        <BackupsPageView
          instanceId={42}
          onInstanceChange={() => {}}
          renderInstancePicker={({ value, onChange }) => (
            <InstancePicker
              value={value}
              onChange={(next) => onChange(next)}
              items={LOG_INSTANCE_CANDIDATES}
              valueLabel="survival-main"
              ariaLabel="选择实例"
            />
          )}
          storages={BACKUP_STORAGE_CHOICES}
          backups={BACKUP_ROWS}
          backupsActive
          instanceLive
          onCreate={() => {}}
          onRestore={() => Promise.resolve()}
          onDelete={() => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：备份列表与存储位置注入；
          <span className="font-medium">instanceId 是列表与实例详情查询的键</span>
          ，一变即重新取数，故归外壳；轮询启停也由外壳按「是否有进行中备份」决定，视图只按
          backupsActive 显示刷新提示。写动作以回调上报（恢复回传 Promise 供视图在请求结束后关窗）。
          实例选择器走 renderInstancePicker 插槽——候选要服务端搜索、千级实例不能一次拉全量，属外壳
          策略；这里给包内 InstancePicker 的静态候选实现。卡片/列表切换、创建目标存储、恢复与删除的
          确认目标留在组件内。本样例实例存活，故恢复按钮按后端同一守卫禁用。
        </p>
      </Panel>

      <Panel title="LogsPageView · 日志中心（虚拟滚动）">
        <LogsPageView
          items={LOG_ROWS}
          total={LOG_ROWS.length}
          hasData
          page={1}
          totalPages={1}
          view="node_instance"
          filter={LOGS_FILTER}
          nodes={LOG_NODE_OPTIONS}
          isPlatformAdmin
          onChangeFilter={() => {}}
          onChangeView={() => {}}
          onToggleFollow={() => {}}
          onPrevPage={() => {}}
          onNextPage={() => {}}
          onExport={() => {}}
          onFederationAction={() => {}}
          onRetry={() => {}}
          renderInstancePicker={({ value, onChange, disabled }) => (
            <InstancePicker
              value={value}
              onChange={(next) => onChange(next)}
              items={LOG_INSTANCE_CANDIDATES}
              allowAll
              allLabel="全部实例"
              ariaLabel="实例筛选"
              disabled={disabled}
            />
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：行数据、总数与三态注入；筛选态、主视图、页码与实时跟随都是查询键，
          「改筛选/改视图/翻页/开关跟随都回第 1 页并清联邦游标」由外壳在一处落实，视图只上报意图。
          留组件内的纯 UI 状态只有时间线 scrollTop 与跟随态自动回顶——它们不产生请求。实例筛选走
          renderInstancePicker 插槽（候选来自服务端搜索）。本样例走经典路径（未注入 federation），
          故不渲染联邦横幅与洞察栏。日志正文为示例文本，不含真实 IP 与玩家标识；虚拟滚动窗口高度
          是包内常量（460px），不依赖外部 DOM 尺寸，嵌在面板里也能正常渲染。
        </p>
      </Panel>

      <Panel title="NetworksPageView · 群组列表 + 详情双栏">
        <NetworksPageView
          view={networksView}
          onViewChange={setNetworksView}
          networks={NETWORK_SUMMARIES}
          detailId={networkDetailId}
          detail={NETWORK_DETAIL}
          onOpenDetail={setNetworkDetailId}
          onCloseDetail={() => setNetworkDetailId(null)}
          onCreate={() => Promise.resolve(true)}
          onDelete={() => {}}
          onAddMembers={() => Promise.resolve(true)}
          onRemoveMember={() => Promise.resolve()}
          onBatchAction={() => {}}
          renderTopology={() => (
            <div className="rounded-md border border-dashed p-6 text-center text-xs text-muted-foreground">
              拓扑图本体与其取数接线层在外壳（应用侧 useTopology），包内不引用它——
              此处为静态占位，切换「拓扑」视图时由外壳注入真实拓扑。
            </div>
          )}
          renderInstancePicker={({ selected, onToggle, memberIds, onAdd, adding }) => (
            <NetworkInstancePickerView
              items={NETWORK_CANDIDATES}
              total={NETWORK_CANDIDATES.length}
              selected={selected}
              onToggle={onToggle}
              memberIds={memberIds}
              nodes={NETWORK_NODE_REFS}
              onQueryChange={() => {}}
              onAdd={onAdd}
              adding={adding}
            />
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：列表、详情与加载态注入；onCreate / onAddMembers 回传 Promise（成功才关窗、
          才清空勾选），onRemoveMember 回传 Promise 供视图落定后收起二次确认。归外壳的受控状态是
          view 与 detailId——两者由路由/查询参数派生，是「页面在看什么」的导航语义（本样例固定为
          列表 + detailId=1）。留组件内的是新建弹窗与草稿、删除目标、勾选集合与移除确认。两个插槽把
          应用侧策略留在外壳：renderTopology（拓扑接线层自行取数）与 renderInstancePicker（候选走
          服务端搜索）——后者这里接了包内的多选 NetworkInstancePickerView。
        </p>
      </Panel>

      <Panel title="PlayersPageView · 玩家管理（在线 Tab）">
        <PlayersPageView
          tab="online"
          onTabChange={() => {}}
          online={PLAYER_ONLINE}
          onPlayerAction={() => Promise.resolve()}
          liveInstances={PLAYER_BACKEND_INSTANCES}
          liveInstanceId={42}
          onLiveInstanceChange={() => {}}
          probeConnected
          roster={PLAYER_ROSTER}
          events={PLAYER_EVENTS}
          bans={PLAYER_BANS}
          bansActiveOnly
          onBansActiveOnlyChange={() => {}}
          unbanAllowed
          onUnban={() => Promise.resolve(true)}
          whitelistBackends={PLAYER_BACKEND_INSTANCES}
          whitelistInstanceId={42}
          onWhitelistInstanceChange={() => {}}
          whitelist={PLAYER_WHITELIST}
          onRetryWhitelist={() => {}}
          onWhitelistAdd={() => Promise.resolve(true)}
          onWhitelistRemove={() => Promise.resolve()}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：tab 是受控值（外壳据此门控各 Tab 的查询与订阅），本样例固定停在「在线」——切换、
          换订阅、解封与白名单增删都由外壳执行，视图只上报意图；踢出/封禁的循环提交与成败汇总也在
          外壳。留组件内的是子服筛选与勾选集合、踢/封确认弹窗与原因草稿、事件面板的筛选/暂停快照、
          解封待确认目标与白名单输入草稿。解封门禁 scope 固定 group，unbanAllowed 由外壳读角色注入。
          样例里有一个后端探针不可用，用于展示降级提示；玩家名与封禁理由均为虚构示例。
        </p>
      </Panel>

      <Panel title="RuntimeAssetsPageView · 运行时与制品全局页">
        <RuntimeAssetsPageView
          overview={RUNTIME_OVERVIEW}
          isLoading={false}
          isError={false}
          onRefreshRuntimes={() => {}}
          refreshing={false}
          onDeleteJdk={() => {}}
          deletingJdkId={null}
          onDeleteAsset={() => {}}
          deletingAssetId={null}
          onImportAsset={() => Promise.resolve(true)}
          importing={false}
          onBatchDeploy={() => {}}
          deploying={false}
          deployResult={RUNTIME_DEPLOY_RESULT}
          renderInstancePicker={({ selected, onToggle }) => (
            <RuntimeAssetsInstancePickerView
              items={RUNTIME_INSTANCE_CANDIDATES}
              selected={selected}
              onToggle={onToggle}
              onQueryChange={() => {}}
            />
          )}
          renderReconcileSection={({ channels }) => (
            <ArtifactReconcileSectionView
              channels={channels}
              runs={ARTIFACT_RECONCILE_RUNS}
              settings={{ enabled: true, intervalHours: 24, nextRunAt: '2025-03-19T03:00:00Z' }}
              onSaveSettings={() => Promise.resolve(true)}
              onTrigger={() => {}}
              onOpenReport={() => {}}
              onCloseReport={() => {}}
              onMissingPageChange={() => {}}
              onOrphanPageChange={() => {}}
              onResolveMissing={() => Promise.resolve(true)}
              onCleanupOrphans={() => Promise.resolve(true)}
            />
          )}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：聚合载荷与在途标志（refreshing / importing / deploying / deleting*Id）注入，
          删除与导入以回调上报——只有外壳知道在删哪一条，故逐行禁用标志也由外壳给。留组件内的是
          导入表单草稿、上传进度条与二次确认弹窗。两个插槽：renderInstancePicker（批量部署要多选，
          候选走服务端搜索，这里接包内 RuntimeAssetsInstancePickerView）与 renderReconcileSection
          （对账区块自带取数与处置动作，整块由外壳接线，这里按生产同款组合复用
          ArtifactReconcileSectionView）。deployResult 给的是上次回执样例（2 成功 / 1 失败）。
        </p>
      </Panel>
    </>
  )
}
