import { HttpResponse, type HttpResponseResolver } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { requireAuth } from '@jianmanager/devmock/auth-middleware'
import { capabilityProfileFor, type MockCapabilityProfile } from '@jianmanager/devmock/capability-profile'

/**
 * 实例核心域 mock handler（FR-201，照 spec §7 范式）。
 * 覆盖实例 CRUD / 状态机（start→RUNNING、stop→STOPPED…）/ 端口 / 服务器状态 / 组织分组。
 * 不重定义地基/它域端点：/instances/:id/terminal-token（realtime/terminal-ws）、
 * /instances/events（realtime/instance-events）、/instances/:id/metrics（监控域）。
 */

/**
 * 假后端实例行。字段严格对齐 web/src/api/instances.ts 的 InstanceInfo——
 * tags 是 JSON 字符串列（"" / '["env:prod"]' / "null"），与后端一致、前端经 parseTags 解析，
 * 直接当数组用会白屏（见全局记忆「JSON 字符串字段前端解析」）。
 */
export interface MockInstance {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  role: string
  processType: string
  status: string
  /** 当前状态原因（对齐 InstanceInfo.statusReason）：CRASHED 时给出具体崩溃原因，供 FR-294 浮窗显示。 */
  statusReason?: string
  startCommand: string
  /** 直接绑定的 JDK id；0/缺省表示未直接绑定。 */
  jdkId?: number
  /** Java 大版本兜底绑定（无直接 JDK 时按节点+主版本解析）。 */
  javaMajorVersion?: number
  workDir: string
  image: string
  cpuLimit: number
  memLimitMb: number
  diskLimitMb: number
  /** 运行期配额 throttle 档登记的待收紧限额（FR-467）：下次启动/重建容器时生效；0=无。 */
  throttleCpuLimit?: number
  throttleMemLimitMb?: number
  serverPort: number
  autoStart: boolean
  autoRestart: boolean
  /** JSON 字符串列（保真返回字符串，勿返数组）。 */
  tags: string
  createdAt: string
}

/** 假后端组织分组节点（对应 InstanceGroupNode，扁平 + parentId 重建层级）。 */
export interface MockInstanceGroup {
  id: number
  uuid: string
  name: string
  parentId: number | null
  sort: number
}

/** 分组 ↔ 实例成员关系（多对多；instanceCount 由子树去重聚合）。 */
export interface MockGroupMembership {
  id: number
  groupId: number
  instanceId: number
}

/** 滚动编排会话（FR-457）：字段对齐 web/src/api/instanceRolling.ts 的 RollingOp。 */
export interface MockRollingOp {
  id: number
  action: string
  command?: string
  batchSize: number
  batchIntervalSec: number
  failFast: boolean
  ratio: number
  targets: number[]
  cursor: number
  state: 'pending' | 'running' | 'paused' | 'done' | 'canceled'
  requested: number
  succeeded: number
  failed: number
  skipped: number
  errors: { instanceId: number; error: string }[]
  createdAt: string
  updatedAt: string
}

/** 崩溃快照（FR-313，增强 FR-470）：字段对齐 web/src/api/crashSnapshots.ts 的 CrashSnapshot。 */
export interface MockCrashSnapshot {
  id: number
  instanceId: number
  occurredAt: string
  exitCode: number
  signal: string
  durationMs: number
  tailOutput: string
  /** 根因归类（FR-470）；缺失等价于后端旧快照的空值（前端按 unknown 渲染）。 */
  rootCause?: string
  /** 归一化同类指纹（FR-470）。 */
  signature?: string
  /** 命中规则的原文行（后端解析后的数组形态，前端直接渲染）。 */
  evidenceLines?: string[]
  confidence?: number
  /** 崩溃与资源关联证据（FR-470 §2.3）。 */
  correlation?: {
    nearOom: boolean
    rssAtCrash: number
    memLimitMb: number
    heapUsedMax: number
    gcNote: string
    note?: string
  }
  createdAt: string
}

/** 整机快照（FR-466）：字段对齐 web/src/api/snapshots.ts 的 InstanceSnapshot。 */
export interface MockInstanceSnapshot {
  id: number
  uuid: string
  instanceId: number
  name: string
  kind: 'manual' | 'pre_rollback' | 'scheduled'
  state: string
  rootBackupId: number
  binaryName: string
  binarySha256: string
  configHash: string
  configSummary: string
  triggeredBy: number
  triggeredByRollbackId: number
  sizeMb: number
  failureReason: string
  note: string
  createdAt: string
  updatedAt: string
}

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const INSTANCE_SEED_OVERRIDES: MockInstance[] = [
  {
    id: 1,
    uuid: 'i-survival',
    nodeId: 1,
    name: 'survival-1',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -Xmx2G -jar paper.jar nogui',
    jdkId: 1,
    javaMajorVersion: 21,
    workDir: '/servers/survival-1',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25565,
    autoStart: false,
    autoRestart: true,
    tags: '["env:prod","survival","region:r1","zone:z1"]',
    createdAt: '2026-01-01T00:00:00Z',
  },
  {
    id: 2,
    uuid: 'i-lobby',
    nodeId: 1,
    name: 'lobby-proxy',
    type: 'minecraft_java',
    role: 'proxy',
    processType: 'daemon',
    status: 'STOPPED',
    startCommand: 'java -jar velocity.jar',
    jdkId: 0,
    javaMajorVersion: 17,
    workDir: '/servers/lobby-proxy',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25577,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod","region:r1","zone:z1"]',
    createdAt: '2026-01-02T00:00:00Z',
  },
  {
    id: 3,
    uuid: 'i-creative',
    nodeId: 2,
    name: 'creative-1',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'docker',
    status: 'CRASHED',
    statusReason: '实例未绑定 JDK，启动委托失败',
    startCommand: 'java -jar paper.jar nogui',
    jdkId: 0,
    javaMajorVersion: 21,
    workDir: '/servers/creative-1',
    image: 'itzg/minecraft-server:latest',
    cpuLimit: 1.5,
    memLimitMb: 2048,
    diskLimitMb: 0,
    serverPort: 25566,
    autoStart: false,
    autoRestart: false,
    tags: '["env:test","creative","region:r2","zone:z2"]',
    createdAt: '2026-01-03T00:00:00Z',
  },
  {
    id: 10,
    uuid: 'i-survival-proxy',
    nodeId: 1,
    name: 'survival-proxy',
    type: 'minecraft_java',
    role: 'proxy',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -jar velocity.jar',
    workDir: '/servers/survival-proxy',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25570,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod","survival","edge","region:r1","zone:z1"]',
    createdAt: '2026-01-10T00:00:00Z',
  },
  {
    id: 11,
    uuid: 'i-survival-lobby',
    nodeId: 1,
    name: 'survival-lobby',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -Xmx2G -jar paper.jar nogui',
    workDir: '/servers/survival-lobby',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25566,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod","survival","lobby","region:r1","zone:z1"]',
    createdAt: '2026-01-11T00:00:00Z',
  },
  {
    id: 12,
    uuid: 'i-survival-world',
    nodeId: 2,
    name: 'survival-world',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'docker',
    status: 'CRASHED',
    statusReason: 'OutOfMemoryError: Java heap space',
    startCommand: 'java -Xmx4G -jar paper.jar nogui',
    workDir: '/servers/survival-world',
    image: 'itzg/minecraft-server:latest',
    cpuLimit: 2,
    memLimitMb: 4096,
    diskLimitMb: 0,
    serverPort: 25567,
    autoStart: false,
    autoRestart: true,
    tags: '["env:prod","survival","world","region:r1","zone:z2"]',
    createdAt: '2026-01-12T00:00:00Z',
  },
  {
    id: 20,
    uuid: 'i-creative-proxy',
    nodeId: 1,
    name: 'creative-proxy',
    type: 'minecraft_java',
    role: 'proxy',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -jar velocity.jar',
    workDir: '/servers/creative-proxy',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25580,
    autoStart: true,
    autoRestart: true,
    tags: '["env:test","creative","edge","region:r2","zone:z1"]',
    createdAt: '2026-01-20T00:00:00Z',
  },
  {
    id: 21,
    uuid: 'i-creative-plot',
    nodeId: 1,
    name: 'creative-plot',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'STOPPED',
    startCommand: 'java -Xmx2G -jar paper.jar nogui',
    workDir: '/servers/creative-plot',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25581,
    autoStart: false,
    autoRestart: true,
    tags: '["env:test","creative","plot","region:r2","zone:z2"]',
    createdAt: '2026-01-21T00:00:00Z',
  },
  {
    // 配套服务（FR-450）：Go 原生二进制，type=generic、role=beacon，无 MC 世界语义。
    id: 30,
    uuid: 'i-beacon',
    nodeId: 1,
    name: 'beacon-1',
    type: 'generic',
    role: 'beacon',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: './beacon-1.1.0-linux-amd64 --config config.yaml',
    jdkId: 0,
    workDir: '/servers/beacon-1',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 8080,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod","infra"]',
    createdAt: '2026-01-30T00:00:00Z',
  },
]

