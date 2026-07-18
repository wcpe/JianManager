/**
 * devmock 自持 API 契约类型（FR-284，见 ADR-064 / monorepo-unification spec §3）。
 *
 * 依赖反转：devmock 不 import 应用内部（@/…）——本文件按 `docs/API.md` 契约
 * 自持 handler 所需的请求/响应形状（与 app 的 `src/api/*.ts` 同源心智模型）。
 * 形状漂移由既有 dom 测试看守（真组件打真 mock，字段错位即红，ADR-047 机制）；
 * 将来出现第二个消费 app 再评估抽独立 api-contracts 包（YAGNI）。
 */

// ─────────────────────────── 实例（instances） ───────────────────────────

/** 实例概要。tags 为后端原始 JSON 字符串承载（见 app parseTags 说明），勿直接当数组用。 */
export interface InstanceInfo {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  /** 群组服角色（FR-032）：proxy / backend / universal。 */
  role: string
  processType: string
  status: string
  statusReason?: string
  startCommand: string
  jdkId?: number
  workDir: string
  workDirInPlace?: boolean
  image?: string
  cpuLimit?: number
  memLimitMb?: number
  diskLimitMb?: number
  serverPort: number
  autoStart: boolean
  autoRestart: boolean
  tags: string | string[] | null
  createdAt: string
}

// ─────────────────────────── 备份（backups） ───────────────────────────

/** 备份记录。status/mode/type 取值与后端 model.Backup 对齐。 */
export interface BackupInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  filePath: string
  fileSizeMb: number
  /** 触发来源：0=手动, 1=定时 */
  type: number
  /** 备份模式：0=全量, 1=增量（FR-056） */
  mode: number
  /** 状态：0=待处理, 1=进行中, 2=已完成, 3=失败 */
  status: number
  parentId?: number
  storageId?: number
  storageKey?: string
  checksum?: string
  checksumAlgo?: string
  createdAt: string
}

/** 创建备份请求体。 */
export interface CreateBackupBody {
  name?: string
  incremental?: boolean
  storageId?: number
}

// ─────────────────────── 备份存储（backup-storages） ───────────────────────

/** 备份远程存储后端。凭证以 ${ENV_VAR} 引用，后端不返回明文（FR-057）。 */
export interface BackupStorage {
  id: number
  name: string
  /** local | s3 | sftp | webdav */
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  accessKeyEnv: string
  secretKeyEnv: string
  useSsl: boolean
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
  backupCount: number
  usedBytes: number
  createdAt: string
}

/** 创建存储后端请求体。 */
export interface CreateBackupStorageBody {
  name: string
  type: string
  endpoint?: string
  bucket?: string
  region?: string
  prefix?: string
  accessKeyEnv?: string
  secretKeyEnv?: string
  useSsl?: boolean
}

// ─────────────────── 制品存储渠道（artifact-storages，FR-347） ───────────────────

/** 制品存储渠道：client-file 制品落点路由配置。响应永不含凭证明文/密文（ADR-073）。 */
export interface ArtifactStorageChannel {
  id: number
  name: string
  /** local（内置「本机存储」独占）| s3 */
  type: 'local' | 's3'
  endpoint: string
  bucket: string
  region: string
  prefix: string
  useSsl: boolean
  presignTtlSeconds: number
  active: boolean
  builtin: boolean
  hasAccessKey: boolean
  hasSecretKey: boolean
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
  createdAt: string
  updatedAt: string
}

/** 创建/编辑渠道请求体。编辑时 accessKey/secretKey 留空 = 保留原值。 */
export interface SaveArtifactStorageBody {
  name: string
  type: string
  endpoint?: string
  bucket?: string
  region?: string
  prefix?: string
  accessKey?: string
  secretKey?: string
  useSsl?: boolean
  presignTtlSeconds?: number
}

