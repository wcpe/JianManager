import { HttpResponse, delay } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth, requirePlatformAdmin } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'

/**
 * 节点与运行时域 mock handler（FR-200，照 spec §7 范式）。
 * 覆盖 api 模块：nodes / nodeRuntime / nodeRepair / jdks / runtimeAssets / selfUpdate 的每个 endpoint。
 * 字段严格匹配各 `web/src/api/*.ts` 的 interface；写操作改 db 联动（如装 JDK→列表出现、删 JDK→overview 减少、
 * 升级节点→check 当前版本变更）。受保护端点首行 requireAuth。集合 nodes/node-jdks/enroll-tokens/assets
 * 各在本模块顶层带 seedFn 唯一声明。
 */

/** 假后端节点（字段同 web/src/api/nodes.ts NodeInfo）。 */
interface MockNode {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number
  maintenance: boolean
  /** 反向隧道已连（FR-281，见 ADR-066）：在线节点 true=隧道下发，false=直拨回退。 */
  tunnelConnected: boolean
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  diskTotalMb: number
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  networkBytesSent: number
  networkBytesRecv: number
  loadAvg1: number
  lastHeartbeat: string | null
  createdAt: string
  /** Worker 二进制当前版本（自更新对比用，非 NodeInfo 字段、不外泄到 /nodes）。 */
  workerVersion: string
  /** 升级前备份版本（FR-182），空=无备份。 */
  backupVersion?: string
}

/** 假后端 JDK（字段同 web/src/api/jdks.ts NodeJDK）。 */
interface MockJDK {
  id: number
  nodeId: number
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

/** 假后端 enrollment token 元数据（字段同 web/src/api/nodes.ts EnrollTokenInfo）。 */
interface MockEnrollToken {
  id: number
  tokenPrefix: string
  nodeName: string
  expiresAt: string
  used: boolean
  usedAt: string | null
  usedByNode: string
  revoked: boolean
  createdAt: string
}

/** 假后端制品（字段同 web/src/api/runtimeAssets.ts AssetInfo）。 */
interface MockAsset {
  id: number
  type: 'core' | 'plugin' | 'image' | 'video' | 'archive' | 'blob' | 'client-file'
  name: string
  version: string
  filename: string
  sha256: string
  md5: string
  size: number
  contentType: string
  sourceUrl: string
  metadata: string
  storageState: 'hot' | 'archived' | 'external'
  storageBackend: string
  refCount: number
  relPath: string
  createdAt: string
  lastUsedAt: string | null
}

/** 假后端 Worker 二进制缓存条目（字段同 web/src/api/selfUpdate.ts WorkerAssetCacheEntry）。 */
interface MockWorkerAsset {
  id: string
  version: string
  os: string
  arch: string
  cached: boolean
  sha256: string
  size: number
  sourceUrl: string
  cachedAt: string
  lastError: string
}

/** runtime-assets 聚合只需要实例的 JDK 绑定子集，避免跨域 mock 强耦合。 */
interface MockRuntimeInstanceRef {
  id: number
  uuid: string
  nodeId: number
  name: string
  status: string
  jdkId?: number
  javaMajorVersion?: number
}

const NOW = '2026-06-28T08:00:00Z'
/** 更新源 mock 最新版本。 */
const FEED_LATEST = '0.10.0'

/** 从 multipart 请求提取制品导入所需字段（FR-155）。 */
interface MultipartAssetFields {
  type?: string
  name?: string
  version?: string
  filename?: string
  contentType?: string
  size?: number
}

/**
 * 稳健读取 multipart/form-data 字段（FR-155 导入制品）。
 * 优先走标准 request.formData()；在测试环境下 axios 编码的真实 File 会触发 undici multipart
 * 解析器断言失败（jsdom/undici 已知不兼容），此时回退按原始文本正则提取各段，保证 mock 仍可用。
 */
async function readMultipartFields(request: Request): Promise<MultipartAssetFields> {
  try {
    const form = await request.clone().formData()
    const file = form.get('file')
    const fileLike = file as { name?: unknown; size?: unknown; type?: unknown } | null
    return {
      type: strOrUndef(form.get('type')),
      name: strOrUndef(form.get('name')),
      version: strOrUndef(form.get('version')),
      filename: typeof file === 'string' ? file : typeof fileLike?.name === 'string' ? fileLike.name : undefined,
      contentType: typeof fileLike?.type === 'string' ? fileLike.type : undefined,
      size: typeof fileLike?.size === 'number' ? fileLike.size : undefined,
    }
  } catch {
    // 回退：从原始 multipart 文本按 name 抓取各段（含文件段的 filename / 正文长度）。
    const raw = await request.text()
    const field = (n: string) => {
      const m = raw.match(new RegExp(`name="${n}"\\r?\\n\\r?\\n([\\s\\S]*?)\\r?\\n--`, 'i'))
      return m ? m[1] : undefined
    }
    const fileMatch = raw.match(/name="file"; filename="([^"]*)"[\s\S]*?\r?\n\r?\n([\s\S]*?)\r?\n--/i)
    return {
      type: field('type'),
      name: field('name'),
      version: field('version'),
      filename: fileMatch?.[1],
      size: fileMatch ? fileMatch[2].length : undefined,
    }
  }
}