const INSTANCE_SEED_SIZE = 1200
const STATUS_POOL = ['RUNNING', 'STOPPED', 'CRASHED', 'STARTING', 'STOPPING'] as const
const ENV_POOL = ['prod', 'test', 'dev'] as const

function buildGeneratedInstance(id: number): MockInstance {
  const role = id % 11 === 0 ? 'proxy' : id % 7 === 0 ? 'universal' : 'backend'
  const env = ENV_POOL[id % ENV_POOL.length]
  const status = STATUS_POOL[id % STATUS_POOL.length]
  const name = role === 'proxy' ? `proxy-${id.toString().padStart(4, '0')}` : `server-${id.toString().padStart(4, '0')}`
  return {
    id,
    uuid: `i-scale-${id}`,
    nodeId: id % 2 === 0 ? 2 : 1,
    name,
    type: 'minecraft_java',
    role,
    processType: id % 4 === 0 ? 'docker' : 'daemon',
    status,
    statusReason: status === 'CRASHED' ? `进程异常退出（exit code ${id % 3 === 0 ? 137 : 1}）` : undefined,
    startCommand: role === 'proxy' ? 'java -jar velocity.jar' : 'java -Xmx2G -jar paper.jar nogui',
    workDir: `/servers/${name}`,
    image: id % 4 === 0 ? 'itzg/minecraft-server:latest' : '',
    cpuLimit: id % 4 === 0 ? 1 + (id % 4) * 0.5 : 0,
    memLimitMb: id % 4 === 0 ? 2048 + (id % 3) * 1024 : 0,
    diskLimitMb: 0,
    serverPort: 25565 + id,
    autoStart: id % 3 === 0,
    autoRestart: id % 5 !== 0,
    // FR-452：多数实例带 region:/zone: 标签（供 region/zone 维度分组），
    // 少数（id%13===0 无 region，id%5===0 无 zone）不带，覆盖「未分组」落末尾场景。
    tags: JSON.stringify([
      `env:${env}`,
      role === 'proxy' ? 'edge' : 'survival',
      ...(id % 13 === 0 ? [] : [`region:r${(id % 3) + 1}`]),
      ...(id % 5 === 0 ? [] : [`zone:z${(id % 4) + 1}`]),
    ]),
    createdAt: new Date(Date.UTC(2026, 2, 1 + id)).toISOString(),
  }
}

function seedInstances(): MockInstance[] {
  const rows = new Map<number, MockInstance>()
  for (let id = 1; id <= INSTANCE_SEED_SIZE; id++) rows.set(id, buildGeneratedInstance(id))
  for (const row of INSTANCE_SEED_OVERRIDES) rows.set(row.id, row)
  return [...rows.values()].sort((a, b) => a.id - b.id)
}

const instances = db<MockInstance>('instances', seedInstances)

/** 按实例 (type, role) 取能力画像（真源抽到 `@jianmanager/devmock/capability-profile`）。 */
function capabilitiesFor(inst: MockInstance): MockCapabilityProfile {
  return capabilityProfileFor(inst.type, inst.role)
}

/**
 * 列表响应逐行补能力画像（FR-445 §2.4）：列表也优先用后端下发的画像，
 * 前端仅在画像缺失时才走本地兜底表。与详情响应保持同一真源。
 */
function withCapabilities(
  rows: MockInstance[],
): Array<MockInstance & { capabilities: MockCapabilityProfile }> {
  return rows.map((i) => ({ ...i, capabilities: capabilitiesFor(i) }))
}

const instanceGroups = db<MockInstanceGroup>('instanceGroups', () => [
  { id: 1, uuid: 'g-asia', name: '亚洲区', parentId: null, sort: 0 },
  { id: 2, uuid: 'g-survival', name: '生存', parentId: 1, sort: 0 },
  { id: 3, uuid: 'g-creative', name: '创造', parentId: 1, sort: 1 },
])

const groupMembers = db<MockGroupMembership>('instanceGroupMembers', () => [
  { id: 1, groupId: 2, instanceId: 1 },
  { id: 2, groupId: 3, instanceId: 3 },
])

/**
 * 相对「今天（UTC）」的 N 天前固定时刻 —— 崩溃种子必须落在**趋势窗口内**。
 *
 * 种子为什么不能用写死的日期：趋势窗口是相对当下算的（后端 `crashDayFloor` =
 * 今天 −(days−1)，与 mock 同口径），写死 2026-07 的日期会随真实时间推移滑出 30 天窗口，
 * 使趋势总数静默变成 0。用相对日保证种子**永远**落在窗口内，同时保留测试依赖的先后顺序。
 */
function crashAtDaysAgo(days: number, hour = 8, minute = 0): string {
  const d = new Date(Date.now() - days * 86_400_000)
  d.setUTCHours(hour, minute, 0, 0)
  return d.toISOString()
}

/** 相对「今天（UTC）」的 N 天前的统计日（YYYY-MM-DD，与后端 bucket_day 同为 UTC 日）。 */
function crashDayAgo(days: number): string {
  return crashAtDaysAgo(days).slice(0, 10)
}

/**
 * 实例 12 的 6 条崩溃快照种子（FR-313 + FR-470）。
 *
 * 为什么是 **6** 条：K=5 是后端的滚动保留上限，只有超过它才能让「列表被裁到 5 条、
 * 趋势仍报完整 6 次」这条契约在 mock 下**真的可能失败**——若种子不越过边界，
 * 该断言在任何实现下都成立，属假绿。
 * 逐条分配不同指纹（前 4 条两个指纹交替），使同类聚合有真实分组可断言。
 */
function crashSeedRowsForInstance12(): MockCrashSnapshot[] {
  const reasons: { day: number; signature: string; tail: string }[] = [
    { day: 5, signature: 'Java heap space', tail: 'java.lang.OutOfMemoryError: Java heap space\n' },
    { day: 4, signature: 'GC overhead limit exceeded', tail: 'java.lang.OutOfMemoryError: GC overhead limit exceeded\n' },
    { day: 4, signature: 'Java heap space', tail: 'java.lang.OutOfMemoryError: Java heap space\n' },
    { day: 2, signature: 'GC overhead limit exceeded', tail: 'java.lang.OutOfMemoryError: GC overhead limit exceeded\n' },
    { day: 2, signature: 'Java heap space', tail: 'java.lang.OutOfMemoryError: Java heap space\n' },
    { day: 1, signature: 'Java heap space', tail: 'java.lang.OutOfMemoryError: Java heap space\n\tat net.minecraft.server.MinecraftServer.tick\n' },
  ]
  return reasons.map((r, idx) => {
    const headline = r.tail.trim().split('\n')[0]
    return {
      id: 3 + idx,
      instanceId: 12,
      occurredAt: crashAtDaysAgo(r.day, 2, 15),
      exitCode: 1,
      signal: '',
      durationMs: 3600000,
      tailOutput: r.tail,
      rootCause: 'oom',
      signature: r.signature,
      evidenceLines: headline ? [headline] : [],
      confidence: 0.92,
      createdAt: crashAtDaysAgo(r.day, 2, 15),
    }
  })
}

