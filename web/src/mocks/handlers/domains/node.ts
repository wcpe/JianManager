import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
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

const NOW = '2026-06-28T08:00:00Z'

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

const enrollTokens = db<MockEnrollToken>('enroll-tokens', () => [
  {
    id: 1,
    tokenPrefix: 'jm_enr_AbCd',
    nodeName: 'gamma',
    expiresAt: '2026-06-28T09:00:00Z',
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
])

/** 一个 NodeInfo 视图（剔除 mock 内部字段 workerVersion/backupVersion）。 */
function toNodeInfo(n: MockNode) {
  const { workerVersion: _w, backupVersion: _b, ...info } = n
  void _w
  void _b
  return info
}

/** CP（Control Plane）自身 mock 版本状态，升级/回滚后变更，check 即反映（FR-081）。 */
const cp = { currentVersion: '0.9.0', backupVersion: '0.8.0' as string | undefined }
/** 更新源 mock 最新版本。 */
const FEED_LATEST = '0.10.0'

/** 全网升级编排进度快照（FR-081），upgrade-all 触发后填充。 */
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
    const created = enrollTokens.insert({
      tokenPrefix: `jm_enr_${Math.random().toString(36).slice(2, 6)}`,
      nodeName: name,
      expiresAt: '2026-06-28T09:00:00Z',
      used: false,
      usedAt: null,
      usedByNode: '',
      revoked: false,
      createdAt: NOW,
    })
    return HttpResponse.json({
      token: `jm_enr_${created.tokenPrefix}_secret`,
      tokenId: created.id,
      tokenPrefix: created.tokenPrefix,
      expiresAt: created.expiresAt,
      nodeName: name,
      controlPlaneGrpc: 'cp.example.com:9100',
      scriptBaseUrl: 'https://cp.example.com',
      installCommandLinux: `curl -fsSL https://cp.example.com/install.sh | bash -s -- --token jm_enr_${created.tokenPrefix}_secret`,
      installCommandWindows: `iwr https://cp.example.com/install.ps1 | iex`,
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
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeRows = nodes.list()
    const jdkRows = jdks.list()
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
        referencedJdk: 0,
        instanceRefs: 0,
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
    const body = (await info.request.json().catch(() => ({}))) as { nodeIds?: number[]; version?: string }
    const to = body.version || FEED_LATEST
    const targets = nodes
      .list((n) => n.status === 1)
      .filter((n) => !body.nodeIds || body.nodeIds.includes(n.id))
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
