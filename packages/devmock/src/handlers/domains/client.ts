import { http, HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { requireAuth } from '@jianmanager/devmock/auth-middleware'

/**
 * 客户端分发与平台设置域 mock handler（FR-210，照 spec §7 范式）。
 * 覆盖 clientChannels / clientVersions / clientStats / licenses / settings 五个 api 模块的每个 endpoint。
 * 受保护端点首行 requireAuth；字段严格匹配 web/src/api/{clientChannels,clientVersions,clientStats,licenses,settings}.ts。
 */

// ── client-channels（频道 + 拉取密钥，FR-086/187）────────────────────────────

/** 假后端分发频道（匹配 ClientChannel；createdAt/updatedAt 为字符串）。 */
interface MockChannel {
  id: number
  channelId: string
  name: string
  description: string
  currentVersion: number
  createdAt: string
  updatedAt: string
}

/** 假后端拉取密钥（匹配 ClientPullKey，含 keyHash 仅 mock 内部用于 reveal 回放）。 */
interface MockKey {
  id: number
  channelId: string
  name: string
  keyPrefix: string
  /** mock 内部留存的明文，供 reveal/创建一次性回显（真后端为可逆加密 KeyEnc）。 */
  plain: string
  revoked: boolean
  expiresAt: string | null
  lastUsedAt: string | null
  createdAt: string
  revealable: boolean
}

/** 假后端版本（匹配 ClientVersionDetail；files/managedDirs 为真数组，agent 可选）。 */
interface MockVersion {
  id: number
  channelId: string
  version: number
  note: string
  createdBy: number
  createdAt: string
  managedDirs: string[]
  /** 运营自定义追加排除（FR-255）。 */
  cleanExclude?: string[]
  files: import('@/api/clientVersions').ManifestFile[]
  agent?: import('@/api/clientVersions').ManifestAgent
}

const channels = db<MockChannel>('client-channels', () => [
  {
    id: 1,
    channelId: 'skyblock-s1',
    name: '空岛一区',
    description: '空岛生存主分发频道',
    currentVersion: 2,
    createdAt: '2026-06-01T08:00:00Z',
    updatedAt: '2026-06-20T08:00:00Z',
  },
  {
    id: 2,
    channelId: 'survival-s2',
    name: '生存二区',
    description: '生存服灰度频道',
    currentVersion: 0,
    createdAt: '2026-06-10T08:00:00Z',
    updatedAt: '2026-06-10T08:00:00Z',
  },
])

const keys = db<MockKey>('client-keys', () => [
  {
    id: 1,
    channelId: 'skyblock-s1',
    name: '正式包',
    keyPrefix: 'jmck_ab12',
    plain: 'jmck_ab12_secret_release',
    revoked: false,
    expiresAt: null,
    lastUsedAt: '2026-06-25T10:00:00Z',
    createdAt: '2026-06-01T09:00:00Z',
    revealable: true,
  },
  {
    id: 2,
    channelId: 'skyblock-s1',
    name: '灰度包',
    keyPrefix: 'jmck_cd34',
    plain: 'jmck_cd34_secret_canary',
    revoked: false,
    expiresAt: null,
    lastUsedAt: null,
    createdAt: '2026-06-05T09:00:00Z',
    revealable: true,
  },
])

const versions = db<MockVersion>('client-versions', () => [
  {
    id: 1,
    channelId: 'skyblock-s1',
    version: 1,
    note: '首发版本',
    createdBy: 1,
    createdAt: '2026-06-01T10:00:00Z',
    managedDirs: ['mods', 'config'],
    files: [
      {
        path: 'mods/example.jar',
        sha256: 'a'.repeat(64),
        md5: 'b'.repeat(32),
        size: 1024,
        sync: 'strict',
        platform: '',
        artifact: { sha256: 'a'.repeat(64), size: 512, codec: 'zstd' },
      },
    ],
  },
  {
    id: 2,
    channelId: 'skyblock-s1',
    version: 2,
    note: '修复材质包',
    createdBy: 1,
    createdAt: '2026-06-20T10:00:00Z',
    managedDirs: ['mods', 'config', 'resourcepacks'],
    files: [
      {
        path: 'mods/example.jar',
        sha256: 'a'.repeat(64),
        md5: 'b'.repeat(32),
        size: 1024,
        sync: 'strict',
        platform: '',
        artifact: { sha256: 'a'.repeat(64), size: 512, codec: 'zstd' },
      },
      {
        path: 'resourcepacks/pack.zip',
        sha256: 'c'.repeat(64),
        md5: 'd'.repeat(32),
        size: 4096,
        sync: 'once',
        platform: '',
        artifact: { sha256: 'c'.repeat(64), size: 2048, codec: 'zstd' },
      },
    ],
  },
])

/** 假后端分发明细事件（匹配 ClientDistEvent，FR-093/249）。 */
interface MockDistEvent {
  id: number
  channelId: string
  machineId: string
  playerName?: string
  coreVersion?: string
  ip: string
  kind: string
  version: number
  artifactSha: string
  bytes: number
  status: number
  errCode: string
  errReason?: string
  method?: string
  path?: string
  etag?: string
  requestHeaders?: Record<string, string>
  responseHeaders?: Record<string, string>
  durationMs: number
  createdAt: string
}

const distEvents = db<MockDistEvent>('client-dist-events', (): MockDistEvent[] => [
  {
    id: 1,
    channelId: 'skyblock-s1',
    machineId: 'm-aaaa',
    ip: '203.0.113.1',
    kind: 'manifest',
    version: 2,
    artifactSha: '',
    bytes: 1200,
    status: 200,
    errCode: '',
    method: 'GET',
    path: '/api/v1/client-channels/skyblock-s1/manifest',
    etag: '"v2"',
    requestHeaders: { 'User-Agent': 'JM-Updater/1.0', 'X-Client-Key': 'present', 'X-Machine-Id': 'm-aaaa' },
    responseHeaders: { ETag: '"v2"', 'Cache-Control': 'no-store' },
    durationMs: 4,
    createdAt: '2026-06-28T10:05:00Z',
  },
  {
    id: 2,
    channelId: 'skyblock-s1',
    machineId: 'm-bbbb',
    ip: '198.51.100.7',
    kind: 'artifact',
    version: 0,
    artifactSha: 'a'.repeat(64),
    bytes: 2048,
    status: 206,
    errCode: '',
    method: 'GET',
    path: '/api/v1/client-artifacts/aaaaaaaaaaaa',
    etag: '"artifact-a"',
    requestHeaders: { 'User-Agent': 'JM-Updater/1.0', Range: 'bytes=0-2047', 'X-Machine-Id': 'm-bbbb' },
    responseHeaders: { ETag: '"artifact-a"', 'Content-Range': 'bytes 0-2047/4096' },
    durationMs: 12,
    createdAt: '2026-06-28T10:04:00Z',
  },
  {
    id: 3,
    channelId: 'skyblock-s1',
    machineId: 'm-cccc',
    ip: '203.0.113.9',
    kind: 'manifest',
    version: 0,
    artifactSha: '',
    bytes: 60,
    status: 401,
    errCode: 'INVALID_CLIENT_KEY',
    errReason: '拉取密钥无效',
    method: 'GET',
    path: '/api/v1/client-channels/skyblock-s1/manifest',
    requestHeaders: { 'User-Agent': 'JM-Updater/1.0', 'X-Client-Key': 'present', 'X-Machine-Id': 'm-cccc' },
    responseHeaders: { 'Cache-Control': 'no-store' },
    durationMs: 1,
    createdAt: '2026-06-28T10:03:00Z',
  },
  {
    id: 4,
    channelId: 'survival-s2',
    machineId: 'm-dddd',
    ip: '203.0.113.20',
    kind: 'manifest',
    version: 0,
    artifactSha: '',
    bytes: 80,
    status: 404,
    errCode: 'NO_LATEST_VERSION',
    errReason: '频道尚未发布版本',
    method: 'GET',
    path: '/api/v1/client-channels/survival-s2/manifest',
    requestHeaders: { 'User-Agent': 'JM-Updater/1.0', 'X-Client-Key': 'present', 'X-Machine-Id': 'm-dddd' },
    responseHeaders: { 'Cache-Control': 'no-store' },
    durationMs: 2,
    createdAt: '2026-06-28T10:02:00Z',
  },
])

/** 假后端客户端运行态（匹配 ClientRuntimeState，FR-265）。 */
interface MockRuntimeState {
  id: number
  channelId: string
  machineId: string
  playerName: string
  ip: string
  platform: string
  javaVersion: string
  launcher: string
  coreVersion: string
  localVersion: number
  firstSeenAt: string
  lastHeartbeatAt: string
  createdAt: string
  updatedAt: string
}

const runtimeStates = db<MockRuntimeState>('client-runtime-states', () => [
  {
    id: 1,
    channelId: 'skyblock-s1',
    machineId: 'm-aaaa',
    playerName: 'Alex',
    ip: '203.0.113.1',
    platform: 'windows',
    javaVersion: '21',
    launcher: 'hmcl',
    coreVersion: '3',
    localVersion: 2,
    firstSeenAt: '2026-06-28T09:50:00Z',
    lastHeartbeatAt: '2026-06-28T10:05:00Z',
    createdAt: '2026-06-28T09:50:00Z',
    updatedAt: '2026-06-28T10:05:00Z',
  },
  {
    id: 2,
    channelId: 'skyblock-s1',
    machineId: 'm-bbbb',
    playerName: 'Steve',
    ip: '198.51.100.7',
    platform: 'linux',
    javaVersion: '17',
    launcher: 'pcl',
    coreVersion: '3',
    localVersion: 1,
    firstSeenAt: '2026-06-28T08:10:00Z',
    lastHeartbeatAt: '2026-06-28T10:01:00Z',
    createdAt: '2026-06-28T08:10:00Z',
    updatedAt: '2026-06-28T10:01:00Z',
  },
  {
    id: 3,
    channelId: 'survival-s2',
    machineId: 'm-dddd',
    playerName: 'Herobrine',
    ip: '203.0.113.20',
    platform: 'windows',
    javaVersion: '21',
    launcher: 'hmcl',
    coreVersion: '2',
    localVersion: 0,
    firstSeenAt: '2026-06-28T09:00:00Z',
    lastHeartbeatAt: '2026-06-28T09:58:00Z',
    createdAt: '2026-06-28T09:00:00Z',
    updatedAt: '2026-06-28T09:58:00Z',
  },
])

/** 假后端分块上传会话（FR-251）：内存态，仅按 index 记账，不落字节。 */
interface MockUpload {
  channelId: string
  filename: string
  totalSize: number
  chunkSize: number
  chunkCount: number
  received: Set<number>
}

/** 分块上传会话表（模块级内存态，进程内共享；与真后端会话内存态语义一致）。 */
const uploads = new Map<string, MockUpload>()
/** uploadId 自增序列（mock 稳定可预测）。 */
let uploadSeq = 0

/**
 * 已知制品登记表（FR-346 秒传预查镜像）：sha256 → {md5,size}。
 * 聚合上传 / 分块 complete（带 expectedSha256）成功后登记；预查按 sha+size 全等命中。
 * 与真后端 CAS 语义一致：同内容重复发布 → 预查命中 → 零字节上传。
 */
const knownArtifacts = new Map<string, { md5: string; size: number }>()

/** 由 sha256 派生稳定伪 md5（mock 不真算内容；取前 32 hex 保证确定且互异）。 */
const pseudoMd5 = (sha256: string) => sha256.slice(0, 32).padEnd(32, '0')

/** 校验 64 位 hex sha256（大小写不敏感），归一小写；非法返回 null。 */
function normalizeSha(raw: unknown): string | null {
  const s = String(raw ?? '').trim().toLowerCase()
  return /^[0-9a-f]{64}$/.test(s) ? s : null
}

/**
 * 从 multipart 原始文本中提取指定表单字段的正文（首个命中 part）。
 * 仅供 mock 消费文本型字段（meta JSON）；不处理编码/二进制（mock 不消费文件字节）。
 */
function extractMultipartField(raw: string, field: string): string | null {
  const marker = `name="${field}"`
  const at = raw.indexOf(marker)
  if (at < 0) return null
  const bodyStart = raw.indexOf('\r\n\r\n', at)
  if (bodyStart < 0) return null
  const bodyEnd = raw.indexOf('\r\n--', bodyStart + 4)
  if (bodyEnd < 0) return null
  return raw.slice(bodyStart + 4, bodyEnd)
}

/** 统计 multipart 原始文本中指定表单字段的 part 数（files 计数用；part 头为 ASCII 可靠）。 */
function countMultipartParts(raw: string, field: string): number {
  const marker = `name="${field}"`
  let n = 0
  for (let i = raw.indexOf(marker); i >= 0; i = raw.indexOf(marker, i + marker.length)) n += 1
  return n
}

/** mock 默认分片 8 MiB，夹取到 [1MiB,64MiB]，与后端 clampChunkSize 一致。 */
function clampMockChunk(chunkSize?: number): number {
  const def = 8 * 1024 * 1024
  if (!chunkSize || chunkSize <= 0) return def
  return Math.min(Math.max(chunkSize, 1024 * 1024), 64 * 1024 * 1024)
}

/** 频道下未吊销密钥数（列表 keyCount）。 */
const keyCountOf = (channelId: string) =>
  keys.list((k) => k.channelId === channelId && !k.revoked).length

/** 频道当前 latest 版本号（取该频道最大版本，无则 0）。 */
const latestVersionOf = (channelId: string) =>
  versions.list((v) => v.channelId === channelId).reduce((m, v) => Math.max(m, v.version), 0)

/** 按字段聚合 mock 排行，用于安全总览与剖析。 */
function rankBy<T extends object>(rows: T[], key: keyof T) {
  const m = new Map<string, { subject: string; count: number; bytes: number }>()
  for (const row of rows) {
    const subject = String(row[key] ?? '')
    if (!subject) continue
    const cur = m.get(subject) ?? { subject, count: 0, bytes: 0 }
    cur.count += 1
    cur.bytes += Number((row as { bytes?: unknown }).bytes ?? 0)
    m.set(subject, cur)
  }
  return [...m.values()].sort((a, b) => b.count - a.count || a.subject.localeCompare(b.subject))
}

/** 序列化频道为列表项（带 keyCount，currentVersion 实时由版本派生）。 */
function channelToSummary(ch: MockChannel) {
  return { ...ch, currentVersion: latestVersionOf(ch.channelId), keyCount: keyCountOf(ch.channelId) }
}

/** 序列化密钥元数据（剥离 mock 内部 plain 字段，匹配 ClientPullKey）。 */
function keyToMeta(k: MockKey) {
  return {
    id: k.id,
    name: k.name,
    keyPrefix: k.keyPrefix,
    revoked: k.revoked,
    expiresAt: k.expiresAt,
    lastUsedAt: k.lastUsedAt,
    createdAt: k.createdAt,
    revealable: k.revealable,
  }
}

// ── client-dist 统计（FR-095）────────────────────────────────────────────────

/** 构造频道分发统计（匹配 ClientDistStats；只从分发请求事件派生）。 */
function buildStats(channelId: string, days: number) {
  const rows = distEvents.list((e) => !channelId || e.channelId === channelId)
  const byDay = new Map<string, { day: string; requests: number; bytes: number }>()
  const versions = new Map<number, number>()
  const ips = new Map<string, number>()
  const machines = new Set<string>()
  let success = 0
  let failure = 0
  for (const e of rows) {
    const day = e.createdAt.slice(0, 10)
    const cur = byDay.get(day) ?? { day, requests: 0, bytes: 0 }
    cur.requests += 1
    cur.bytes += e.bytes
    byDay.set(day, cur)
    if (e.kind === 'manifest' && e.version > 0) versions.set(e.version, (versions.get(e.version) ?? 0) + 1)
    if (e.status >= 400) failure += 1
    else if (e.status > 0) success += 1
    if (e.ip) ips.set(e.ip, (ips.get(e.ip) ?? 0) + 1)
    if (e.machineId) machines.add(e.machineId)
  }
  const total = success + failure
  return {
    channelId,
    days,
    downloads: [...byDay.values()].sort((a, b) => a.day.localeCompare(b.day)),
    versions: [...versions.entries()].map(([version, requests]) => ({ version, requests })).sort((a, b) => b.requests - a.requests),
    results: [
      { result: 'success', count: success },
      { result: 'failure', count: failure },
    ],
    successRate: total > 0 ? success / total : 0,
    failureRate: total > 0 ? failure / total : 0,
    rollbackRate: 0,
    activeMachines: machines.size,
    topIps: [...ips.entries()].map(([ip, count]) => ({ ip, count })).sort((a, b) => b.count - a.count).slice(0, 10),
  }
}

/** 构造频道观测视图（匹配 ClientDistObservability，FR-217）。 */
function buildObservability(channelId: string, range: string) {
  const series = Array.from({ length: 3 }, (_, i) => ({
    ts: `2026-06-2${5 + i}T10:00:00Z`,
    manifestPulls: 120 + i * 10,
    artifactPulls: 35 + i * 5,
    downloadBytes: (120 + i * 10) * 70_000,
    activeMachines: 48 + i,
    updateTotal: 30,
    updateSuccess: 27,
    updateFailStatic: 1,
    updateRolledBack: 1,
    updateError: 1,
  }))
  return {
    channelId,
    from: '2026-06-21T00:00:00Z',
    to: '2026-06-28T00:00:00Z',
    series,
    summary: {
      manifestPulls: 1500,
      artifactPulls: 400,
      downloadBytes: 99_000_000,
      updateTotal: 360,
      updateSuccess: 330,
      updateFailStatic: 10,
      updateRolledBack: 12,
      updateError: 8,
      successRate: 0.9167,
      failStaticRate: 0.0278,
      rollbackRate: 0.0333,
      activeMachines: 512,
      // 短窗（24h/7d）落明细保留窗内→精确去重；长窗超窗→人次近似。
      activeMachinesExact: range === '24h' || range === '7d',
    },
    versionDist: [
      { version: 7, count: 900 },
      { version: 6, count: 600 },
    ],
    platformDist: [
      { os: 'windows', count: 1200 },
      { os: 'linux', count: 300 },
    ],
    lagDist: [
      { lag: 0, count: 320 },
      { lag: 1, count: 30 },
    ],
  }
}

function buildErrorSummary(channelId: string) {
  const rows = distEvents
    .list((e) => (!channelId || e.channelId === channelId) && (e.status >= 400 || !!e.errCode))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  const counts = new Map<string, number>()
  for (const row of rows) {
    const code = row.errCode || `HTTP_${row.status}`
    counts.set(code, (counts.get(code) ?? 0) + 1)
  }
  return {
    from: '2026-06-21T00:00:00Z',
    to: '2026-06-28T23:59:59Z',
    topErrors: [...counts.entries()]
      .map(([errCode, count]) => ({ errCode, count }))
      .sort((a, b) => b.count - a.count || a.errCode.localeCompare(b.errCode))
      .slice(0, 10),
    samples: rows.slice(0, 20).map((row) => ({
      id: row.id,
      time: row.createdAt,
      channelId: row.channelId,
      kind: row.kind,
      errCode: row.errCode || `HTTP_${row.status}`,
      errReason: row.errReason ?? '',
      status: row.status,
      ip: row.ip.replace(/\.[^.]+$/, '.*'),
      machineId: row.machineId.length > 10 ? `${row.machineId.slice(0, 6)}…${row.machineId.slice(-4)}` : '***',
    })),
  }
}

function buildRealtime(channelId: string) {
  const rows = distEvents.list((e) => !channelId || e.channelId === channelId)
  const summaryRows = rows.filter((e) => e.createdAt >= '2026-06-28T09:05:00Z')
  const byHour = new Map<string, { ts: string; manifest: number; artifact: number; error: number }>()
  const ips = new Map<string, number>()
  const machines = new Set<string>()
  for (const e of rows) {
    const ts = `${e.createdAt.slice(0, 13)}:00:00Z`
    const cur = byHour.get(ts) ?? { ts, manifest: 0, artifact: 0, error: 0 }
    if (e.kind === 'manifest') cur.manifest += 1
    if (e.kind === 'artifact') cur.artifact += 1
    if (e.status >= 400) cur.error += 1
    byHour.set(ts, cur)
  }
  for (const e of summaryRows) {
    if (e.ip) ips.set(e.ip, (ips.get(e.ip) ?? 0) + 1)
    if (e.machineId) machines.add(e.machineId)
  }
  return {
    summary1h: {
      manifestPulls: summaryRows.filter((e) => e.kind === 'manifest').length,
      artifactPulls: summaryRows.filter((e) => e.kind === 'artifact').length,
      errorRequests: summaryRows.filter((e) => e.status >= 400).length,
      activeMachines: machines.size,
    },
    requestRate24h: [...byHour.values()].sort((a, b) => a.ts.localeCompare(b.ts)),
    recentErrors: rows
      .filter((e) => e.status >= 400)
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
      .slice(0, 10)
      .map((e) => ({
        id: e.id,
        time: e.createdAt,
        channelId: e.channelId,
        kind: e.kind,
        target: e.kind === 'manifest' && e.version > 0 ? `v${e.version}` : e.artifactSha.slice(0, 12),
        ip: e.ip,
        status: e.status,
        errCode: e.errCode,
      })),
    topIps1h: [...ips.entries()].map(([ip, count]) => ({ ip, count })).sort((a, b) => b.count - a.count).slice(0, 10),
  }
}

function buildRuntimeOverview(channelId: string, range: string) {
  const rows = runtimeStates.list((s) => !channelId || s.channelId === channelId)
  const latest = new Map<string, number>()
  for (const row of rows) latest.set(row.channelId, Math.max(latest.get(row.channelId) ?? 0, row.localVersion))
  const updateResultSeries = [
    { ts: '2026-06-27T00:00:00Z', success: 6, failStatic: 1, rolledBack: 0, error: 1 },
    { ts: '2026-06-28T00:00:00Z', success: 8, failStatic: 0, rolledBack: 1, error: 1 },
  ]
  const total = updateResultSeries.reduce((sum, p) => sum + p.success + p.failStatic + p.rolledBack + p.error, 0)
  const success = updateResultSeries.reduce((sum, p) => sum + p.success, 0)
  const failure = updateResultSeries.reduce((sum, p) => sum + p.failStatic + p.error, 0)
  return {
    channelId,
    from: range === '24h' ? '2026-06-27T10:00:00Z' : '2026-06-21T10:00:00Z',
    to: '2026-06-28T10:00:00Z',
    summary: {
      recentStarted: rows.filter((s) => s.lastHeartbeatAt >= '2026-06-28T10:00:00Z').length,
      todayStarted: rows.filter((s) => s.lastHeartbeatAt >= '2026-06-28T00:00:00Z').length,
      recentStarts: rows.filter((s) => s.lastHeartbeatAt >= '2026-06-28T10:00:00Z').length,
      todayStarts: rows.filter((s) => s.lastHeartbeatAt >= '2026-06-28T00:00:00Z').length,
      updateSuccessRate: total > 0 ? success / total : 0,
      updateFailureRate: total > 0 ? failure / total : 0,
    },
    items: rows.sort((a, b) => b.lastHeartbeatAt.localeCompare(a.lastHeartbeatAt)),
    runtimeVersionDist: countRuntime(rows, (s) => String(s.localVersion)).map((x) => ({ version: Number(x.value), count: x.count })),
    coreVersionDist: countRuntime(rows, (s) => s.coreVersion),
    platformDist: countRuntime(rows, (s) => s.platform),
    launcherDist: countRuntime(rows, (s) => s.launcher),
    lagDist: countRuntime(rows, (s) => String(Math.max(0, (latest.get(s.channelId) ?? s.localVersion) - s.localVersion))).map((x) => ({ lag: Number(x.value), count: x.count })),
    updateResultSeries,
  }
}

function countRuntime(rows: MockRuntimeState[], pick: (s: MockRuntimeState) => string) {
  const m = new Map<string, number>()
  for (const row of rows) m.set(pick(row), (m.get(pick(row)) ?? 0) + 1)
  return [...m.entries()].map(([value, count]) => ({ value, count })).sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
}

function searchDistEvents(url: URL) {
  const channelId = url.searchParams.get('channelId') ?? ''
  const machineId = url.searchParams.get('machineId') ?? ''
  const kind = url.searchParams.get('kind') ?? ''
  const outcome = url.searchParams.get('outcome') ?? ''
  const errCode = url.searchParams.get('errCode') ?? ''
  const runtimeVersion = url.searchParams.get('runtimeVersion')
  const coreVersion = url.searchParams.get('coreVersion') ?? ''
  const platform = url.searchParams.get('platform') ?? ''
  const lag = url.searchParams.get('lag')
  let rows = distEvents.list()
  if (channelId) rows = rows.filter((e) => e.channelId === channelId)
  if (machineId) rows = rows.filter((e) => e.machineId === machineId)
  if (kind) rows = rows.filter((e) => e.kind === kind)
  if (outcome === 'failure') rows = rows.filter((e) => e.status >= 400)
  else if (outcome === 'success') rows = rows.filter((e) => e.status > 0 && e.status < 400)
  if (errCode) rows = rows.filter((e) => e.errCode === errCode)
  if (runtimeVersion || coreVersion || platform || lag) {
    const states = runtimeStates.list((s) => {
      if (channelId && s.channelId !== channelId) return false
      if (runtimeVersion && s.localVersion !== Number(runtimeVersion)) return false
      if (coreVersion && s.coreVersion !== coreVersion) return false
      if (platform && s.platform !== platform) return false
      if (lag) {
        const latest = Math.max(...runtimeStates.list((x) => x.channelId === s.channelId).map((x) => x.localVersion))
        if (Math.max(0, latest - s.localVersion) !== Number(lag)) return false
      }
      return true
    })
    const machines = new Set(states.map((s) => s.machineId))
    rows = rows.filter((e) => machines.has(e.machineId))
  }
  return rows
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map((event) => {
      const state = runtimeStates.find((s) => s.channelId === event.channelId && s.machineId === event.machineId)
      return { ...event, playerName: state?.playerName ?? '', coreVersion: state?.coreVersion ?? '' }
    })
}

export const handlers = [
  // 频道列表（受保护）。
  domainRoute('get', '/client-channels', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(channels.list().map(channelToSummary))
  }),

  // 频道详情（含密钥列表）。
  domainRoute('get', '/client-channels/:channelId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const ch = channels.find((c) => c.channelId === channelId)
    if (!ch) return HttpResponse.json({ error: 'NOT_FOUND', message: '频道不存在' }, { status: 404 })
    return HttpResponse.json({
      ...channelToSummary(ch),
      keys: keys.list((k) => k.channelId === channelId).map(keyToMeta),
    })
  }),

  // 创建频道。
  domainRoute('post', '/client-channels', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { channelId: string; name: string; description?: string }
    const now = '2026-06-28T00:00:00Z'
    const ch = channels.insert({
      channelId: body.channelId,
      name: body.name,
      description: body.description ?? '',
      currentVersion: 0,
      createdAt: now,
      updatedAt: now,
    })
    return HttpResponse.json(channelToSummary(ch), { status: 201 })
  }),

  // 删除频道（连同其密钥/版本）。
  domainRoute('delete', '/client-channels/:channelId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const ch = channels.find((c) => c.channelId === channelId)
    if (ch) channels.remove(ch.id)
    keys.list((k) => k.channelId === channelId).forEach((k) => keys.remove(k.id))
    versions.list((v) => v.channelId === channelId).forEach((v) => versions.remove(v.id))
    return new HttpResponse(null, { status: 204 })
  }),

  // 创建拉取密钥（返回一次性明文 key）。
  domainRoute('post', '/client-channels/:channelId/keys', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json()) as { name: string; expiresAt?: string; value?: string }
    const plain = body.value?.trim() || `jmck_${Math.random().toString(36).slice(2, 10)}`
    const k = keys.insert({
      channelId,
      name: body.name,
      keyPrefix: plain.slice(0, 9),
      plain,
      revoked: false,
      expiresAt: body.expiresAt ?? null,
      lastUsedAt: null,
      createdAt: '2026-06-28T00:00:00Z',
      revealable: true,
    })
    return HttpResponse.json({ ...keyToMeta(k), key: plain }, { status: 201 })
  }),

  // 编辑密钥（改名 / 改值；改值回显新明文）。
  domainRoute('put', '/client-channels/:channelId/keys/:keyId', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const keyId = Number(info.params.keyId)
    const body = (await info.request.json()) as { name: string; value?: string }
    const k = keys.get(keyId)
    if (!k) return HttpResponse.json({ error: 'NOT_FOUND', message: '密钥不存在' }, { status: 404 })
    const newValue = body.value?.trim()
    const patch: Partial<MockKey> = { name: body.name }
    if (newValue) {
      patch.plain = newValue
      patch.keyPrefix = newValue.slice(0, 9)
      patch.revealable = true
    }
    const updated = keys.update(keyId, patch)!
    // 仅改值时回显新明文（前端据 key 非空判定是否弹一次性明文弹窗）。
    return HttpResponse.json({ ...keyToMeta(updated), key: newValue ?? '' })
  }),

  // 吊销密钥。
  domainRoute('delete', '/client-channels/:channelId/keys/:keyId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const keyId = Number(info.params.keyId)
    keys.update(keyId, { revoked: true })
    return new HttpResponse(null, { status: 204 })
  }),

  // 查看密钥明文（FR-192）：无 KeyEnc → 404 KEY_NOT_REVEALABLE。
  domainRoute('get', '/client-channels/:channelId/keys/:keyId/reveal', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const keyId = Number(info.params.keyId)
    const k = keys.get(keyId)
    if (!k || !k.revealable) {
      return HttpResponse.json({ error: 'KEY_NOT_REVEALABLE', message: '该密钥不可找回' }, { status: 404 })
    }
    return HttpResponse.json({ key: k.plain })
  }),

  // 版本历史列表（版本号 DESC）。
  domainRoute('get', '/client-channels/:channelId/versions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const latest = latestVersionOf(channelId)
    const rows = versions
      .list((v) => v.channelId === channelId)
      .sort((a, b) => b.version - a.version)
      .map((v) => ({
        version: v.version,
        note: v.note,
        fileCount: v.files.length,
        createdBy: v.createdBy,
        createdAt: v.createdAt,
        isLatest: v.version === latest,
      }))
    return HttpResponse.json(rows)
  }),

  // 版本详情（文件清单）。
  domainRoute('get', '/client-channels/:channelId/versions/:version', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const version = Number(info.params.version)
    const v = versions.find((x) => x.channelId === channelId && x.version === version)
    if (!v) return HttpResponse.json({ error: 'NOT_FOUND', message: '版本不存在' }, { status: 404 })
    return HttpResponse.json({
      version: v.version,
      note: v.note,
      createdBy: v.createdBy,
      createdAt: v.createdAt,
      isLatest: v.version === latestVersionOf(channelId),
      managedDirs: v.managedDirs,
      cleanExclude: v.cleanExclude,
      files: v.files,
      agent: v.agent,
    })
  }),

  // 上传单个客户端文件制品（multipart）→ 内容寻址元数据。
  domainRoute('post', '/client-channels/:channelId/files', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      sha256: 'e'.repeat(64),
      md5: 'f'.repeat(32),
      size: 2048,
      codec: 'none',
    })
  }),

  // ── 大文件分块上传（FR-251）：init→chunk→complete→abort ────────────────────
  // 内存假会话：声明大小得 uploadId/chunkSize/chunkCount → 逐片累计已收 → complete 校验齐全回元数据。

  // init：建上传会话。
  domainRoute('post', '/client-channels/:channelId/uploads', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json()) as {
      filename?: string
      totalSize: number
      chunkSize?: number
    }
    // 与真后端一致（BUG 修复）：totalSize=0 合法（空文件如 .gitkeep，chunkCount=0 直达 complete），仅负数/非数拒。
    const totalSize = Number(body.totalSize ?? 0)
    if (!Number.isFinite(totalSize) || totalSize < 0) {
      return HttpResponse.json({ error: 'INVALID_UPLOAD_INIT', message: 'totalSize 不能为负' }, { status: 400 })
    }
    const chunkSize = clampMockChunk(body.chunkSize)
    const chunkCount = Math.ceil(totalSize / chunkSize)
    const uploadId = `mock-upload-${++uploadSeq}`
    uploads.set(uploadId, {
      channelId,
      filename: body.filename ?? '',
      totalSize,
      chunkSize,
      chunkCount,
      received: new Set<number>(),
    })
    return HttpResponse.json({ uploadId, chunkSize, chunkCount }, { status: 201 })
  }),

  // chunk：写入一个分片（原始字节；mock 仅按 index 记账，不校验字节内容）。
  domainRoute('put', '/client-channels/:channelId/uploads/:uploadId/chunks/:index', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const uploadId = String(info.params.uploadId)
    const index = Number(info.params.index)
    const sess = uploads.get(uploadId)
    if (!sess) return HttpResponse.json({ error: 'UPLOAD_NOT_FOUND', message: '上传会话不存在' }, { status: 404 })
    if (sess.channelId !== String(info.params.channelId)) {
      return HttpResponse.json({ error: 'UPLOAD_CHANNEL_MISMATCH', message: '频道不匹配' }, { status: 403 })
    }
    if (index < 0 || index >= sess.chunkCount) {
      return HttpResponse.json({ error: 'INVALID_CHUNK_INDEX', message: '分片序号越界' }, { status: 400 })
    }
    sess.received.add(index)
    return HttpResponse.json({ received: sess.received.size, total: sess.chunkCount })
  }),

  // complete：校验齐全 → 回内容寻址元数据（与单次上传同结构）→ 清会话。
  domainRoute('post', '/client-channels/:channelId/uploads/:uploadId/complete', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const uploadId = String(info.params.uploadId)
    const sess = uploads.get(uploadId)
    if (!sess) return HttpResponse.json({ error: 'UPLOAD_NOT_FOUND', message: '上传会话不存在' }, { status: 404 })
    if (sess.received.size !== sess.chunkCount) {
      return HttpResponse.json({ error: 'UPLOAD_INCOMPLETE', message: '分片不齐全' }, { status: 422 })
    }
    uploads.delete(uploadId)
    // FR-346：complete 可带 expectedSha256（分块路径已算 hash 时强校验）——mock 回显之并登记
    // 进 knownArtifacts（后续预查可命中）；未带时沿用稳定伪 sha（真后端为内容寻址）。
    const body = (await info.request.json().catch(() => ({}))) as { expectedSha256?: string }
    const sha = normalizeSha(body.expectedSha256) ?? 'e'.repeat(64)
    knownArtifacts.set(sha, { md5: pseudoMd5(sha), size: sess.totalSize })
    return HttpResponse.json(
      { sha256: sha, md5: pseudoMd5(sha), size: sess.totalSize, codec: 'none' },
      { status: 201 },
    )
  }),

  // ── 上传增效（FR-346）：秒传预查 + 小文件聚合 ────────────────────────────

  // 秒传预查：≤500 项，命中 knownArtifacts（sha+size 全等）者回与真上传同构结果。
  domainRoute('post', '/client-channels/:channelId/files/precheck', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { files?: { sha256?: string; size?: number }[] }
    const files = body.files ?? []
    if (files.length === 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'files 为空' }, { status: 400 })
    }
    if (files.length > 500) {
      return HttpResponse.json({ error: 'BATCH_LIMIT_EXCEEDED', message: '单次预查最多 500 项' }, { status: 400 })
    }
    const results = files.map((f) => {
      const sha = normalizeSha(f.sha256)
      if (!sha) return { sha256: String(f.sha256 ?? ''), hit: false }
      const known = knownArtifacts.get(sha)
      if (known && known.size === Number(f.size)) {
        return { sha256: sha, hit: true, result: { sha256: sha, md5: known.md5, size: known.size, codec: 'none' } }
      }
      return { sha256: sha, hit: false }
    })
    return HttpResponse.json({ results })
  }),

  // 小文件聚合上传：meta（JSON 数组）+ 同序 files part；结果回声明值并登记（后续预查命中）。
  // 注意：不走 request.formData()——jsdom 测试环境下 undici 的 multipart 解析对 jsdom
  // File 断言失败（与 setup.ts 的 Blob.stream polyfill 同类互操作坑）。mock 只消费 meta
  // 声明与 files part 计数，从原始文本按 multipart 语法提取即可（文件字节不参与结果）。
  domainRoute('post', '/client-channels/:channelId/files/batch', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const raw = await info.request.text()
    const metaJson = extractMultipartField(raw, 'meta')
    let metas: { filename?: string; size?: number; sha256?: string }[]
    try {
      metas = JSON.parse(metaJson ?? '') as typeof metas
    } catch {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'meta JSON 非法' }, { status: 400 })
    }
    const fileParts = countMultipartParts(raw, 'files')
    if (!Array.isArray(metas) || metas.length === 0 || fileParts !== metas.length) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'files part 数与 meta 不符' }, { status: 400 })
    }
    if (metas.length > 200) {
      return HttpResponse.json({ error: 'BATCH_LIMIT_EXCEEDED', message: '单批最多 200 个文件' }, { status: 400 })
    }
    const results = metas.map((m) => {
      const sha = normalizeSha(m.sha256) ?? 'e'.repeat(64)
      const size = Number(m.size ?? 0)
      knownArtifacts.set(sha, { md5: pseudoMd5(sha), size })
      return { sha256: sha, md5: pseudoMd5(sha), size, codec: 'none' }
    })
    return HttpResponse.json({ results }, { status: 201 })
  }),

  // abort：弃单（幂等 204）。
  domainRoute('delete', '/client-channels/:channelId/uploads/:uploadId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    uploads.delete(String(info.params.uploadId))
    return new HttpResponse(null, { status: 204 })
  }),

  // 发布版本：单调递增版本号、切 latest。
  domainRoute('post', '/client-channels/:channelId/versions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json()) as {
      files: import('@/api/clientVersions').ManifestFile[]
      managedDirs: string[]
      cleanExclude?: string[]
      agent?: import('@/api/clientVersions').ManifestAgent
      note?: string
    }
    const nextVersion = latestVersionOf(channelId) + 1
    const v = versions.insert({
      channelId,
      version: nextVersion,
      note: body.note ?? '',
      createdBy: 1,
      createdAt: '2026-06-28T00:00:00Z',
      managedDirs: body.managedDirs,
      cleanExclude: body.cleanExclude,
      files: body.files,
      agent: body.agent,
    })
    const ch = channels.find((c) => c.channelId === channelId)
    if (ch) channels.update(ch.id, { currentVersion: nextVersion })
    return HttpResponse.json({ version: v.version }, { status: 201 })
  }),

  // 运营回滚：以更高版本号重发历史版本内容为新 latest。
  domainRoute('post', '/client-channels/:channelId/rollback', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json()) as { sourceVersion: number; note?: string }
    const src = versions.find((x) => x.channelId === channelId && x.version === body.sourceVersion)
    if (!src) return HttpResponse.json({ error: 'NOT_FOUND', message: '源版本不存在' }, { status: 404 })
    const nextVersion = latestVersionOf(channelId) + 1
    versions.insert({
      channelId,
      version: nextVersion,
      note: body.note ?? `回滚自 v${body.sourceVersion}`,
      createdBy: 1,
      createdAt: '2026-06-28T00:00:00Z',
      managedDirs: src.managedDirs,
      files: src.files,
      agent: src.agent,
    })
    const ch = channels.find((c) => c.channelId === channelId)
    if (ch) channels.update(ch.id, { currentVersion: nextVersion })
    return HttpResponse.json({ version: nextVersion })
  }),

  // 制品文本预览（FR-214，版本详情弹窗「预览」视图用）。
  domainRoute('get', '/client-channels/:channelId/files/content', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const sha = new URL(info.request.url).searchParams.get('sha256') ?? ''
    // mock：返回占位文本，让 FileBrowser 能展示内容。
    const content = `# mock artifact preview\nsha256: ${sha}\n这是 mock 模式下的占位内容。`
    return new HttpResponse(content, {
      status: 200,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    })
  }),

  // 制品下载（FR-214，版本详情弹窗「下载」按钮）。
  domainRoute('get', '/client-channels/:channelId/files/download', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const sha = new URL(info.request.url).searchParams.get('sha256') ?? ''
    const content = `mock artifact download\nsha256: ${sha}\n`
    return new HttpResponse(content, {
      status: 200,
      headers: {
        'Content-Type': 'application/octet-stream',
        'Content-Disposition': `attachment; filename="artifact-${sha.slice(0, 12)}"`,
      },
    })
  }),

  // 分发统计（FR-095）。
  domainRoute('get', '/client-dist/stats', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const channelId = url.searchParams.get('channelId') ?? ''
    const days = Number(url.searchParams.get('days') ?? 30)
    return HttpResponse.json(buildStats(channelId, days))
  }),

  // 客户端分发观测（FR-217，消费方含 FR-220 平台统计页）：跨频道/单频道汇总 + 分布。
  // 平台管理员端点；mock 默认用户 role=10，requireAuth 即可放行（真后端按 role 403）。
  domainRoute('get', '/client-dist/observability', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const channelId = url.searchParams.get('channelId') ?? ''
    const range = url.searchParams.get('range') ?? '7d'
    return HttpResponse.json(buildObservability(channelId, range))
  }),

  // 客户端分发安全（FR-264）：mock 模式下提供最小可验收数据。
  domainRoute('get', '/client-dist/security/overview', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      activeDownloads: 2,
      downloadBytesPerSecond: 4096,
      abnormalRequests: 2,
      unauthorizedRequests: distEvents.list((e) => e.status === 401).length,
      forbiddenRequests: distEvents.list((e) => e.status === 403).length,
      rateLimitedRequests: distEvents.list((e) => e.status === 429).length,
      blockedIpCount: 1,
      throttledKeyCount: 1,
      protectedChannelCount: 1,
      topIps: rankBy(distEvents.list(), 'ip'),
      topKeys: [{ subject: 'jmck_ab12', count: 3 }],
      topChannels: rankBy(distEvents.list(), 'channelId'),
      topPlayers: runtimeStates.list().map((s) => ({ subject: s.playerName, count: 1 })),
    })
  }),

  domainRoute('get', '/client-dist/security/logs', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const type = url.searchParams.get('type') ?? ''
    const playerName = url.searchParams.get('playerName') ?? ''
    const items = [
      ...runtimeStates.list().map((s) => ({ id: `runtime:${s.id}`, type: 'runtime', title: '运行态心跳', channelId: s.channelId, machineId: s.machineId, playerName: s.playerName, ip: s.ip, status: s.coreVersion, createdAt: s.lastHeartbeatAt, detail: { platform: s.platform, javaVersion: s.javaVersion, launcher: s.launcher, localVersion: s.localVersion } })),
      ...distEvents.list().map((e) => ({ id: `request:${e.id}`, type: 'request', title: e.kind, channelId: e.channelId, machineId: e.machineId, playerName: '', ip: e.ip, status: String(e.status), errCode: e.errCode, createdAt: e.createdAt, detail: { path: e.path, bytes: e.bytes, requestHeaders: e.requestHeaders } })),
      { id: 'hello:1', type: 'hello', title: '安全画像上报', channelId: 'skyblock-s1', machineId: 'm-aaaa', playerName: 'Alex', ip: '203.0.113.1', status: 'accepted', createdAt: '2026-06-28T10:06:00Z', detail: { installId: 'install-a', keyPrefix: 'jmck_ab12' } },
      { id: 'telemetry:1', type: 'telemetry', title: '更新遥测', channelId: 'skyblock-s1', machineId: 'm-aaaa', playerName: 'Alex', ip: '203.0.113.1', status: 'success', createdAt: '2026-06-28T10:07:00Z', detail: { fromVersion: 1, toVersion: 2, durationMs: 830 } },
      { id: 'risk:1', type: 'risk', title: 'INVALID_PLAYER_NAME', channelId: 'survival-s2', machineId: 'm-dddd', playerName: 'Herobrine', ip: '203.0.113.20', status: 'low', errCode: 'INVALID_PLAYER_NAME', createdAt: '2026-06-28T10:08:00Z', detail: { reason: '玩家名异常' } },
      { id: 'action:1', type: 'action', title: 'temp_block', channelId: '', machineId: '', playerName: '', ip: '203.0.113.20', status: 'active', createdAt: '2026-06-28T10:09:00Z', detail: { targetType: 'ip', targetValue: '203.0.113.20', reason: '异常拉取' } },
    ].filter((item) => (!type || item.type === type) && (!playerName || item.playerName === playerName))
    return HttpResponse.json({ items, total: items.length, page: 1, pageSize: 100 })
  }),

  domainRoute('get', '/client-dist/security/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([{ id: 1, subjectType: 'client', subjectValue: 'install-a', channelId: 'survival-s2', machineId: 'm-dddd', installId: 'install-a', playerName: 'Herobrine', ip: '203.0.113.20', keyId: 1, keyPrefix: 'jmck_ab12', ruleCode: 'INVALID_PLAYER_NAME', severity: 'warn', scoreDelta: 1, action: 'observe', reason: '玩家名异常', createdAt: '2026-06-28T10:08:00Z' }])
  }),

  domainRoute('get', '/client-dist/security/profiles', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(runtimeStates.list().map((s) => ({ id: s.id, channelId: s.channelId, machineId: s.machineId, installId: `install-${s.id}`, playerName: s.playerName, keyId: 1, keyPrefix: 'jmck_ab12', firstSeen: s.firstSeenAt, lastSeen: s.lastHeartbeatAt, lastIp: s.ip, userAgent: 'JM-Updater/1.0', coreVersion: s.coreVersion, wedgeVersion: '1', manifestVersion: s.localVersion, os: s.platform, osVersion: '', arch: 'amd64', javaVendor: 'Temurin', javaVersion: s.javaVersion, javaArch: 'amd64', launcher: s.launcher, locale: 'zh-CN', timezone: 'Asia/Shanghai', memoryTier: '4-8g', riskScore: 0, riskLevel: 'info', protectionState: 'normal', labels: [], createdAt: s.createdAt, updatedAt: s.updatedAt })))
  }),

  // 画像详情（FR-358）：列表行点开后拉全量字段 + 时间线
  domainRoute('get', '/client-dist/security/profiles/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const s = runtimeStates.find((row) => row.id === id)
    if (!s) return HttpResponse.json({ error: 'PROFILE_NOT_FOUND' }, { status: 404 })
    return HttpResponse.json({
      id: s.id,
      channelId: s.channelId,
      machineId: s.machineId,
      installId: `install-${s.id}`,
      playerName: s.playerName,
      keyId: 1,
      keyPrefix: 'jmck_ab12',
      firstSeen: s.firstSeenAt,
      lastSeen: s.lastHeartbeatAt,
      lastIp: s.ip,
      userAgent: 'JM-Updater/1.0',
      coreVersion: s.coreVersion,
      wedgeVersion: '1',
      manifestVersion: s.localVersion,
      os: s.platform,
      osVersion: '',
      arch: 'amd64',
      javaVendor: 'Temurin',
      javaVersion: s.javaVersion,
      javaArch: 'amd64',
      launcher: s.launcher,
      locale: 'zh-CN',
      timezone: 'Asia/Shanghai',
      memoryTier: '4-8g',
      riskScore: 0,
      riskLevel: 'info',
      protectionState: 'normal',
      labels: [],
      createdAt: s.createdAt,
      updatedAt: s.updatedAt,
      recentEvents: [],
      protectionActions: [],
    })
  }),

  domainRoute('get', '/client-channels/:id/security-summary', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.id)
    return HttpResponse.json({
      channelId,
      riskLevel: 'info',
      abnormalRequests: 0,
      blockedIpCount: 0,
      restrictedKeyCount: 0,
      protectionMode: '',
      windowMinutes: 60,
    })
  }),

  domainRoute('get', '/client-dist/security/ip-analysis', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(rankBy(distEvents.list(), 'ip').map((r) => ({ ip: r.subject, requestCount: r.count, rejectCount: r.subject === '203.0.113.20' ? 1 : 0, invalidKeyCount: 0, notFoundCount: 0, rangeCount: 1, downloadBytes: r.bytes ?? 0, keyCount: 1, channelCount: 1, riskScore: 1, blocked: r.subject === '203.0.113.20', lastSeen: '2026-06-28T10:09:00Z' })))
  }),

  domainRoute('get', '/client-dist/security/player-analysis', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(runtimeStates.list().map((s) => ({ playerName: s.playerName, installCount: 1, machineCount: 1, ipCount: 1, keyCount: 1, channelCount: 1, downloadBytes: 2048, abnormalRequests: s.playerName === 'Herobrine' ? 1 : 0, riskScore: s.playerName === 'Herobrine' ? 1 : 0, lastSeen: s.lastHeartbeatAt })))
  }),

  domainRoute('get', '/client-dist/security/actions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([{ id: 1, targetType: 'ip', targetValue: '203.0.113.20', channelId: '', action: 'temp_block', status: 'active', reason: '异常拉取', auto: false, expiresAt: '2026-06-28T11:09:00Z', createdBy: 1, createdAt: '2026-06-28T10:09:00Z', updatedAt: '2026-06-28T10:09:00Z' }])
  }),

  domainRoute('get', '/client-dist/security/groups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([{ id: 1, name: '高风险 IP', kind: 'manual', targetType: 'ip', enabled: true, createdBy: 1, createdAt: '2026-06-28T10:00:00Z', updatedAt: '2026-06-28T10:00:00Z' }])
  }),

  // 运行态启动心跳（FR-265）：mock 接收后 upsert 运行态，避免端点 404。
  domainRoute('post', '/client-channels/:channelId/telemetry/heartbeat', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json().catch(() => ({}))) as Partial<MockRuntimeState>
    const machineId = info.request.headers.get('X-Machine-Id') ?? 'mock-machine'
    const playerName = info.request.headers.get('X-Player-Name') ?? body.playerName ?? ''
    const row = runtimeStates.find((s) => s.channelId === channelId && s.machineId === machineId)
    const now = new Date().toISOString()
    if (row) {
      runtimeStates.update(row.id, {
        playerName: playerName || row.playerName,
        platform: body.platform ?? row.platform,
        javaVersion: body.javaVersion ?? row.javaVersion,
        launcher: body.launcher ?? row.launcher,
        coreVersion: body.coreVersion ?? row.coreVersion,
        localVersion: body.localVersion ?? row.localVersion,
        lastHeartbeatAt: now,
        updatedAt: now,
      })
    } else {
      runtimeStates.insert({
        id: Date.now(),
        channelId,
        machineId,
        playerName,
        ip: '127.0.0.1',
        platform: body.platform ?? 'unknown',
        javaVersion: body.javaVersion ?? '',
        launcher: body.launcher ?? 'unknown',
        coreVersion: body.coreVersion ?? 'unknown',
        localVersion: body.localVersion ?? 0,
        firstSeenAt: now,
        lastHeartbeatAt: now,
        createdAt: now,
        updatedAt: now,
      })
    }
    return new HttpResponse(null, { status: 202 })
  }),

  // 分发请求近实时聚合（FR-265）：只看 client-dist-events mock 集合。
  domainRoute('get', '/client-dist/realtime', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    return HttpResponse.json(buildRealtime(url.searchParams.get('channelId') ?? ''))
  }),

  // 错误码 TopN 与失败样例（FR-357）。
  domainRoute('get', '/client-dist/error-summary', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    return HttpResponse.json(buildErrorSummary(url.searchParams.get('channelId') ?? ''))
  }),

  // 客户端运行态聚合（FR-265）：运行态 + client_telemetry 更新结果 mock。
  domainRoute('get', '/client-dist/clients', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    return HttpResponse.json(buildRuntimeOverview(url.searchParams.get('channelId') ?? '', url.searchParams.get('range') ?? '7d'))
  }),

  // 分发明细分页检索（FR-265）：支持 outcome 与运行态维度联动过滤。
  domainRoute('get', '/client-dist/events/search', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const page = Number(url.searchParams.get('page') ?? 1)
    const pageSize = Number(url.searchParams.get('pageSize') ?? 100)
    const rows = searchDistEvents(url)
    return HttpResponse.json({ items: rows.slice((page - 1) * pageSize, page * pageSize), page, pageSize, total: rows.length })
  }),

  // 分发明细脱敏详情（FR-265）。
  domainRoute('get', '/client-dist/events/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = distEvents.find((e) => e.id === id)
    if (!row) return HttpResponse.json({ error: 'EVENT_NOT_FOUND' }, { status: 404 })
    return HttpResponse.json({ ...row, requestHeaders: row.requestHeaders ?? {}, responseHeaders: row.responseHeaders ?? {} })
  }),

  // 分发明细事件检索（FR-093/249 兼容旧端点）。
  domainRoute('get', '/client-dist/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const limit = Number(url.searchParams.get('limit') ?? 200)
    return HttpResponse.json(searchDistEvents(url).slice(0, limit))
  }),

  // 内嵌更新器 jar 信息（FR-107 接入引导）。
  domainRoute('get', '/client-dist/updater-jars', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      version: '0.9.0',
      coreVersion: '3',
      wedge: { available: true, size: 32_768 },
      core: { available: true, size: 1_048_576 },
    })
  }),

  // jm-updater.json 一键生成（FR-253，见 ADR-053；FR-259 起 core 改楔子自动拉取，去 signPublicKey/coreJar）。
  domainRoute('get', '/client-channels/:channelId/updater-config', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const key = url.searchParams.get('key')?.trim()
    if (!key) return HttpResponse.json({ error: 'CLIENT_KEY_REQUIRED', message: '生成 jm-updater.json 必须提供拉取密钥' }, { status: 400 })
    const base = url.origin
    const cid = String(info.params.channelId)
    return HttpResponse.json({
      channel: cid,
      key,
      endpoint: `${base}/api/v1`,
      timeoutSec: 120,
      telemetry: true,
      bootConfirmSec: 30,
    })
  }),

  // updater-core 归档版本列表（FR-259）。
  domainRoute('get', '/client-channels/:channelId/updater-core/versions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([
      {
        version: 2,
        coreVersion: '0.1.0-SNAPSHOT',
        displayVersion: '0.1.0-SNAPSHOT+abc123def456.dirty',
        gitCommit: 'abc123def456',
        dirty: true,
        buildTime: '2026-07-01T09:55:00Z',
        sha256: 'a'.repeat(64),
        size: 1_048_576,
        createdAt: '2026-07-01T10:00:00Z',
        selected: true,
      },
      { version: 1, sha256: 'b'.repeat(64), size: 1_024_000, createdAt: '2026-06-28T10:00:00Z', selected: false },
    ])
  }),

  // 手动上传 updater-core.jar（hotfix）。
  domainRoute('post', '/client-channels/:channelId/updater-core/versions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const form = await info.request.formData()
    const file = form.get('file')
    if (!(file instanceof File)) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '需上传 updater-core.jar' }, { status: 400 })
    const version = Number(form.get('version') || 3)
    const selected = form.get('select') === 'true'
    return HttpResponse.json({
      version,
      coreVersion: '0.1.1-hotfix',
      displayVersion: '0.1.1-hotfix+def456abc789',
      gitCommit: 'def456abc789',
      dirty: false,
      buildTime: new Date().toISOString(),
      sha256: 'c'.repeat(64),
      size: file.size || 1_048_576,
      createdAt: new Date().toISOString(),
      selected,
    })
  }),

  // 切换频道选定 updater-core 版本（FR-259）。
  domainRoute('put', '/client-channels/:channelId/updater-core/selected', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ ok: true })
  }),

  // 下载更新器 jar（FR-107）：返回二进制占位流。
  domainRoute('get', '/client-dist/updater-jars/:component', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return new HttpResponse(new Blob([new Uint8Array([0x50, 0x4b])]), {
      headers: { 'Content-Type': 'application/java-archive' },
    })
  }),

  // ── 平台设置（FR-063 / ADR-015）────────────────────────────────────────────
  // 读取平台配置全量视图（仅平台管理员；mock 仅按 requireAuth 放行）。
  domainRoute('get', '/settings', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const s = db<SettingsRow>('settings').list()
    return HttpResponse.json({
      editable: s.filter((it) => it.editable).map(rowToItem),
      readOnly: s.filter((it) => !it.editable).map(rowToItem),
    })
  }),

  // 写入平台配置覆盖：把提交的键覆盖到 settings 集合，回填最新视图。
  domainRoute('put', '/settings', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { values: Record<string, string> }
    const coll = db<SettingsRow>('settings')
    for (const [key, value] of Object.entries(body.values)) {
      const row = coll.find((r) => r.key === key)
      if (row) coll.update(row.id, { value, overridden: true })
    }
    const s = coll.list()
    return HttpResponse.json({
      editable: s.filter((it) => it.editable).map(rowToItem),
      readOnly: s.filter((it) => !it.editable).map(rowToItem),
    })
  }),

  // ── 开源许可清单（FR-135）────────────────────────────────────────────────
  // 静态资源端点（非 /api/v1，前端用原生 fetch）：用裸 http.get 注册到 licenses.json。
  http.get('*/licenses.json', () =>
    HttpResponse.json({
      generatedAt: '2026-06-28T00:00:00Z',
      dependencies: [
        {
          name: 'react',
          version: '19.0.0',
          license: 'MIT',
          author: 'Meta',
          url: 'https://react.dev',
          scope: 'web',
          ecosystem: 'npm',
          type: 'runtime',
          licenseText: 'MIT License — react',
        },
        {
          name: 'vitest',
          version: '3.0.0',
          license: 'MIT',
          author: 'Anthony Fu',
          url: 'https://vitest.dev',
          scope: 'web',
          ecosystem: 'npm',
          type: 'dev',
          licenseText: '',
        },
        {
          name: 'mineflayer',
          version: '4.37.1',
          license: 'MIT',
          author: 'PrismarineJS',
          url: 'https://github.com/PrismarineJS/mineflayer',
          scope: 'bot-worker',
          ecosystem: 'npm',
          type: 'runtime',
          licenseText: 'MIT License — mineflayer',
        },
        {
          name: 'github.com/gin-gonic/gin',
          version: 'v1.10.0',
          license: 'MIT',
          author: 'Gin-Gonic',
          url: 'https://github.com/gin-gonic/gin',
          scope: 'go',
          ecosystem: 'go',
          type: 'runtime',
          licenseText: 'MIT License — gin',
        },
        {
          name: 'com.github.luben:zstd-jni',
          version: '1.5.6-4',
          license: 'BSD-2-Clause',
          author: 'Luben Karavelov',
          url: 'https://github.com/luben/zstd-jni',
          scope: 'client-updater',
          ecosystem: 'maven',
          type: 'runtime',
          licenseText: '',
        },
        {
          name: 'org.ow2.asm:asm',
          version: '9.7.1',
          license: 'BSD-3-Clause',
          author: 'OW2',
          url: 'https://asm.ow2.io',
          scope: 'serverprobe',
          ecosystem: 'maven',
          type: 'runtime',
          licenseText: '',
        },
      ],
    }),
  ),
]

// ── settings 集合声明 ────────────────────────────────────────────────────────

/** 假后端平台配置项（匹配 SettingItem，外加 id 供集合寻址）。 */
interface SettingsRow {
  id: number
  key: string
  value: string
  editable: boolean
  sensitive: boolean
  overridden: boolean
  effectiveImmediately: boolean
}

/** 剥离 id，序列化为 SettingItem。 */
function rowToItem(r: SettingsRow) {
  return {
    key: r.key,
    value: r.value,
    editable: r.editable,
    sensitive: r.sensitive,
    overridden: r.overridden,
    effectiveImmediately: r.effectiveImmediately,
  }
}

db<SettingsRow>('settings', () => [
  { id: 1, key: 'log.level', value: 'info', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  {
    id: 2,
    key: 'graceful_stop.timeout',
    value: '30s',
    editable: true,
    sensitive: false,
    overridden: false,
    effectiveImmediately: false,
  },
  { id: 3, key: 'jdk.mirror', value: 'https://mirror.example.com', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  { id: 4, key: 'database.dsn', value: 'sqlite://****', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
  { id: 5, key: 'jwt.secret', value: '****', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
  { id: 6, key: 'debug.mode', value: 'false', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
])