// 崩溃快照种子（FR-313）：实例 3 两条 + 实例 12 六条。
//
// **两个集合刻意分离**：真后端的崩溃快照（K=5 滚动裁剪）与崩溃趋势（独立汇总表
// `InstanceCrashStat`）是**两个数据源**——连崩超过 5 次后列表被裁到 5 条，趋势计数仍完整。
// 实例 12 刻意有 6 条快照：列表只回最近 5 条（真实触发 K=5 裁剪），趋势侧仍是完整 6 次，
// 使「列表条数 < 趋势总数」在 mock 下真实可断言（否则该断言退化成恒真）。
const crashSnapshots = db<MockCrashSnapshot>('instanceCrashSnapshots', () => [
  {
    id: 1,
    instanceId: 3,
    occurredAt: crashAtDaysAgo(3),
    exitCode: 1,
    signal: '',
    durationMs: 2300,
    tailOutput: 'Error: Unable to access jarfile paper.jar\n',
    rootCause: 'class_not_found',
    signature: 'Unable to access jarfile N.jar',
    evidenceLines: ['Error: Unable to access jarfile paper.jar'],
    confidence: 0.85,
    createdAt: crashAtDaysAgo(3, 8, 0),
  },
  {
    id: 2,
    instanceId: 3,
    occurredAt: crashAtDaysAgo(1, 9, 30),
    exitCode: 137,
    signal: 'killed',
    durationMs: 754000,
    tailOutput: '[09:29:58] [Server thread/INFO]: Saving chunks\n[09:29:59] [Server thread/WARN]: Can\'t keep up!\n',
    rootCause: 'oom',
    signature: 'killed',
    evidenceLines: [],
    confidence: 0.6,
    correlation: {
      nearOom: true,
      rssAtCrash: 1024 * 1024 * 1024,
      memLimitMb: 1024,
      heapUsedMax: 0,
      gcNote: 'GC 关联待 FR-465 落地（serverprobe_gc_* 尚未入库）',
    },
    createdAt: crashAtDaysAgo(1, 9, 30),
  },
  // 实例 12：6 条同根因（oom）崩溃——列表侧将被 K=5 裁到 5 条。
  ...crashSeedRowsForInstance12(),
])

/**
 * 崩溃统计汇总行（FR-470，对齐后端 `model.InstanceCrashStat`）。
 *
 * 唯一键是 (实例, 天, 根因, **指纹**)——同一「天 × 根因」天然可能有多行（每个指纹一行），
 * 故趋势必须按 (天 × 根因) **求和**后输出（后端 `trendAggregator` 的语义）。
 * 本集合刻意独立于 `crashSnapshots`：趋势的计数来自这里，列表的裁剪不影响到它。
 */
export interface MockCrashStat {
  id: number
  instanceId: number
  /** 统计日（UTC，YYYY-MM-DD；后端 bucket_day 同为 UTC）。 */
  bucketDay: string
  rootCause: string
  signature: string
  count: number
}

const crashStats = db<MockCrashStat>('instanceCrashStats', () => [
  // 实例 3：与快照列表 1:1（两天、两个根因），用于验证「列表与趋势一致」的常规路径。
  { id: 1, instanceId: 3, bucketDay: crashDayAgo(3), rootCause: 'class_not_found', signature: 'Unable to access jarfile N.jar', count: 1 },
  { id: 2, instanceId: 3, bucketDay: crashDayAgo(1), rootCause: 'oom', signature: 'killed', count: 1 },
  // 实例 12：6 次崩溃，三个统计日各 2 次；刻意构成两种不同的聚合压力：
  //   · day-4：**同一「天 × 根因」的两种形态**都在——两个不同根因各 1 次（逼前端按天求和，
  //     只取一条会让那天显示 1 而非 2）；
  //   · day-2：同一根因下两个不同**指纹**各 1 次（逼 mock 按 (天 × 根因) 求和，
  //     逐行 append 会吐出重复点）；
  //   · day-1：同一指纹计数 2（覆盖 count>1 的直接累加路径）。
  { id: 3, instanceId: 12, bucketDay: crashDayAgo(4), rootCause: 'oom', signature: 'Java heap space', count: 1 },
  { id: 4, instanceId: 12, bucketDay: crashDayAgo(4), rootCause: 'port_in_use', signature: 'Address already in use', count: 1 },
  { id: 5, instanceId: 12, bucketDay: crashDayAgo(2), rootCause: 'oom', signature: 'Java heap space', count: 1 },
  { id: 6, instanceId: 12, bucketDay: crashDayAgo(2), rootCause: 'oom', signature: 'GC overhead limit exceeded', count: 1 },
  { id: 7, instanceId: 12, bucketDay: crashDayAgo(1), rootCause: 'oom', signature: 'Java heap space', count: 2 },
])

// 整机快照种子（FR-466）：两条**都挂在实例 3**（creative-1）——一条回滚前快照 + 一条手动快照，
// 使「一键回滚」与「回滚前留底」两种形态在 mock 下都能看到。
// 实例 2 刻意无快照，供空态用例使用；实例 1 无快照（如需在实例 1 验证回滚，请自行补种子）。
const instanceSnapshots = db<MockInstanceSnapshot>('instanceSnapshots', () => [
  {
    id: 1,
    uuid: 'snap-seed-1',
    instanceId: 3,
    name: '回滚前自动快照 2026-07-13 02:00:00',
    kind: 'pre_rollback',
    state: 'completed',
    rootBackupId: 901,
    binaryName: 'server.jar',
    binarySha256: 'a'.repeat(64),
    configHash: 'c'.repeat(64),
    configSummary: 'start=java -Xmx2G -jar server.jar',
    triggeredBy: 1,
    triggeredByRollbackId: 2,
    sizeMb: 256.5,
    failureReason: '',
    note: '',
    createdAt: '2026-07-13T02:00:00Z',
    updatedAt: '2026-07-13T02:00:30Z',
  },
  {
    id: 2,
    uuid: 'snap-seed-2',
    instanceId: 3,
    name: '升级前整机快照',
    kind: 'manual',
    state: 'completed',
    rootBackupId: 902,
    binaryName: 'server.jar',
    binarySha256: 'a'.repeat(64),
    configHash: 'c'.repeat(64),
    configSummary: 'start=java -Xmx2G -jar server.jar',
    triggeredBy: 1,
    triggeredByRollbackId: 0,
    sizeMb: 256.1,
    failureReason: '',
    note: '',
    createdAt: '2026-07-12T20:00:00Z',
    updatedAt: '2026-07-12T20:01:00Z',
  },
])

/** 每实例滚动保留的崩溃快照条数（后端 maxCrashSnapshotsPerInstance，K=5，写死不可配）。 */
const CRASH_SNAPSHOT_KEEP = 5

/** 二进制启动命令的固定参数尾部（FR-468 升级/回滚改的只是可执行文件名）。 */
const BINARY_START_ARGS = ' --config config.yaml'

/**
 * 制品库候选版本（FR-468）：按 id 倒序（与后端 View 的排序契约一致，含当前版本）。
 * 三档版本使「升级到更新版本」与「回滚到上一版本」两条路径都有真实版本变化可断言。
 */
const BINARY_CANDIDATES = [
  { assetId: 13, filename: 'beacon-1.2.0-linux-amd64', version: '1.2.0', sha256: 'e'.repeat(64), size: 33554432 },
  { assetId: 12, filename: 'beacon-1.1.0-linux-amd64', version: '1.1.0', sha256: 'd'.repeat(64), size: 32505856 },
  { assetId: 11, filename: 'beacon-1.0.0-linux-amd64', version: '1.0.0', sha256: 'b'.repeat(64), size: 31457280 },
]

/** 实例二进制版本绑定（FR-468，可变：升级/回滚交换 Current/Previous）。 */
interface MockBinaryBinding {
  id: number
  currentAssetId: number
  currentVersion: string
  currentFilename: string
  currentSha256: string
  previousAssetId: number
  previousVersion: string
  previousFilename: string
}

/**
 * 版本绑定集合（FR-468）：**只有 30（beacon-1）预置可回滚点**，其余绑定实例按需懒建。
 *
 * 预置回滚点是为了让「二进制回滚」这条路径在 mock 下可达——否则 hasRollback 恒 false，
 * 回滚按钮永远禁用，回滚的二次确认与回滚后的版本变化在 dom 测试里无法覆盖。
 * 实例 30 种子 startCommand 已是 1.1.0，故回滚点自然是 1.0.0（与种子自洽）。
 */
const binaryBindings = db<MockBinaryBinding>('instanceBinaryBindings', () => [
  {
    id: 30,
    currentAssetId: 12,
    currentVersion: '1.1.0',
    currentFilename: 'beacon-1.1.0-linux-amd64',
    currentSha256: 'd'.repeat(64),
    previousAssetId: 11,
    previousVersion: '1.0.0',
    previousFilename: 'beacon-1.0.0-linux-amd64',
  },
])