/** FormDataEntryValue → 非空字符串或 undefined。 */
function strOrUndef(v: FormDataEntryValue | null): string | undefined {
  return typeof v === 'string' && v !== '' ? v : undefined
}

/** 生成从当前时间起算的接入 token 过期时间，避免 mock seed 随日期推进变成过期样例。 */
function enrollTokenExpiresAt(ttlMinutes = 60) {
  return new Date(Date.now() + ttlMinutes * 60_000).toISOString()
}

const nodes = db<MockNode>('nodes', () => [
  {
    id: 1,
    uuid: 'node-alpha',
    name: 'alpha',
    host: '10.0.0.11',
    grpcPort: 9100,
    wsPort: 9200,
    status: 1,
    maintenance: false,
    tunnelConnected: true,
    os: 'linux',
    arch: 'amd64',
    cpuCores: 8,
    memoryMb: 32768,
    diskTotalMb: 512000,
    cpuUsage: 0.32,
    memoryUsage: 0.55,
    diskUsage: 0.4,
    networkBytesSent: 1048576,
    networkBytesRecv: 2097152,
    loadAvg1: 1.6,
    lastHeartbeat: NOW,
    createdAt: NOW,
    workerVersion: '0.9.0',
    backupVersion: '0.8.0',
  },
  {
    id: 2,
    uuid: 'node-beta',
    name: 'beta',
    host: '10.0.0.12',
    grpcPort: 9100,
    wsPort: 9200,
    status: 0,
    maintenance: false,
    tunnelConnected: false,
    os: 'windows',
    arch: 'amd64',
    cpuCores: 4,
    memoryMb: 16384,
    diskTotalMb: 256000,
    cpuUsage: 0,
    memoryUsage: 0,
    diskUsage: 0,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    loadAvg1: 0,
    lastHeartbeat: null,
    createdAt: NOW,
    workerVersion: '0.10.0',
  },
])

const jdks = db<MockJDK>('node-jdks', () => [
  {
    id: 1,
    nodeId: 1,
    vendor: 'temurin',
    majorVersion: 21,
    version: '21.0.3+9',
    arch: 'amd64',
    path: '/opt/jdks/temurin-21',
    managed: true,
    createdAt: NOW,
  },
  {
    id: 2,
    nodeId: 1,
    vendor: 'temurin',
    majorVersion: 17,
    version: '17.0.11+9',
    arch: 'amd64',
    path: '/opt/jdks/temurin-17',
    managed: true,
    createdAt: NOW,
  },
])