/** 长任务（与 web/src/api/tasks.ts Task 对齐）。 */
export interface Task {
  id: number
  taskId: string
  nodeId: number
  kind: string
  state: 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'
  progress: number
  title: string
  detail: string
  error: string
  result: string
  cancelRequested: boolean
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 制品存量迁移登记与实时计数（FR-348）。 */
export interface ArtifactMigrationInfo {
  taskId: string
  targetChannelId: number
  /** 目标渠道当前名称（迁移后渠道被删时为空串）。 */
  targetName: string
  total: number
  migrated: number
  failed: number
  skipped: number
}

/** 最近一次迁移状态；从未迁移过时 task/migration 均为 null。 */
export interface ArtifactMigrationStatus {
  task: Task | null
  migration: ArtifactMigrationInfo | null
}

/** 迁移失败明细一条（sha256 + 原因，FR-348）。 */
export interface ArtifactMigrationFailure {
  id: number
  taskId: string
  assetId: number
  sha256: string
  filename: string
  size: number
  reason: string
  createdAt: string
}

// ─────────────────────────── 定时任务（schedules） ───────────────────────────

/** 定时任务（与后端 model.Schedule 对齐）。 */
export interface ScheduleInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  cronExpr: string
  /** 动作：start / stop / restart / command / backup。 */
  action: string
  payload: string
  enabled: boolean
  lastRun: string | null
  createdAt: string
}

/** 定时任务执行日志（与后端 model.ScheduleExecutionLog 对齐）。 */
export interface ScheduleLogInfo {
  id: number
  scheduleId: number
  action: string
  status: 'success' | 'failed'
  error: string
  startedAt: string
  finishedAt: string
}

/** 创建定时任务请求体。 */
export interface CreateScheduleBody {
  instanceId: number
  name: string
  cronExpr: string
  action: string
  payload?: string
}

/** 更新定时任务请求体（PUT /schedules/:id 仅接收这些可选字段）。 */
export interface UpdateScheduleBody {
  cronExpr?: string
  enabled?: boolean
  action?: string
  payload?: string
}

// ─────────────────────────── 群组网络（networks） ───────────────────────────

/** 成员实例按运行状态的计数桶（五态零补齐，FR-335）。 */
export interface MemberStatusCounts {
  running: number
  stopped: number
  crashed: number
  starting: number
  stopping: number
}

/** 群组概要。 */
export interface NetworkSummary {
  id: number
  uuid: string
  name: string
  description: string
  memberCount: number
  /** 成员健康计数桶（FR-335）。 */
  memberStatus: MemberStatusCounts
  createdAt: string
}

/** 群组成员实例概要。 */
export interface NetworkMember {
  instanceId: number
  name: string
  role: string
  nodeId: number
  status: string
}

/** 群组详情（含成员）。 */
export interface NetworkDetail {
  id: number
  uuid: string
  name: string
  description: string
  members: NetworkMember[]
}

/** 群组批量操作结果。 */
export interface BatchActionResult {
  action: string
  total: number
  succeeded: number
  failed: number
  results: { instanceId: number; ok: boolean; error?: string }[]
}

// ─────────────────────── 注册关系（registrations） ───────────────────────

/** proxy↔backend 注册关系（对应后端 model.ServerRegistration + backend 概要）。 */
export interface Registration {
  id: number
  proxyId: number
  backendId: number
  alias: string
  priority: number
  forcedHost: string
  restricted: boolean
  enabled: boolean
  backend?: {
    id: number
    name: string
    role: string
    nodeId: number
    serverPort: number
    status: string
  }
}

/** 创建注册请求体。 */
export interface CreateRegistrationBody {
  backendId: number
  alias?: string
  priority?: number
  forcedHost?: string
  restricted?: boolean
  enabled?: boolean
}

// ─────────────────────────── 拓扑聚合（topology，FR-335） ───────────────────────────

/** 拓扑聚合里的单个代理概要 + 其注册（与 GET /proxies/:id/registrations 同构）。 */
export interface TopologyProxy {
  id: number
  name: string
  status: string
  serverPort: number
  nodeId: number
  registrations: Registration[]
}

/** 拓扑分组概要：一个 network 的成员归属。 */
export interface TopologyNetwork {
  id: number
  name: string
  memberInstanceIds: number[]
}

/** 全量群组拓扑聚合响应（GET /topology，FR-335）。 */
export interface TopologyResponse {
  proxies: TopologyProxy[]
  networks: TopologyNetwork[]
}

// ─────────────────────────── 搭建代理（proxy） ───────────────────────────

/** 搭建代理请求体（对应后端 service.ProvisionProxyRequest，FR-035）。 */
export interface ProvisionProxyBody {
  nodeId: number
  name: string
  proxyType: string // velocity | waterfall | bungeecord
  version?: string
  jdkId?: number
  memoryMb?: number
  jvmArgs?: string[]
  groupId?: number
  onlineMode?: boolean
}

/** 搭建代理结果。 */
export interface ProvisionProxyResult {
  instance: InstanceInfo
  forwardingSecret?: string
  registrations: unknown[]
  warnings?: string[]
}