/** 按需懒建某实例的版本绑定（未预置的绑定实例默认当前 1.0.0、无回滚点）。 */
function seedBinaryBinding(instanceId: number): MockBinaryBinding {
  const seeded: MockBinaryBinding = {
    id: instanceId,
    currentAssetId: 11,
    currentVersion: '1.0.0',
    currentFilename: 'beacon-1.0.0-linux-amd64',
    currentSha256: 'b'.repeat(64),
    previousAssetId: 0,
    previousVersion: '',
    previousFilename: '',
  }
  return binaryBindings.insert(seeded)
}

/** 实例进程是否存活（升级/回滚要求已停止，与后端 CONFLICT 守卫同口径）。 */
function isInstanceLive(status: string): boolean {
  return status === 'RUNNING' || status === 'STARTING' || status === 'STOPPING'
}

/**
 * 快照自增 ID 的**可重播**分配（FR-466）。
 *
 * 为什么不用模块级 `let nextSnapshotId = 100`：它是模块顶层可变状态，`resetDb()`
 * 只重播各集合、不会重置它，于是同一 vitest 进程内后执行的用例拿到 101、102…——
 * 一旦有人写 `expect(snapshotId).toBe(101)` 就成了依赖执行顺序的脆弱测试。
 * 计数器本身放进集合（随 resetDb 重播），且不从已有行现算，避免删除后 ID 复用。
 */
const snapshotIdSeq = db<{ id: number; next: number }>('instanceSnapshotIdSeq', () => [{ id: 1, next: 101 }])

/** 取下一个快照 ID（每次调用自增；resetDb 后回到 101）。 */
function allocateSnapshotId(): number {
  const row = snapshotIdSeq.get(1) ?? { id: 1, next: 101 }
  const id = row.next
  snapshotIdSeq.update(1, { next: id + 1 })
  return id
}

/** 趋势窗口天数归一（后端 normalizeCrashDays：<=0 默认 30，上限 365）。 */
function normalizeCrashDays(days: number): number {
  if (!Number.isFinite(days) || days <= 0) return 30
  return Math.min(days, 365)
}

/** 标签字符串列解析为数组（容错非 JSON），供按 env/tag 过滤。 */
function parseTags(raw: string): string[] {
  if (!raw || raw === 'null') return []
  try {
    const v: unknown = JSON.parse(raw)
    return Array.isArray(v) ? v.filter((t): t is string => typeof t === 'string') : []
  } catch {
    return []
  }
}

function filterInstancesByQuery(url: URL): MockInstance[] {
  const nodeId = url.searchParams.get('nodeId')
  const status = url.searchParams.get('status')
  const role = url.searchParams.get('role')
  const env = url.searchParams.get('env')
  const tag = url.searchParams.get('tag')
  const networkId = url.searchParams.get('networkId')
  const q = (url.searchParams.get('q') ?? '').trim().toLowerCase()
  return instances.list((i) => {
    if (nodeId && String(i.nodeId) !== nodeId) return false
    if (status && i.status !== status) return false
    if (role && i.role !== role) return false
    if (q && !i.name.toLowerCase().includes(q)) return false
    const tags = parseTags(i.tags)
    if (env && !tags.includes(`env:${env}`)) return false
    if (tag && !tags.includes(tag)) return false
    // networkId 在假后端无群组关系映射，留作不收敛（仅鉴权/形参占位）。
    void networkId
    return true
  })
}

function searchNumber(url: URL, key: string, fallback: number): number {
  const value = Number(url.searchParams.get(key))
  return Number.isFinite(value) && value > 0 ? value : fallback
}

function sortInstances(rows: MockInstance[], url: URL): MockInstance[] {
  const sort = url.searchParams.get('sort') ?? 'name'
  const order = url.searchParams.get('order') === 'desc' ? -1 : 1
  const byText = (a: string, b: string) => a.localeCompare(b, 'zh-CN')
  const sorted = [...rows].sort((a, b) => {
    if (sort === 'status') return byText(a.status, b.status) * order || (a.id - b.id)
    if (sort === 'createdAt') return byText(a.createdAt, b.createdAt) * order || (a.id - b.id)
    if (sort === 'nodeId') return (a.nodeId - b.nodeId) * order || (a.id - b.id)
    return byText(a.name, b.name) * order || (a.id - b.id)
  })
  return sorted
}

function countBy(rows: MockInstance[], key: 'status' | 'role'): Record<string, number> {
  return rows.reduce<Record<string, number>>((acc, row) => {
    const value = row[key]
    acc[value] = (acc[value] ?? 0) + 1
    return acc
  }, {})
}

/** 该组**直接**挂载（不含后代）的实例 ID，对应树视图 memberInstanceIds（FR-452）。 */
function directMemberIds(groupId: number): number[] {
  return groupMembers.list((m) => m.groupId === groupId).map((m) => m.instanceId)
}

/** 子树（含自身及所有后代）去重的实例 ID 集合，对应 instanceCount / GET …/instances 语义。 */
function subtreeInstanceIds(groupId: number): number[] {
  const descendants = new Set<number>([groupId])
  let grew = true
  while (grew) {
    grew = false
    for (const g of instanceGroups.list()) {
      if (g.parentId != null && descendants.has(g.parentId) && !descendants.has(g.id)) {
        descendants.add(g.id)
        grew = true
      }
    }
  }
  const ids = new Set<number>()
  for (const m of groupMembers.list()) {
    if (descendants.has(m.groupId)) ids.add(m.instanceId)
  }
  return [...ids]
}

/** 实例概要（分组/群组成员视图复用）。 */
function memberView(instanceId: number) {
  const inst = instances.get(instanceId)
  return {
    instanceId,
    name: inst?.name ?? `#${instanceId}`,
    role: inst?.role ?? 'backend',
    nodeId: inst?.nodeId ?? 0,
    status: inst?.status ?? 'STOPPED',
  }
}

function filteredInstances(url: URL): MockInstance[] {
  const q = url.searchParams.get('q')?.trim().toLowerCase()
  const nodeId = url.searchParams.get('nodeId')
  const status = url.searchParams.get('status')
  const role = url.searchParams.get('role')
  const groupId = url.searchParams.get('groupId')
  const env = url.searchParams.get('env')
  const tag = url.searchParams.get('tag')
  const networkId = url.searchParams.get('networkId')
  const groupIds = groupId ? new Set(subtreeInstanceIds(Number(groupId))) : null

  return instances.list((i) => {
    if (q && !i.name.toLowerCase().includes(q)) return false
    if (nodeId && String(i.nodeId) !== nodeId) return false
    if (status && i.status !== status) return false
    if (role && i.role !== role) return false
    if (groupIds && !groupIds.has(i.id)) return false
    const tags = parseTags(i.tags)
    if (env && !tags.includes(`env:${env}`)) return false
    if (tag && !tags.includes(tag)) return false
    // networkId 在假后端无群组关系映射，留作不收敛（仅鉴权/形参占位）。
    void networkId
    return true
  })
}

function sortedInstances(rows: MockInstance[], url: URL): MockInstance[] {
  const sort = url.searchParams.get('sort') ?? 'name'
  const order = url.searchParams.get('order') === 'desc' ? -1 : 1
  const value = (row: MockInstance) => {
    if (sort === 'status') return row.status
    if (sort === 'createdAt') return row.createdAt
    if (sort === 'nodeId') return row.nodeId
    return row.name
  }
  return [...rows].sort((a, b) => {
    const av = value(a)
    const bv = value(b)
    if (av < bv) return -1 * order
    if (av > bv) return 1 * order
    return a.id - b.id
  })
}

function pageParam(url: URL): { page: number; pageSize: number } {
  const page = Math.max(1, Number(url.searchParams.get('page') ?? 1) || 1)
  const rawSize = Number(url.searchParams.get('pageSize') ?? 50) || 50
  const pageSize = Math.min(200, Math.max(1, rawSize))
  return { page, pageSize }
}