/** 假后端非 JDK 运行时（FR-298，字段同 web/src/api/runtimes.ts NodeRuntimeItem 去 type=jdk）。 */
interface MockNodeRuntime {
  id: number
  nodeId: number
  type: string
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

const nodeRuntimes = db<MockNodeRuntime>('node-runtimes', () => [])

const enrollTokens = db<MockEnrollToken>('enroll-tokens', () => [
  {
    id: 1,
    tokenPrefix: 'jmet_AbCd',
    nodeName: 'gamma',
    expiresAt: enrollTokenExpiresAt(),
    used: false,
    usedAt: null,
    usedByNode: '',
    revoked: false,
    createdAt: NOW,
  },
])

const assets = db<MockAsset>('assets', () => [
  {
    id: 1,
    type: 'core',
    name: 'paper-1.20.4',
    version: '1.20.4-496',
    filename: 'paper-1.20.4-496.jar',
    sha256: 'a'.repeat(64),
    md5: 'b'.repeat(32),
    size: 48234123,
    contentType: 'application/java-archive',
    sourceUrl: 'https://papermc.io/downloads',
    metadata: '{}',
    storageState: 'hot',
    storageBackend: 'local',
    refCount: 1,
    relPath: 'core/paper-1.20.4-496.jar',
    createdAt: NOW,
    lastUsedAt: NOW,
  },
  {
    id: 2,
    type: 'plugin',
    name: 'ViaVersion',
    version: '5.0.1',
    filename: 'ViaVersion-5.0.1.jar',
    sha256: 'c'.repeat(64),
    md5: 'd'.repeat(32),
    size: 3211264,
    contentType: 'application/java-archive',
    sourceUrl: '',
    metadata: '{}',
    storageState: 'archived',
    storageBackend: 'local',
    refCount: 0,
    relPath: 'plugin/ViaVersion-5.0.1.jar',
    createdAt: NOW,
    lastUsedAt: null,
  },
  {
    id: 3,
    type: 'client-file',
    name: 'lobby-client-config',
    version: '2026.06.28',
    filename: 'servers.json.zst',
    sha256: 'e'.repeat(64),
    md5: 'f'.repeat(32),
    size: 2048,
    contentType: 'application/octet-stream',
    sourceUrl: '',
    metadata: '{"path":"config/servers.json","codec":"zstd"}',
    storageState: 'hot',
    storageBackend: 'local',
    refCount: 1,
    relPath: 'client-file/servers.json.zst',
    createdAt: NOW,
    lastUsedAt: NOW,
  },
])

const workerAssets = db<MockWorkerAsset>('worker-assets', () => [
  {
    id: 'linux/amd64',
    version: '0.9.0',
    os: 'linux',
    arch: 'amd64',
    cached: true,
    sha256: '0123456789abcdef'.repeat(4),
    size: 1024 * 1024,
    sourceUrl: 'https://github.com/wxys233/jianmanager/releases/download/v0.10.0/worker-linux-amd64',
    cachedAt: '2026-07-06T05:00:00Z',
    lastError: '',
  },
  {
    id: 'windows/amd64',
    version: '0.9.0',
    os: 'windows',
    arch: 'amd64',
    cached: false,
    sha256: '',
    size: 0,
    sourceUrl: 'https://github.com/wxys233/jianmanager/releases/download/v0.10.0/worker-windows-amd64.exe',
    cachedAt: '',
    lastError: 'sha256 校验失败',
  },
])

/** 一个 NodeInfo 视图（剔除 mock 内部字段 workerVersion/backupVersion）。 */
function toNodeInfo(n: MockNode) {
  const { workerVersion: _w, backupVersion: _b, ...info } = n
  void _w
  void _b
  return info
}

function workerAssetId(os: string, arch: string) {
  return `${os}/${arch}`
}

function toWorkerAssetInfo(a: MockWorkerAsset) {
  const { id: _id, ...info } = a
  void _id
  return info
}

function cacheWorkerAsset(os: string, arch: string) {
  const id = workerAssetId(os, arch)
  const patch = {
    version: cp.currentVersion,
    os,
    arch,
    cached: true,
    sha256: 'fedcba9876543210'.repeat(4),
    size: 2 * 1024 * 1024,
    sourceUrl: `https://github.com/wxys233/jianmanager/releases/download/v0.10.0/worker-${os}-${arch}`,
    cachedAt: '2026-07-06T06:00:00Z',
    lastError: '',
  }
  return workerAssets.update(id, patch) ?? workerAssets.insert({ id, ...patch })
}

/** CP（Control Plane）自身 mock 版本状态，升级/回滚后变更，check 即反映（FR-081）。 */
const cp = { currentVersion: '0.9.0', backupVersion: '0.8.0' as string | undefined }

/** 全网升级编排进度快照（FR-081 + FR-155 金丝雀分批），upgrade-all 触发后填充。 */
interface MockRollout {
  rolloutId: string
  targetVersion: string
  state: string
  startedAt: string
  finishedAt: string | null
  total: number
  succeeded: number
  failed: number
  pending: number
  nodes: { nodeId: number; name: string; state: string; fromVersion: string; toVersion: string; error: string; attempts: number }[]
  phase: string
  canarySize: number
  batchSize: number
  currentBatch: number
}
let rollout: MockRollout = {
  rolloutId: '',
  targetVersion: '',
  state: 'idle',
  startedAt: '',
  finishedAt: null,
  total: 0,
  succeeded: 0,
  failed: 0,
  pending: 0,
  nodes: [],
  phase: '',
  canarySize: 0,
  batchSize: 0,
  currentBatch: 0,
}

/** 升级一个版本号的小工具：currentVersion→FEED_LATEST 时算可升级。 */
function componentStatusForNode(n: MockNode) {
  const updateAvailable = n.workerVersion.replace(/^v/, '') !== FEED_LATEST.replace(/^v/, '')
  return {
    nodeId: n.id,
    nodeUuid: n.uuid,
    name: n.name,
    online: n.status === 1,
    currentVersion: n.workerVersion,
    os: n.os,
    arch: n.arch,
    updateAvailable: updateAvailable && n.status === 1,
    artifactAvailable: true,
    backupVersion: n.backupVersion,
  }
}

export const handlers = [
  /* ===================== 连通性测试（FR-229） ===================== */
  domainRoute('post', '/diagnostics/http-test', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { url } = (await info.request.json()) as { url?: string }
    if (!url || !/^https?:\/\/[^/]/.test(url)) {
      return HttpResponse.json({ error: 'INVALID_URL', message: '仅支持 http/https URL' }, { status: 400 })
    }
    // mock 不真正联网：恒返可达 200，演示按钮成功态。
    return HttpResponse.json({ ok: true, status: 200, latencyMs: 42 })
  }),

