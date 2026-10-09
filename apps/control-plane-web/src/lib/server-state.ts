/**
 * 服务器状态（探针全量快照）的核心契约（FR-076 / FR-077）。
 *
 * 归包理由：服务器状态视图要按分区渲染 Bukkit 内部状态，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 有界裁剪结构（探针侧 ServerStateSupport.bounded）：大集合裁剪到 items + 原始总数 + 是否截断。 */
export interface Bounded<T> {
  items: T[]
  total: number
  truncated: boolean
}

/** server 分区：版本/视距/在线/MOTD/白名单·在线模式/插件清单（有界）。 */
export interface ServerSection {
  version?: string
  bukkitVersion?: string
  motd?: string
  viewDistance?: number
  onlinePlayers?: number
  maxPlayers?: number
  onlineMode?: boolean
  whitelistEnabled?: boolean
  allowNether?: boolean
  allowEnd?: boolean
  plugins?: Bounded<{ name: string; version: string; enabled: boolean }>
  error?: string
}

/** 单个世界的状态条目。 */
export interface WorldEntry {
  name: string
  environment: string
  difficulty: string
  loadedChunks: number
  entities: number
  tileEntities: number
  players: number
  seed: number
}

/** jvm 分区：堆/非堆/线程/运行时长/处理器数/JVM 名版本 vendor。 */
export interface JvmSection {
  jvmName?: string
  jvmVendor?: string
  jvmVersion?: string
  availableProcessors?: number
  uptimeMs?: number
  startTimeMs?: number
  heapUsedBytes?: number
  heapCommittedBytes?: number
  heapMaxBytes?: number
  nonHeapUsedBytes?: number
  threadCount?: number
  daemonThreadCount?: number
  peakThreadCount?: number
  error?: string
}

/** classloader 分区（FR-076 重点）：类加载计数 + 各插件类加载器层级链。 */
export interface ClassloaderSection {
  counts?: {
    loadedClassCount: number
    totalLoadedClassCount: number
    unloadedClassCount: number
  }
  pluginLoaders?: Bounded<{ plugin: string; loaderClass: string; chain: string[] }>
  error?: string
}

/** scheduler 分区：待执行任务数 / 活跃 worker 数。 */
export interface SchedulerSection {
  pendingTasks?: number
  activeWorkers?: number
  error?: string
}

/** listeners 分区：已注册监听器条目数摘要（按插件分组，有界）。 */
export interface ListenersSection {
  totalRegistered?: number
  byPlugin?: Bounded<{ plugin: string; count: number }>
  error?: string
}

/** 探针采集的全量服务器状态（与探针侧 BukkitServerStateCollector.collectJson 结构对齐）。 */
export interface ProbeServerState {
  collectedAt?: number
  server?: ServerSection
  worlds?: Bounded<WorldEntry>
  jvm?: JvmSection
  classloader?: ClassloaderSection
  scheduler?: SchedulerSection
  listeners?: ListenersSection
}

/** GET /instances/:id/server-state 响应（FR-076，CP 原样透传探针 state）。 */
export interface ServerStateResponse {
  instanceId: number
  /** 探针当前是否连入本机 Worker（false 时 state 为 null，前端提示部署/连接探针）。 */
  connected: boolean
  /** 本次是否成功取回状态（探针在线但采集超时/失败时为 false，前端提示重试）。 */
  available: boolean
  /** 探针采集的全量状态（不可得时为 null）。 */
  state: ProbeServerState | null
  /** 降级时的友好原因。 */
  error?: string
}