function aggregateRows(rows: MockInstance[]) {
  const byStatus: Record<string, number> = { RUNNING: 0, STOPPED: 0, CRASHED: 0, STARTING: 0, STOPPING: 0 }
  const byRole: Record<string, number> = { backend: 0, proxy: 0, universal: 0 }
  const byNode = new Map<number, number>()
  for (const row of rows) {
    byStatus[row.status] = (byStatus[row.status] ?? 0) + 1
    byRole[row.role] = (byRole[row.role] ?? 0) + 1
    byNode.set(row.nodeId, (byNode.get(row.nodeId) ?? 0) + 1)
  }
  return {
    total: rows.length,
    byStatus,
    byNode: [...byNode.entries()].sort((a, b) => a[0] - b[0]).map(([nodeId, count]) => ({ nodeId, count })),
    byRole,
  }
}

/** 滚动编排会话集合（FR-457）。 */
const rollingOps = db<MockRollingOp>('instanceRollingOps', () => [])

let rollingSeq = 100
let groupSeq = 100
let memberSeq = 100

/** 解析滚动编排目标：ids 优先，否则按 filter 筛选（含 filter.instanceIds 显式集合）。 */
function resolveRollingTargets(body: {
  ids?: number[]
  filter?: { nodeId?: number; status?: string; role?: string; instanceIds?: number[] }
}): { targets: MockInstance[]; skipped: number } {
  if (body.ids?.length) {
    const found = body.ids.map((id) => instances.get(id)).filter((i): i is MockInstance => !!i)
    return { targets: found, skipped: body.ids.length - found.length }
  }
  const scoped = body.filter?.instanceIds
  const rows = instances.list((i) => {
    if (scoped?.length && !scoped.includes(i.id)) return false
    if (body.filter?.nodeId && i.nodeId !== body.filter.nodeId) return false
    if (body.filter?.status && i.status !== body.filter.status) return false
    if (body.filter?.role && i.role !== body.filter.role) return false
    return true
  })
  return { targets: rows, skipped: 0 }
}

/** 对一批目标执行动作（镜像 /instances/batch 的假后端语义）。 */
function applyRollingAction(action: string, targets: MockInstance[]): {
  succeeded: number
  failed: number
  errors: { instanceId: number; error: string }[]
} {
  const nextStatus: Record<string, string> = { start: 'RUNNING', stop: 'STOPPED', restart: 'RUNNING', kill: 'STOPPED' }
  let succeeded = 0
  const errors: { instanceId: number; error: string }[] = []
  for (const inst of targets) {
    if (action === 'command') {
      if (inst.status === 'RUNNING') succeeded++
      else errors.push({ instanceId: inst.id, error: '实例未运行，无法下发命令' })
    } else if (nextStatus[action]) {
      instances.update(inst.id, { status: nextStatus[action] })
      succeeded++
    }
  }
  return { succeeded, failed: errors.length, errors }
}

/** 暂停/继续/取消滚动编排：终态会话幂等返回原状态。 */
function rollingControl(
  info: Parameters<HttpResponseResolver>[0],
  next: 'paused' | 'running' | 'canceled',
) {
  const denied = requireAuth(info)
  if (denied) return denied
  const opId = Number((info.params as { opId: string }).opId)
  const op = rollingOps.get(opId)
  if (!op) return HttpResponse.json({ error: 'NOT_FOUND', message: '编排会话不存在' }, { status: 404 })
  const terminal = op.state === 'done' || op.state === 'canceled'
  if (!terminal) rollingOps.update(opId, { state: next, updatedAt: new Date().toISOString() })
  return HttpResponse.json(rollingOps.get(opId))
}