  domainRoute('post', '/nodes/:id/ping', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    // mock：按 db 节点 status（1=在线）返存活/离线，联动节点状态。
    return n.status === 1
      ? HttpResponse.json({ alive: true, latencyMs: 12, version: '0.12.0', os: 'linux', arch: 'amd64' })
      : HttpResponse.json({ alive: false, latencyMs: 0, error: '节点未连接' })
  }),

  domainRoute('post', '/nodes/:id/docker/check', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    return n.status === 1
      ? HttpResponse.json({ available: true, version: '29.4.1' })
      : HttpResponse.json({ error: 'NODE_OFFLINE', message: '节点未连接' }, { status: 503 })
  }),

  /* ===================== nodes（FR-048 / FR-080 / FR-185） ===================== */
  domainRoute('get', '/nodes', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(nodes.list().map(toNodeInfo))
  }),

  domainRoute('get', '/nodes/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    return HttpResponse.json(toNodeInfo(n))
  }),

  domainRoute('post', '/nodes/:id/maintenance', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const { enabled } = (await info.request.json()) as { enabled: boolean }
    const n = nodes.update(id, { maintenance: enabled })
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    return HttpResponse.json(toNodeInfo(n))
  }),

  domainRoute('post', '/nodes/:id/drain', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    return HttpResponse.json({ stoppedCount: 0, stopped: [], failed: [] })
  }),

  domainRoute('delete', '/nodes/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    nodes.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('post', '/nodes/enroll-token', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as { nodeName?: string; ttlMinutes?: number }
    const name = body.nodeName || 'new-node'
    const ttlMinutes = body.ttlMinutes ?? 60
    const created = enrollTokens.insert({
      tokenPrefix: `jmet_${Math.random().toString(36).slice(2, 6)}`,
      nodeName: name,
      expiresAt: enrollTokenExpiresAt(ttlMinutes),
      used: false,
      usedAt: null,
      usedByNode: '',
      revoked: false,
      createdAt: NOW,
    })
    const token = `${created.tokenPrefix}_secret`
    const workerDownload = `https://cp.example.com/worker-assets/${FEED_LATEST}/{os}/{arch}/worker?token=mock-worker-install`
    return HttpResponse.json({
      token,
      tokenId: created.id,
      tokenPrefix: created.tokenPrefix,
      expiresAt: created.expiresAt,
      nodeName: name,
      controlPlaneGrpc: 'cp.example.com:9100',
      scriptBaseUrl: 'https://cp.example.com',
      installCommandLinux: `curl -fsSL https://cp.example.com/install-worker.sh | sh -s -- --control-plane cp.example.com:9100 --token ${token}${name ? ` --name ${name}` : ''} --download-url '${workerDownload}'`,
      installCommandWindows: `iex (iwr https://cp.example.com/install-worker.ps1 -UseBasicParsing).Content; Install-JianManagerWorker -ControlPlane cp.example.com:9100 -Token ${token}${name ? ` -Name ${name}` : ''} -DownloadUrl '${workerDownload}'`,
    })
  }),

  domainRoute('get', '/nodes/enroll-tokens', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(enrollTokens.list())
  }),

  domainRoute('delete', '/nodes/enroll-tokens/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    enrollTokens.update(id, { revoked: true })
    return new HttpResponse(null, { status: 204 })
  }),

  /* ===================== node proxy（FR-185） ===================== */
  domainRoute('get', '/nodes/:id/proxy', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    return HttpResponse.json({
      mode: 'inherit',
      url: '',
      noProxy: '',
      effectiveUrl: 'http://proxy.internal:7890',
      effectiveNoProxy: 'localhost,127.0.0.1',
      globalDefaultUrl: 'http://proxy.internal:7890',
      online: n.status === 1,
    })
  }),

  domainRoute('patch', '/nodes/:id/proxy', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    const body = (await info.request.json()) as { mode: 'inherit' | 'custom'; url?: string; noProxy?: string }
    const custom = body.mode === 'custom'
    return HttpResponse.json({
      mode: body.mode,
      url: custom ? body.url ?? '' : '',
      noProxy: custom ? body.noProxy ?? '' : '',
      effectiveUrl: custom ? body.url ?? '' : 'http://proxy.internal:7890',
      effectiveNoProxy: custom ? body.noProxy ?? '' : 'localhost,127.0.0.1',
      globalDefaultUrl: 'http://proxy.internal:7890',
      online: n.status === 1,
    })
  }),

  /* ===================== node repair（BUG-A / ADR-039） ===================== */
  domainRoute('get', '/nodes/repair/suspects', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([])
  }),

  domainRoute('get', '/nodes/:id/orphans', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    return HttpResponse.json({ nodeId: id, jdkCount: 0, instanceCount: 0 })
  }),

  domainRoute('post', '/nodes/:id/reenroll', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    const oldUuid = n.uuid
    const newUuid = `node-reenrolled-${id}`
    nodes.update(id, { uuid: newUuid })
    return HttpResponse.json({ nodeId: id, newUuid, newSecret: 'jm_secret_rotated', oldUuid })
  }),

  domainRoute('post', '/nodes/:id/purge-orphans', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    return HttpResponse.json({ nodeId: id, jdkDeleted: 0, instancesPurged: 0 })
  }),

  /* ===================== JDK 托管（FR-033 / FR-072 / FR-178 / FR-183） ===================== */
  domainRoute('get', '/nodes/:id/jdks', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    return HttpResponse.json(jdks.list((j) => j.nodeId === nodeId))
  }),

  domainRoute('post', '/nodes/:id/jdks', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as Omit<MockJDK, 'id' | 'nodeId' | 'createdAt'>
    const created = jdks.insert({ ...body, nodeId, createdAt: NOW })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/nodes/:id/jdks/:jid', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const jid = Number((info.params as { jid: string }).jid)
    const body = (await info.request.json()) as Partial<MockJDK>
    const updated = jdks.update(jid, body)
    if (!updated) return HttpResponse.json({ error: 'NOT_FOUND', message: 'JDK 不存在' }, { status: 404 })
    return HttpResponse.json(updated)
  }),

  domainRoute('delete', '/nodes/:id/jdks/:jid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const jid = Number((info.params as { jid: string }).jid)
    if (!jdks.get(jid)) return HttpResponse.json({ error: 'NOT_FOUND', message: 'JDK 不存在' }, { status: 404 })
    jdks.remove(jid)
    return new HttpResponse(null, { status: 204 })
  }),

  // 一键装 JDK（FR-183 异步化）：受理即装入列表（mock 同步落库以便列表联动），回执 taskId（HTTP 202）。
  domainRoute('post', '/nodes/:id/jdks/install', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as { vendor: string; majorVersion: number; arch: string; version?: string }
    jdks.insert({
      nodeId,
      vendor: body.vendor,
      majorVersion: body.majorVersion,
      version: body.version || `${body.majorVersion}.0.0`,
      arch: body.arch,
      path: `/opt/jdks/${body.vendor}-${body.majorVersion}`,
      managed: true,
      createdAt: NOW,
    })
    return HttpResponse.json({ taskId: `task-jdk-${Date.now()}` }, { status: 202 })
  }),

  // JDK 探测（FR-228）：mock 据路径返厂商/版本/架构；含 "invalid" 或不含 jdk/java 关键字 → valid=false 演示错误态。
  domainRoute('post', '/nodes/:id/jdks/probe', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { path } = (await info.request.json()) as { path?: string }
    const p = (path ?? '').toLowerCase()
    const looksLikeJdk = p.includes('jdk') || p.includes('java') || p.includes('temurin') || p.includes('corretto') || p.includes('zulu')
    if (!p || p.includes('invalid') || !looksLikeJdk) {
      return HttpResponse.json({ valid: false, vendor: '', majorVersion: 0, version: '', arch: '', javaHome: path ?? '', error: '未找到 bin/java 或无法读取版本' })
    }
    return HttpResponse.json({ valid: true, vendor: 'Temurin', majorVersion: 21, version: '21.0.4+9', arch: 'x64', javaHome: path })
  }),

  domainRoute('get', '/nodes/:id/jdk/catalog', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json([
      { distribution: 'temurin', majorVersion: 21, javaVersion: '21.0.3+9', archiveType: 'tar.gz', latest: true },
      { distribution: 'temurin', majorVersion: 21, javaVersion: '21.0.2+13', archiveType: 'tar.gz', latest: false },
    ])
  }),

  domainRoute('get', '/nodes/:id/browse', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const path = url.searchParams.get('path') ?? ''
    return HttpResponse.json({
      path,
      parent: path ? '/opt' : '',
      dirs: [
        { name: 'jdks', path: '/opt/jdks' },
        { name: 'instances', path: '/opt/instances' },
      ],
    })
  }),

  /* ===================== 节点运行时库（FR-298） ===================== */
  // 统一 Runtime 视图：node-jdks(type=jdk) + node-runtimes 读侧拼装。
  domainRoute('get', '/nodes/:id/runtimes', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    const jdkRows = jdks.list((j) => j.nodeId === nodeId).map((j) => ({
      id: j.id,
      nodeId: j.nodeId,
      type: 'jdk',
      name: j.vendor,
      majorVersion: j.majorVersion,
      version: j.version,
      arch: j.arch,
      path: j.path,
      managed: j.managed,
      createdAt: j.createdAt,
    }))
    return HttpResponse.json([...jdkRows, ...nodeRuntimes.list((r) => r.nodeId === nodeId)])
  }),

  // 扫描发现：固定两条候选（jdk 同 seed 路径→已在库；nodejs 未登记时可勾选入库，入库后重扫标已在库）。
  domainRoute('post', '/nodes/:id/runtimes/scan', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    await delay(50)
    const jdkPath = '/opt/jdks/temurin-21'
    const nodePath = '/usr/local/bin/node'
    return HttpResponse.json({
      candidates: [
        {
          type: 'jdk', vendor: 'Temurin', version: '21.0.3+9', majorVersion: 21, arch: 'x64', path: jdkPath,
          alreadyRegistered: jdks.list((j) => j.nodeId === nodeId && j.path === jdkPath).length > 0,
        },
        {
          type: 'nodejs', vendor: 'Node.js', version: '22.17.0', majorVersion: 22, arch: 'x64', path: nodePath,
          alreadyRegistered: nodeRuntimes.list((r) => r.nodeId === nodeId && r.path === nodePath).length > 0,
        },
      ],
    })
  }),

  // 泛化登记：type=jdk 落 node-jdks 走现链路语义；其它落 node-runtimes（同 node+type+path 重复 422）。
  domainRoute('post', '/nodes/:id/runtimes', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as {
      type: string; name?: string; vendor?: string; majorVersion: number; version: string; arch?: string; path: string; managed?: boolean
    }
    if (!['jdk', 'nodejs', 'python'].includes(body.type)) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: `不支持的运行时类型: ${body.type}` }, { status: 422 })
    }
    if (body.type === 'jdk') {
      const created = jdks.insert({
        nodeId,
        vendor: body.vendor || body.name || 'Unknown',
        majorVersion: body.majorVersion,
        version: body.version,
        arch: body.arch ?? '',
        path: body.path,
        managed: body.managed ?? false,
        createdAt: NOW,
      })
      return HttpResponse.json({
        id: created.id, nodeId, type: 'jdk', name: created.vendor, majorVersion: created.majorVersion,
        version: created.version, arch: created.arch, path: created.path, managed: created.managed, createdAt: NOW,
      }, { status: 201 })
    }
    if (nodeRuntimes.list((r) => r.nodeId === nodeId && r.type === body.type && r.path === body.path).length > 0) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '该路径已登记同类型运行时' }, { status: 422 })
    }
    const created = nodeRuntimes.insert({
      nodeId,
      type: body.type,
      name: body.name || `${body.type === 'nodejs' ? 'Node.js' : 'Python'} ${body.majorVersion}`,
      majorVersion: body.majorVersion,
      version: body.version,
      arch: body.arch ?? '',
      path: body.path,
      managed: body.managed ?? false,
      createdAt: NOW,
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  // 一键安装 Node.js（FR-299）：202+taskId 受理；mock 同步落一条托管运行时行，模拟任务终态落库。
  domainRoute('post', '/nodes/:id/runtimes/install', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as { type: string; major: number; arch?: string }
    if (body.type !== 'nodejs') {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: `不支持的运行时类型: ${body.type}` }, { status: 422 })
    }
    if (!Number.isInteger(body.major) || body.major <= 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '请求参数错误' }, { status: 400 })
    }
    const version = `${body.major}.0.0`
    const path = `/data/opt/runtimes/nodejs-${body.major}/node-v${version}-linux-x64/bin/node`
    if (nodeRuntimes.list((r) => r.nodeId === nodeId && r.type === 'nodejs' && r.path === path).length === 0) {
      nodeRuntimes.insert({
        nodeId,
        type: 'nodejs',
        name: `Node.js ${body.major}`,
        majorVersion: body.major,
        version,
        arch: body.arch || 'x64',
        path,
        managed: true,
        createdAt: NOW,
      })
    }
    const taskId = `task-runtime-install-${body.major}`
    return HttpResponse.json({ taskId, task: { taskId, kind: 'runtime_install', state: 'running' } }, { status: 202 })
  }),

  // 删除：type 定位承载表（jdk → node-jdks，其它 → node-runtimes；托管 nodejs 语义=连文件，mock 仅删记录）。
  domainRoute('delete', '/nodes/:id/runtimes/:rid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const rid = Number((info.params as { rid: string }).rid)
    const type = new URL(info.request.url).searchParams.get('type')
    if (!type) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '缺少 type 查询参数' }, { status: 400 })
    if (type === 'jdk') {
      if (!jdks.get(rid)) return HttpResponse.json({ error: 'NOT_FOUND', message: '运行时不存在' }, { status: 404 })
      jdks.remove(rid)
    } else {
      if (!nodeRuntimes.get(rid)) return HttpResponse.json({ error: 'NOT_FOUND', message: '运行时不存在' }, { status: 404 })
      nodeRuntimes.remove(rid)
    }
    return HttpResponse.json({ message: '已删除' })
  }),

  /* ===================== 节点制品缓存（FR-178） ===================== */
  domainRoute('get', '/nodes/:id/artifact-cache', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      items: [
        {
          sha256: 'a'.repeat(64),
          name: 'paper-1.20.4',
          type: 'core',
          version: '1.20.4-496',
          size: 48234123,
          cachedAt: 1719561600,
          lastUsedAt: 1719565200,
        },
      ],
      totalBytes: 48234123,
      capBytes: 0,
    })
  }),

  domainRoute('delete', '/nodes/:id/artifact-cache/:sha256', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('post', '/nodes/:id/artifact-cache/clear', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('put', '/nodes/:id/artifact-cache/cap', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { capBytes } = (await info.request.json()) as { capBytes: number }
    return HttpResponse.json({ items: [], totalBytes: 0, capBytes })
  }),

  /* ===================== 运行时与制品全局聚合（FR-082） ===================== */
  domainRoute('get', '/runtime-assets/overview', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const nodeRows = nodes.list()
    const jdkRows = jdks.list()
    const instanceRows = db<MockRuntimeInstanceRef>('instances').list()
    const jdkMatrix = jdkRows.map((j) => {
      const node = nodeRows.find((n) => n.id === j.nodeId)
      return {
        id: j.id,
        nodeId: j.nodeId,
        nodeName: node?.name ?? `#${j.nodeId}`,
        nodeOnline: node?.status === 1,
        vendor: j.vendor,
        majorVersion: j.majorVersion,
        version: j.version,
        arch: j.arch,
        path: j.path,
        managed: j.managed,
        instances: [] as { id: number; uuid: string; name: string; status: string; binding: 'direct' | 'major' }[],
        refCount: 0,
      }
    })
    const jdkById = new Map(jdkMatrix.map((j) => [j.id, j]))
    const jdkByNodeMajor = new Map(jdkMatrix.map((j) => [`${j.nodeId}:${j.majorVersion}`, j]))
    for (const inst of instanceRows) {
      const direct = inst.jdkId ? jdkById.get(inst.jdkId) : undefined
      const resolved = direct ?? jdkByNodeMajor.get(`${inst.nodeId}:${inst.javaMajorVersion ?? 0}`)
      if (!resolved) continue
      resolved.instances.push({
        id: inst.id,
        uuid: inst.uuid,
        name: inst.name,
        status: inst.status,
        binding: direct ? 'direct' : 'major',
      })
      resolved.refCount += 1
    }
    const assetRows = assets.list()
    const types: MockAsset['type'][] = ['core', 'plugin', 'image', 'video', 'archive', 'blob', 'client-file']
    const assetGroups = types
      .map((type) => {
        const items = assetRows.filter((a) => a.type === type)
        return {
          type,
          items,
          count: items.length,
          totalSize: items.reduce((s, a) => s + a.size, 0),
          referencedCount: items.filter((a) => a.refCount > 0).length,
          hotCount: items.filter((a) => a.storageState === 'hot').length,
          archivedCount: items.filter((a) => a.storageState === 'archived').length,
          externalCount: items.filter((a) => a.storageState === 'external').length,
        }
      })
      .filter((g) => g.count > 0)
    return HttpResponse.json({
      jdks: jdkMatrix,
      jdkSummary: {
        nodeCount: new Set(jdkRows.map((j) => j.nodeId)).size,
        jdkCount: jdkRows.length,
        referencedJdk: jdkMatrix.filter((j) => j.refCount > 0).length,
        instanceRefs: jdkMatrix.reduce((sum, j) => sum + j.refCount, 0),
      },
      assets: assetGroups,
      assetSummary: {
        assetCount: assetRows.length,
        totalSize: assetRows.reduce((s, a) => s + a.size, 0),
        referencedCount: assetRows.filter((a) => a.refCount > 0).length,
        hotCount: assetRows.filter((a) => a.storageState === 'hot').length,
        archivedCount: assetRows.filter((a) => a.storageState === 'archived').length,
        externalCount: assetRows.filter((a) => a.storageState === 'external').length,
      },
    })
  }),

  // 导入制品（FR-155：制品导入下载进度）——镜像后端 multipart 入库（POST /assets）。
  // 读上传文件 → 按类型插入一条制品，令 overview 联动出现（进度由前端 axios onUploadProgress 驱动）。
  domainRoute('post', '/assets', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const fields = await readMultipartFields(info.request)
    const type = String(fields.type || '') as MockAsset['type']
    const validTypes: MockAsset['type'][] = ['core', 'plugin', 'image', 'video', 'archive', 'blob', 'client-file']
    if (!validTypes.includes(type)) {
      return HttpResponse.json({ error: 'INVALID_TYPE', message: '非法的资产类型' }, { status: 400 })
    }
    if (!fields.filename) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '需提供上传文件' }, { status: 400 })
    }
    // 轻微延迟：模拟上传耗时，让前端进度反馈有可观测窗口（导入进度是本 endpoint 的核心体验）。
    await delay(30)
    const filename = fields.filename
    const created = assets.insert({
      type,
      name: fields.name || filename.replace(/\.[^.]+$/, ''),
      version: fields.version || '',
      filename,
      sha256: 'imported'.padEnd(64, '0'),
      md5: 'imported'.padEnd(32, '0'),
      size: fields.size || 1024,
      contentType: fields.contentType || 'application/octet-stream',
      sourceUrl: '',
      metadata: '{}',
      storageState: 'hot',
      storageBackend: 'local',
      refCount: 0,
      relPath: `${type}/${filename}`,
      createdAt: NOW,
      lastUsedAt: NOW,
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('delete', '/assets/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const a = assets.get(id)
    if (!a) return HttpResponse.json({ error: 'NOT_FOUND', message: '制品不存在' }, { status: 404 })
    if (a.refCount > 0) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '制品被引用，无法删除', count: a.refCount }, { status: 409 })
    }
    assets.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  /* ===================== 面板自更新（FR-081 / FR-182 / FR-186） ===================== */
  domainRoute('get', '/self-update/check', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(buildCheckResult(true))
  }),

  domainRoute('post', '/self-update/check/refresh', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(buildCheckResult(false))
  }),

  domainRoute('get', '/self-update/worker-assets', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    return HttpResponse.json(workerAssets.list().map(toWorkerAssetInfo))
  }),

  domainRoute('post', '/self-update/worker-assets/cache', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as { os?: string; arch?: string }
    if (!body.os || !body.arch) {
      return HttpResponse.json({ error: 'INVALID_ARGUMENT', message: '平台参数不完整' }, { status: 400 })
    }
    return HttpResponse.json(toWorkerAssetInfo(cacheWorkerAsset(body.os, body.arch)))
  }),

  domainRoute('post', '/self-update/control-plane/upgrade', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as { version?: string }
    const from = cp.currentVersion
    const to = body.version || FEED_LATEST
    cp.backupVersion = from
    cp.currentVersion = to
    return HttpResponse.json({ status: 'accepted', fromVersion: from, toVersion: to }, { status: 202 })
  }),

  domainRoute('post', '/self-update/control-plane/rollback', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const from = cp.currentVersion
    const to = cp.backupVersion || from
    cp.currentVersion = to
    return HttpResponse.json({ status: 'accepted', fromVersion: from, toVersion: to }, { status: 202 })
  }),

  domainRoute('post', '/self-update/nodes/:id/upgrade', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    const body = (await info.request.json().catch(() => ({}))) as { version?: string }
    const from = n.workerVersion
    const to = body.version || FEED_LATEST
    nodes.update(id, { workerVersion: to, backupVersion: from })
    return HttpResponse.json({ status: 'accepted', nodeId: id, fromVersion: from, toVersion: to }, { status: 202 })
  }),

  domainRoute('post', '/self-update/nodes/:id/rollback', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = nodes.get(id)
    if (!n) return HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 })
    const from = n.workerVersion
    const to = n.backupVersion || from
    nodes.update(id, { workerVersion: to })
    return HttpResponse.json({ status: 'accepted', nodeId: id, fromVersion: from, toVersion: to }, { status: 202 })
  }),

  domainRoute('post', '/self-update/nodes/upgrade-all', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as {
      nodeIds?: number[]
      version?: string
      canarySize?: number
      batchSize?: number
      abortOnCanaryFailure?: boolean
    }
    const to = body.version || FEED_LATEST
    const targets = nodes
      .list((n) => n.status === 1)
      .filter((n) => !body.nodeIds || body.nodeIds.includes(n.id))
    // FR-155：金丝雀=第 1 批；剩余按 batchSize 分批（<=0=剩余一批）。mock 恒成功，故末态 phase=completed。
    const canarySize = Math.min(Math.max(0, body.canarySize ?? 0), targets.length)
    const batchSize = body.batchSize && body.batchSize > 0 ? body.batchSize : Math.max(1, targets.length - canarySize)
    const remaining = targets.length - canarySize
    const remainingBatches = remaining > 0 ? Math.ceil(remaining / batchSize) : 0
    const currentBatch = canarySize > 0 ? 1 + remainingBatches : Math.max(1, remainingBatches)
    rollout = {
      rolloutId: `rollout-${Date.now()}`,
      targetVersion: to,
      state: 'completed',
      startedAt: NOW,
      finishedAt: NOW,
      total: targets.length,
      succeeded: targets.length,
      failed: 0,
      pending: 0,
      nodes: targets.map((n) => {
        const from = n.workerVersion
        nodes.update(n.id, { workerVersion: to, backupVersion: from })
        return { nodeId: n.id, name: n.name, state: 'succeeded', fromVersion: from, toVersion: to, error: '', attempts: 1 }
      }),
      phase: 'completed',
      canarySize,
      batchSize: body.batchSize ?? 0,
      currentBatch,
    }
    return HttpResponse.json(rollout, { status: 202 })
  }),

  domainRoute('get', '/self-update/rollout', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(rollout)
  }),
]

/** 构建 check / refresh 响应（FR-081 / FR-186）。cached 区分缓存读取与 live 刷新。 */
function buildCheckResult(cached: boolean) {
  return {
    configured: true,
    latestVersion: FEED_LATEST,
    notes: '## 0.10.0\n- mock 更新说明',
    source: 'github:wxys233/jianmanager@stable',
    controlPlane: {
      online: true,
      currentVersion: cp.currentVersion,
      os: 'linux',
      arch: 'amd64',
      updateAvailable: cp.currentVersion.replace(/^v/, '') !== FEED_LATEST.replace(/^v/, ''),
      artifactAvailable: true,
      backupVersion: cp.backupVersion,
    },
    nodes: nodes.list().map(componentStatusForNode),
    cached,
    checkedAt: NOW,
  }
}