export const handlers = [
  // ---- 滚动/分批/灰度编排（FR-457）；须在 /instances/:id 之前注册（避免 'rolling' 被当 id）----
  domainRoute('post', '/instances/rolling', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      action: string
      ids?: number[]
      filter?: { nodeId?: number; status?: string; role?: string; instanceIds?: number[] }
      command?: string
      batchSize?: number
      batchIntervalSec?: number
      failFast?: boolean
      ratio?: number
    }
    const { targets: resolved, skipped } = resolveRollingTargets(body)
    // 灰度抽样（稳定序：按 id 升序取前 ceil(n*ratio) 台）。
    const sorted = [...resolved].sort((a, b) => a.id - b.id)
    const ratio = body.ratio ?? 0
    const targets = ratio > 0 && ratio < 1 ? sorted.slice(0, Math.max(1, Math.round(sorted.length * ratio))) : sorted
    const { succeeded, failed, errors } = applyRollingAction(body.action, targets)
    const batchSize = body.batchSize ?? 0
    const totalBatches = batchSize > 0 ? Math.ceil(targets.length / batchSize) : 1
    const now = new Date().toISOString()
    const op = rollingOps.insert({
      id: rollingSeq++,
      action: body.action,
      command: body.command,
      batchSize,
      batchIntervalSec: body.batchIntervalSec ?? 0,
      failFast: !!body.failFast,
      ratio,
      targets: targets.map((t) => t.id),
      cursor: totalBatches,
      state: 'done',
      requested: targets.length,
      succeeded,
      failed,
      skipped,
      errors,
      createdAt: now,
      updatedAt: now,
    })
    return HttpResponse.json(op)
  }),

  domainRoute('get', '/instances/rolling/:opId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const op = rollingOps.get(Number((info.params as { opId: string }).opId))
    if (!op) return HttpResponse.json({ error: 'NOT_FOUND', message: '编排会话不存在' }, { status: 404 })
    return HttpResponse.json(op)
  }),

  // 暂停/继续/取消：终态会话直接返回原状态（幂等）。
  domainRoute('post', '/instances/rolling/:opId/pause', (info) => rollingControl(info, 'paused')),
  domainRoute('post', '/instances/rolling/:opId/resume', (info) => rollingControl(info, 'running')),
  domainRoute('post', '/instances/rolling/:opId/cancel', (info) => rollingControl(info, 'canceled')),

  // ---- 实例 CRUD ----
  domainRoute('get', '/instances/search', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const page = searchNumber(url, 'page', 1)
    const pageSize = Math.min(searchNumber(url, 'pageSize', 50), 200)
    const rows = sortInstances(filterInstancesByQuery(url), url)
    const start = (page - 1) * pageSize
    return HttpResponse.json({
      items: withCapabilities(rows.slice(start, start + pageSize)),
      total: rows.length,
      page,
      pageSize,
    })
  }),

  domainRoute('get', '/instances/aggregate', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = filterInstancesByQuery(url)
    const byNode = [...rows.reduce<Map<number, number>>((acc, row) => {
      acc.set(row.nodeId, (acc.get(row.nodeId) ?? 0) + 1)
      return acc
    }, new Map()).entries()]
      .sort(([a], [b]) => a - b)
      .map(([nodeId, count]) => ({ nodeId, count }))
    return HttpResponse.json({
      total: rows.length,
      byStatus: countBy(rows, 'status'),
      byNode,
      byRole: countBy(rows, 'role'),
    })
  }),

  domainRoute('get', '/instances', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const nodeId = url.searchParams.get('nodeId')
    const status = url.searchParams.get('status')
    const role = url.searchParams.get('role')
    const env = url.searchParams.get('env')
    const tag = url.searchParams.get('tag')
    const networkId = url.searchParams.get('networkId')
    const rows = instances.list((i) => {
      if (nodeId && String(i.nodeId) !== nodeId) return false
      if (status && i.status !== status) return false
      if (role && i.role !== role) return false
      const tags = parseTags(i.tags)
      if (env && !tags.includes(`env:${env}`)) return false
      if (tag && !tags.includes(tag)) return false
      // networkId 在假后端无群组关系映射，留作不收敛（仅鉴权/形参占位）。
      void networkId
      return true
    })
    return HttpResponse.json(withCapabilities(rows))
  }),

  domainRoute('get', '/instances/search', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = sortedInstances(filteredInstances(url), url)
    const { page, pageSize } = pageParam(url)
    const start = (page - 1) * pageSize
    return HttpResponse.json({ items: withCapabilities(rows.slice(start, start + pageSize)), total: rows.length, page, pageSize })
  }),

  domainRoute('get', '/instances/aggregate', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(aggregateRows(filteredInstances(new URL(info.request.url))))
  }),

  domainRoute('post', '/instances', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as Partial<MockInstance> & {
      tags?: string[]
      groupId?: number
    }
    const created = instances.insert({
      uuid: `i-${Date.now()}`,
      nodeId: body.nodeId ?? 1,
      name: body.name ?? 'new-instance',
      type: body.type ?? 'minecraft_java',
      role: body.role ?? 'backend',
      processType: body.processType ?? 'daemon',
      status: 'STOPPED',
      startCommand: body.startCommand ?? '',
      workDir: body.workDir ?? `/servers/${body.name ?? 'new-instance'}`,
      image: body.image ?? '',
      cpuLimit: body.cpuLimit ?? 0,
      memLimitMb: body.memLimitMb ?? 0,
      diskLimitMb: body.diskLimitMb ?? 0,
      serverPort: 25600 + instances.list().length,
      autoStart: body.autoStart ?? false,
      autoRestart: body.autoRestart ?? true,
      tags: Array.isArray(body.tags) ? JSON.stringify(body.tags) : '',
      createdAt: new Date().toISOString(),
    })
    if (body.groupId) {
      groupMembers.insert({ id: memberSeq++, groupId: body.groupId, instanceId: created.id })
    }
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('get', '/instances/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    // 详情响应下发能力画像（FR-445），列表不计（前端列表走 lib/capabilities 本地兜底）。
    return HttpResponse.json({ ...inst, capabilities: capabilitiesFor(inst) })
  }),

  domainRoute('put', '/instances/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instances.get(id)) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const body = (await info.request.json()) as Partial<MockInstance> & { tags?: string[] }
    const patch: Partial<MockInstance> = {}
    if (body.name !== undefined) patch.name = body.name
    if (body.startCommand !== undefined) patch.startCommand = body.startCommand
    if (body.autoStart !== undefined) patch.autoStart = body.autoStart
    if (body.autoRestart !== undefined) patch.autoRestart = body.autoRestart
    if (body.cpuLimit !== undefined) patch.cpuLimit = body.cpuLimit
    if (body.memLimitMb !== undefined) patch.memLimitMb = body.memLimitMb
    if (body.diskLimitMb !== undefined) patch.diskLimitMb = body.diskLimitMb
    if (Array.isArray(body.tags)) patch.tags = JSON.stringify(body.tags)
    return HttpResponse.json(instances.update(id, patch))
  }),

  domainRoute('delete', '/instances/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    instances.remove(id)
    groupMembers
      .list((m) => m.instanceId === id)
      .forEach((m) => groupMembers.remove(m.id))
    // 级联清崩溃快照（FR-313，对齐真 CP 的级联删除语义）。
    crashSnapshots
      .list((s) => s.instanceId === id)
      .forEach((s) => crashSnapshots.remove(s.id))
    // 级联清整机快照（FR-466，同 FR-313 语义）。
    instanceSnapshots
      .list((s) => s.instanceId === id)
      .forEach((s) => instanceSnapshots.remove(s.id))
    // 级联清崩溃统计汇总行（FR-470）与版本绑定（FR-468），与真 CP 的实例删除级联同口径。
    crashStats
      .list((s) => s.instanceId === id)
      .forEach((s) => crashStats.remove(s.id))
    binaryBindings.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  // ---- 状态机：start/stop/restart/kill 改 status，列表/详情联动 ----
  domainRoute('post', '/instances/:id/start', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // 对齐真 CP：启动 transition 清空 statusReason（FR-312 失败原因横幅随查询刷新消失）。
    instances.update(Number(info.params.id), { status: 'RUNNING', statusReason: undefined })
    return HttpResponse.json({ message: '已启动' })
  }),

  domainRoute('post', '/instances/:id/stop', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'STOPPED' })
    return HttpResponse.json({ message: '已停止' })
  }),

  domainRoute('post', '/instances/:id/restart', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // 重启同为启动 transition，一并清空 statusReason（FR-312）。
    instances.update(Number(info.params.id), { status: 'RUNNING', statusReason: undefined })
    return HttpResponse.json({ message: '已重启' })
  }),

  domainRoute('post', '/instances/:id/kill', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'STOPPED' })
    return HttpResponse.json({ message: '已终止' })
  }),

  domainRoute('post', '/instances/:id/command', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { command } = (await info.request.json()) as { command?: string }
    if (!command) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '缺少 command' }, { status: 400 })
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (inst.status !== 'RUNNING')
      return HttpResponse.json({ error: 'INSTANCE_NOT_RUNNING', message: '实例非运行中' }, { status: 422 })
    return HttpResponse.json({ message: '已发送' })
  }),

  domainRoute('post', '/instances/batch', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      action: string
      ids?: number[]
      filter?: { nodeId?: number; status?: string; role?: string }
      command?: string
    }
    let targets: MockInstance[]
    if (body.ids?.length) {
      targets = body.ids.map((id) => instances.get(id)).filter((i): i is MockInstance => !!i)
    } else {
      targets = instances.list((i) => {
        if (body.filter?.nodeId && i.nodeId !== body.filter.nodeId) return false
        if (body.filter?.status && i.status !== body.filter.status) return false
        if (body.filter?.role && i.role !== body.filter.role) return false
        return true
      })
    }
    const nextStatus: Record<string, string> = { start: 'RUNNING', stop: 'STOPPED', restart: 'RUNNING', kill: 'STOPPED' }
    let succeeded = 0
    for (const inst of targets) {
      if (body.action === 'command') {
        if (inst.status === 'RUNNING') succeeded++
      } else if (nextStatus[body.action]) {
        instances.update(inst.id, { status: nextStatus[body.action] })
        succeeded++
      }
    }
    return HttpResponse.json({
      action: body.action,
      requested: targets.length,
      succeeded,
      failed: 0,
      skipped: (body.ids?.length ?? 0) - targets.length,
      errors: [],
    })
  }),

  // ---- 服务器状态（FR-076，原样透传探针 JSON）----
  domainRoute('get', '/instances/:id/server-state', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (inst.status !== 'RUNNING') {
      return HttpResponse.json({ instanceId: inst.id, connected: false, available: false, state: null, error: '探针未连入' })
    }
    return HttpResponse.json({
      instanceId: inst.id,
      connected: true,
      available: true,
      error: '',
      state: {
        collectedAt: Date.now(),
        server: { version: '1.20.4', motd: 'A Mock Server', onlinePlayers: 2, maxPlayers: 20, viewDistance: 10 },
        worlds: {
          items: [{ name: 'world', environment: 'NORMAL', difficulty: 'EASY', loadedChunks: 441, entities: 30, tileEntities: 12, players: 2, seed: 12345 }],
          total: 1,
          truncated: false,
        },
        jvm: { jvmName: 'OpenJDK 64-Bit Server VM', jvmVersion: '17.0.10', availableProcessors: 4, heapUsedBytes: 536870912, heapMaxBytes: 2147483648, threadCount: 59 },
        classloader: { counts: { loadedClassCount: 18000, totalLoadedClassCount: 18500, unloadedClassCount: 500 } },
        scheduler: { pendingTasks: 3, activeWorkers: 1 },
        listeners: { totalRegistered: 42 },
      },
    })
  }),

  // ---- 崩溃诊断（FR-313）：快照按发生时间倒序（最新在前），空结果回 [] ----
  domainRoute('get', '/instances/:id/crash-snapshots', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const rows = crashSnapshots
      .list((s) => s.instanceId === inst.id)
      .sort((a, b) => b.occurredAt.localeCompare(a.occurredAt) || b.id - a.id)
      // K=5：每实例滚动保留最近 5 条（后端 maxCrashSnapshotsPerInstance 在**写侧**裁剪，
      // 读侧读到的最多就是 5 条）。mock 必须同样裁剪，否则「列表被裁、趋势完整」这条
      // 契约在 mock 下退化成「两个数组本来就是同一份数据」，测试成了假绿。
      .slice(0, CRASH_SNAPSHOT_KEEP)
    return HttpResponse.json(rows)
  }),

  // ---- 崩溃趋势与同类聚合（FR-470）：来源独立汇总表（instanceCrashStats），不受 K=5 裁剪 ----
  domainRoute('get', '/instances/:id/crash-trend', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const days = normalizeCrashDays(Number(new URL(info.request.url).searchParams.get('days')))
    // 窗口下界（UTC 日）：后端 crashDayFloor 是 `今天 - (days-1)`，此处同口径。
    const from = new Date(Date.now() - (days - 1) * 86_400_000).toISOString().slice(0, 10)
    const rows = crashStats.list((s) => s.instanceId === inst.id && s.bucketDay >= from)

    let total = 0
    const byCause = new Map<string, number>()
    // 同类聚合按 (根因 × 指纹) 求和——与后端一致：同一指纹的多行统计要合并成一条。
    const bySignature = new Map<string, { signature: string; rootCause: string; count: number }>()
    // 趋势按 (天 × 根因) 求和后每对**恰好一个点**（后端 trendAggregator 的语义）：
    // 统计行唯一键含指纹，逐行 append 会让同一天同一根因输出多个点、与 byRootCause 自相矛盾。
    const trendIndex = new Map<string, number>()
    const points: { day: string; rootCause: string; count: number }[] = []
    for (const st of rows) {
      total += st.count
      byCause.set(st.rootCause, (byCause.get(st.rootCause) ?? 0) + st.count)
      const sigKey = `${st.rootCause}\u0000${st.signature}`
      const sigEntry = bySignature.get(sigKey) ?? { signature: st.signature, rootCause: st.rootCause, count: 0 }
      sigEntry.count += st.count
      bySignature.set(sigKey, sigEntry)
      const dayKey = `${st.bucketDay}\u0000${st.rootCause}`
      const at = trendIndex.get(dayKey)
      if (at === undefined) {
        trendIndex.set(dayKey, points.length)
        points.push({ day: st.bucketDay, rootCause: st.rootCause, count: st.count })
      } else {
        points[at].count += st.count
      }
    }
    // 统计行按 (天, 根因) 升序读入时输出同样有序；这里显式排序，不依赖集合内的插入顺序。
    points.sort((a, b) => a.day.localeCompare(b.day) || a.rootCause.localeCompare(b.rootCause))
    return HttpResponse.json({
      instanceId: inst.id,
      days,
      total,
      points,
      byRootCause: [...byCause.entries()]
        .map(([rootCause, count]) => ({ rootCause, count }))
        .sort((a, b) => b.count - a.count || a.rootCause.localeCompare(b.rootCause)),
      topSignatures: [...bySignature.values()].sort((a, b) => b.count - a.count || a.signature.localeCompare(b.signature)),
    })
  }),

  // ---- 整机快照（FR-466）：时间点可回滚点；回滚前强制建 pre_rollback 快照 ----
  domainRoute('get', '/instances/:id/snapshots', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const rows = instanceSnapshots
      .list((s) => s.instanceId === inst.id)
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id - a.id)
    return HttpResponse.json(rows)
  }),

  domainRoute('post', '/instances/:id/snapshots', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number(info.params.id)
    const inst = instances.get(instanceId)
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const body = (await info.request.json().catch(() => ({}))) as { name?: string }
    const now = new Date().toISOString()
    const newId = allocateSnapshotId()
    const row: MockInstanceSnapshot = {
      id: newId,
      uuid: `snap-${newId}`,
      instanceId,
      name: body.name?.trim() || `快照 ${now.slice(0, 19).replace('T', ' ')}`,
      kind: 'manual',
      state: 'completed',
      rootBackupId: 900 + newId,
      binaryName: 'server.jar',
      binarySha256: 'a'.repeat(64),
      configHash: 'c'.repeat(64),
      configSummary: `start=${inst.startCommand}`,
      triggeredBy: 1,
      triggeredByRollbackId: 0,
      sizeMb: 128.5,
      failureReason: '',
      note: inst.status === 'RUNNING' ? '创建时实例未停止：MC 世界文件可能非一致，回滚后请核对存档' : '',
      createdAt: now,
      updatedAt: now,
    }
    const inserted = instanceSnapshots.insert(row)
    return HttpResponse.json(inserted, { status: 202 })
  }),

  domainRoute('post', '/snapshots/:sid/rollback', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const sid = Number(info.params.sid)
    const target = instanceSnapshots.get(sid)
    if (!target) return HttpResponse.json({ error: 'NOT_FOUND', message: '快照不存在' }, { status: 404 })
    if (target.state !== 'completed' && target.state !== 'rolled_back') {
      return HttpResponse.json({ error: 'CONFLICT', message: `快照未完成，无法作为回滚目标（当前状态 ${target.state}）` }, { status: 409 })
    }
    const now = new Date().toISOString()
    // 强制回滚前快照：与真 CP 同语义，使「回滚仍是可回滚的」在 mock 下也可验证。
    const preId = allocateSnapshotId()
    const pre: MockInstanceSnapshot = {
      ...target,
      id: preId,
      uuid: `snap-pre-${preId}`,
      name: `回滚前自动快照 ${now.slice(0, 19).replace('T', ' ')}`,
      kind: 'pre_rollback',
      state: 'completed',
      triggeredByRollbackId: sid,
      createdAt: now,
      updatedAt: now,
    }
    const insertedPre = instanceSnapshots.insert(pre)
    instanceSnapshots.update(sid, { state: 'rolled_back', updatedAt: now })
    instances.update(target.instanceId, { status: 'STOPPED', statusReason: undefined })
    return HttpResponse.json(
      {
        taskId: `task-snapshot-rollback-${sid}`,
        snapshotId: sid,
        preRollbackSnapshotId: insertedPre.id,
        instanceId: target.instanceId,
        stopped: true,
        binaryMismatch: false,
        finalStatus: 'STOPPED',
      },
      { status: 202 },
    )
  }),

  domainRoute('delete', '/snapshots/:sid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instanceSnapshots.remove(Number(info.params.sid))
    return HttpResponse.json({ deleted: true })
  }),

  // ---- 运行期配额（FR-467）：限额来源 + 实时用量 + 强制状态 ----
  domainRoute('get', '/instances/:id/quota', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    // 组归属：与真机同口径取「磁盘额度最严的那个组」不可行（mock 未建模用户组↔实例关系，
    // 此处退化为「是否属于任一实例分组」）。关键语义仍保住：**无组即 none**——
    // 旧实现无条件回 'group'，使「不限」的实例在面板上被谎报成「组派生」。
    const groupId = groupMembers.list((m) => m.instanceId === inst.id)[0]?.groupId ?? 0
    const diskLimitMb = inst.diskLimitMb ?? 0
    return HttpResponse.json({
      instanceId: inst.id,
      cpuCores: inst.cpuLimit ?? 0,
      memLimitMb: inst.memLimitMb ?? 0,
      diskLimitMb,
      enforceMode: 'alert',
      memSource: (inst.memLimitMb ?? 0) > 0 ? 'instance' : 'none',
      // 实例级优先；否则仅有组时才是组派生（组内无余量也应是 none，mock 无以建模余量，从简）。
      diskSource: diskLimitMb > 0 ? 'instance' : groupId !== 0 ? 'group' : 'none',
      cpuSource: (inst.cpuLimit ?? 0) > 0 ? 'instance' : 'none',
      groupId,
      cpuPercent: inst.status === 'RUNNING' ? 42.5 : 0,
      rssBytes: inst.status === 'RUNNING' ? 512 * 1024 * 1024 : 0,
      diskBytes: 3 * 1024 * 1024 * 1024,
      sampleNote: inst.status === 'RUNNING' ? undefined : '实例未运行，无实时用量',
      enforcedCpu: false,
      enforcedMem: false,
      enforcedDisk: false,
      // s-2：强制状态只看本进程内存计数，重启后重新累积——必须显式标注作用域。
      enforceStateScope: 'in_process',
      // 待收紧限额（throttle 档触发时登记，下次启动生效）；0=无。
      throttleCpuLimit: inst.throttleCpuLimit ?? 0,
      throttleMemLimitMb: inst.throttleMemLimitMb ?? 0,
      supportedThrottle: inst.processType === 'docker',
    })
  }),

  // ---- 二进制/Beacon 版本管理（FR-468）：当前版本 + 可升级候选 + 一级回滚 ----
  domainRoute('get', '/instances/:id/binary-version', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const bound = inst.type === 'generic'
    if (!bound) {
      return HttpResponse.json({
        instanceId: inst.id,
        bound: false,
        currentAssetId: 0,
        currentVersion: '',
        currentFilename: '',
        currentSha256: '',
        hasRollback: false,
        previousAssetId: 0,
        previousVersion: '',
        previousFilename: '',
        driftDetected: false,
        noLibraryVersion: false,
        note: '该实例未登记二进制版本绑定（非 binary/beacon 实例，或搭建早于版本管理落地）',
        candidates: [],
      })
    }
    // 版本绑定是**可变**的：升级/回滚真在本 mock 内交换 Current/Previous，
    // 否则 hasRollback 恒 false、回滚按钮永远禁用——回滚的二次确认、失败提示与
    // 回滚后的版本变化在 mock 下完全不可达（FR-468 的「可回滚」这一半无法演练）。
    const binding = binaryBindings.get(inst.id) ?? seedBinaryBinding(inst.id)
    return HttpResponse.json({
      instanceId: inst.id,
      bound: true,
      currentAssetId: binding.currentAssetId,
      currentVersion: binding.currentVersion,
      currentFilename: binding.currentFilename,
      currentSha256: binding.currentSha256,
      hasRollback: binding.previousAssetId !== 0,
      previousAssetId: binding.previousAssetId,
      previousVersion: binding.previousVersion,
      previousFilename: binding.previousFilename,
      driftDetected: false,
      noLibraryVersion: false,
      candidates: BINARY_CANDIDATES,
    })
  }),

  domainRoute('post', '/instances/:id/binary-upgrade', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (isInstanceLive(inst.status)) {
      return HttpResponse.json(
        { error: 'CONFLICT', message: `实例运行中，请先停止实例再执行版本变更（当前状态 ${inst.status}）` },
        { status: 409 },
      )
    }
    const body = (await info.request.json().catch(() => ({}))) as { assetId?: number }
    const target = BINARY_CANDIDATES.find((c) => c.assetId === Number(body.assetId))
    if (!target) return HttpResponse.json({ error: 'INVALID_TARGET', message: '目标版本不存在' }, { status: 400 })
    const binding = binaryBindings.get(inst.id) ?? seedBinaryBinding(inst.id)
    // **必须在写入前快照 from-\* 各字段**：`binding` 是集合内那一行的**活引用**，
    // `binaryBindings.update` 会就地 Object.assign 覆盖它，之后再读 binding.currentVersion
    // 拿到的已是交换后的值 —— 响应里的 from/to 会双双等于新版本，语义完全失真。
    const fromAssetId = binding.currentAssetId
    const fromVersion = binding.currentVersion
    // 升级即换绑定 + 记录回滚点（Current → Previous），与后端语义一致。
    const next = {
      currentAssetId: target.assetId,
      currentVersion: target.version,
      currentFilename: target.filename,
      currentSha256: target.sha256,
      previousAssetId: fromAssetId,
      previousVersion: fromVersion,
      previousFilename: binding.currentFilename,
    }
    binaryBindings.update(inst.id, next)
    instances.update(inst.id, { startCommand: `./${target.filename}${BINARY_START_ARGS}` })
    return HttpResponse.json({
      taskId: `task-binary-upgrade-${inst.id}`,
      instanceId: inst.id,
      fromAssetId,
      fromVersion,
      toAssetId: next.currentAssetId,
      toVersion: next.currentVersion,
      toFilename: next.currentFilename,
      startCommand: `./${target.filename}${BINARY_START_ARGS}`,
      commandChanged: true,
    })
  }),

  domainRoute('post', '/instances/:id/binary-rollback', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (isInstanceLive(inst.status)) {
      return HttpResponse.json(
        { error: 'CONFLICT', message: `实例运行中，请先停止实例再执行版本变更（当前状态 ${inst.status}）` },
        { status: 409 },
      )
    }
    const binding = binaryBindings.get(inst.id) ?? seedBinaryBinding(inst.id)
    if (binding.previousAssetId === 0) {
      return HttpResponse.json({ error: 'NO_PREVIOUS_VERSION', message: '没有可回滚的上一版本' }, { status: 400 })
    }
    // 同升级：先快照 from-\* 各字段，update 会就地覆盖 `binding`（活引用）。
    const fromAssetId = binding.currentAssetId
    const fromVersion = binding.currentVersion
    // 回滚 = 对称交换 Current ↔ Previous（回滚本身也可再回滚）。
    const next = {
      currentAssetId: binding.previousAssetId,
      currentVersion: binding.previousVersion,
      currentFilename: binding.previousFilename,
      currentSha256: BINARY_CANDIDATES.find((c) => c.assetId === binding.previousAssetId)?.sha256 ?? 'b'.repeat(64),
      previousAssetId: fromAssetId,
      previousVersion: fromVersion,
      previousFilename: binding.currentFilename,
    }
    binaryBindings.update(inst.id, next)
    instances.update(inst.id, { startCommand: `./${next.currentFilename}${BINARY_START_ARGS}` })
    return HttpResponse.json({
      taskId: `task-binary-rollback-${inst.id}`,
      instanceId: inst.id,
      fromAssetId,
      fromVersion,
      toAssetId: next.currentAssetId,
      toVersion: next.currentVersion,
      toFilename: next.currentFilename,
      startCommand: `./${next.currentFilename}${BINARY_START_ARGS}`,
      commandChanged: true,
    })
  }),

  // ---- 端口占用（FR-032）----
  domainRoute('get', '/nodes/:id/ports', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number(info.params.id)
    const occupied = instances
      .list((i) => i.nodeId === nodeId)
      .map((i) => ({ instanceId: i.id, name: i.name, role: i.role, serverPort: i.serverPort, queryPort: 0 }))
    return HttpResponse.json({ nodeId, ranges: { serverPortBase: 25565, rangeSize: 100 }, occupied })
  }),

  // ---- 组织分组树（FR-165）----
  domainRoute('get', '/instance-groups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const rows = instanceGroups.list().map((g) => ({
      id: g.id,
      uuid: g.uuid,
      name: g.name,
      parentId: g.parentId,
      sort: g.sort,
      instanceCount: subtreeInstanceIds(g.id).length,
      memberInstanceIds: directMemberIds(g.id),
    }))
    return HttpResponse.json(rows)
  }),

  domainRoute('post', '/instance-groups', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name?: string; parentId?: number | null }
    if (!body.name) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '名称为空' }, { status: 400 })
    if (body.parentId != null && !instanceGroups.get(body.parentId))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_PARENT_NOT_FOUND', message: '父分组不存在' }, { status: 400 })
    const created = instanceGroups.insert({
      id: groupSeq++,
      uuid: `g-${Date.now()}`,
      name: body.name,
      parentId: body.parentId ?? null,
      sort: 0,
    })
    return HttpResponse.json({ ...created, instanceCount: 0 }, { status: 201 })
  }),

  domainRoute('put', '/instance-groups/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instanceGroups.get(id))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_FOUND', message: '分组不存在' }, { status: 404 })
    const body = (await info.request.json()) as { name?: string; parentId?: number | null }
    const patch: Partial<MockInstanceGroup> = {}
    if (body.name !== undefined) patch.name = body.name
    if ('parentId' in body) patch.parentId = body.parentId ?? null
    const updated = instanceGroups.update(id, patch)
    return HttpResponse.json({ ...updated, instanceCount: subtreeInstanceIds(id).length })
  }),

  domainRoute('delete', '/instance-groups/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const hasChild = instanceGroups.list((g) => g.parentId === id).length > 0
    const hasMember = groupMembers.list((m) => m.groupId === id).length > 0
    if (hasChild || hasMember)
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_EMPTY', message: '分组非空' }, { status: 409 })
    instanceGroups.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('get', '/instance-groups/:id/instances', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instanceGroups.get(id))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_FOUND', message: '分组不存在' }, { status: 404 })
    return HttpResponse.json({ instanceIds: subtreeInstanceIds(id) })
  }),

  domainRoute('post', '/instance-groups/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { instanceIds = [] } = (await info.request.json()) as { instanceIds?: number[] }
    let added = 0
    for (const instanceId of instanceIds) {
      const exists = groupMembers.find((m) => m.groupId === id && m.instanceId === instanceId)
      if (!exists && instances.get(instanceId)) {
        groupMembers.insert({ id: memberSeq++, groupId: id, instanceId })
        added++
      }
    }
    return HttpResponse.json({ added, members: subtreeInstanceIds(id).map(memberView) })
  }),

  domainRoute('delete', '/instance-groups/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { instanceIds = [] } = (await info.request.json()) as { instanceIds?: number[] }
    for (const instanceId of instanceIds) {
      const m = groupMembers.find((x) => x.groupId === id && x.instanceId === instanceId)
      if (m) groupMembers.remove(m.id)
    }
    return new HttpResponse(null, { status: 204 })
  }),
]
