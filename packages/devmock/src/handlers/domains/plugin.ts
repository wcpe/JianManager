import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { requireAuth } from '@jianmanager/devmock/auth-middleware'

/**
 * 插件 / 玩家 / 经济 / 业务 / 探针域 mock handler（FR-206 域簇，照 spec §7 范式）。
 *
 * 覆盖端点：
 *   - 插件（@/api/plugins.ts）：列表 / 上传 / 删除 / 启用禁用切换 / 制品批量部署
 *   - 探针更新（@/api/probe.ts）：状态查询 / 推送
 *   - 玩家（@/api/players.ts）：在线列表 / 踢封解封 / 封禁记录 / 单后端白名单查改
 *   - 经济（@/api/economy.ts）：余额镜像 / 排行 / 业务事件流（流水）
 *   - 业务（@/api/business.ts）：能力清单 manifest / 业务命令下发
 *
 * 字段严格匹配各 api 模块的 interface（payloadJson 等 JSON 字符串字段返回字符串，见全局记忆
 * 「JSON 字符串字段前端解析」）。受 requireAuth 保护——这些端点后端均要求 instance.* 权限。
 */

// ======================== 集合实体类型 ========================

/** 假后端插件行（匹配 service.PluginInfo / @/api/plugins.ts PluginInfo）。带 instanceId 以隔离不同实例。 */
interface PluginRow {
  id: number
  instanceId: number
  name: string
  dir: string
  enabled: boolean
  size: number
  modTime: number
  version?: string
  author?: string
  dependencies?: string[]
}

/** 批量部署只需要制品库资产的插件子集字段。 */
interface PluginAssetRow {
  id: number
  type: string
  name: string
  filename: string
  size: number
}

/** 批量部署只需要实例列表的可见目标字段。 */
interface PluginTargetInstance {
  id: number
  nodeId: number
  name: string
  role: string
  status: string
}

interface PluginBatchDeployMockRequest {
  assetIds?: number[]
  ids?: number[]
  filter?: Partial<Pick<PluginTargetInstance, 'nodeId' | 'role' | 'status'>> & { q?: string }
  target?: {
    ids?: number[]
    filter?: Partial<Pick<PluginTargetInstance, 'nodeId' | 'role' | 'status'>> & { q?: string }
  }
  destination?: string
  overwrite?: boolean
}

/** 封禁记录行（匹配 @/api/players.ts BanRecord）。 */
interface BanRow {
  id: number
  uuid: string
  playerName: string
  reason: string
  scope: 'network' | 'instance' | 'global'
  scopeId: number
  operatorId: number
  active: boolean
  createdAt: string
  unbannedAt?: string | null
  operator?: { id: number; username: string }
}

/** 单后端白名单行：每实例一条，players 为名单。 */
interface WhitelistRow {
  id: number
  instanceId: number
  players: string[]
}

/** 经济余额镜像行（匹配 @/api/economy.ts EconomyMirrorRow）。 */
interface EconomyMirrorEntity {
  id: number
  nodeUuid: string
  zoneId: string
  playerName: string
  currency: string
  currencyId: number
  balance: string
  lastSeq: number
  lastLedgerId: number
  lastEntryType: string
  occurredAt: number
  updatedAt: string
}

/** 通用业务事件行（匹配 @/api/economy.ts BusinessEvent）；经济流水从 payloadJson 解析。 */
interface BusinessEventEntity {
  id: number
  domain: string
  dedupKey: string
  action: string
  nodeUuid: string
  instanceUuid: string
  operator?: string
  payloadJson: string
  occurredAt: number
  createdAt: string
}

interface InventorySnapshotEntity {
  id: number
  instanceId: number
  player: string
  exists: boolean
  online: boolean
  dataVersion: number
  inventory: Array<Record<string, unknown>>
  enderChest: Array<Record<string, unknown>>
  basicAttrs: Record<string, unknown>
}

// ======================== 集合声明（唯一带 seedFn 处） ========================

const plugins = db<PluginRow>('plugins', () => [
  { id: 1, instanceId: 1, name: 'EssentialsX.jar', dir: 'plugins', enabled: true, size: 1_048_576, modTime: 1_710_000_000, version: '2.21.0', author: 'EssentialsX' },
  { id: 2, instanceId: 1, name: 'WorldEdit.jar', dir: 'plugins', enabled: true, size: 2_097_152, modTime: 1_710_100_000, version: '7.3.0', author: 'EngineHub' },
  { id: 3, instanceId: 1, name: 'OldMod.jar', dir: 'mods', enabled: false, size: 524_288, modTime: 1_709_000_000, version: '1.0.0', dependencies: ['fabric-api'] },
  { id: 4, instanceId: 1, name: 'HighResPack.zip', dir: 'resourcepacks', enabled: true, size: 12_582_912, modTime: 1_710_200_000, version: '1.20.4', author: 'Jian' },
  { id: 5, instanceId: 1, name: 'SpawnTweaks.zip', dir: 'datapacks', enabled: true, size: 262_144, modTime: 1_710_300_000, version: '3', author: 'ops' },
])

const bans = db<BanRow>('bans', () => [
  {
    id: 1,
    uuid: 'b-griefer',
    playerName: 'Griefer',
    reason: '破坏建筑',
    scope: 'global',
    scopeId: 0,
    operatorId: 1,
    active: true,
    createdAt: '2026-06-20T10:00:00Z',
    unbannedAt: null,
    operator: { id: 1, username: 'admin' },
  },
  {
    id: 2,
    uuid: 'b-spammer',
    playerName: 'Spammer',
    reason: '刷屏',
    scope: 'instance',
    scopeId: 1,
    operatorId: 1,
    active: false,
    createdAt: '2026-06-18T08:00:00Z',
    unbannedAt: '2026-06-19T08:00:00Z',
    operator: { id: 1, username: 'admin' },
  },
])

const whitelists = db<WhitelistRow>('whitelists', () => [
  { id: 1, instanceId: 1, players: ['Alice', 'Bob'] },
])

const economyMirror = db<EconomyMirrorEntity>('economyMirror', () => [
  {
    id: 1,
    nodeUuid: 'node-a',
    zoneId: '0',
    playerName: 'Steve',
    currency: 'coin',
    currencyId: 1,
    balance: '1000.00',
    lastSeq: 3,
    lastLedgerId: 7,
    lastEntryType: 'DEPOSIT',
    occurredAt: 1_719_000_000_000,
    updatedAt: '2026-06-22T10:00:00Z',
  },
  {
    id: 2,
    nodeUuid: 'node-a',
    zoneId: '0',
    playerName: 'Alex',
    currency: 'coin',
    currencyId: 1,
    balance: '250.50',
    lastSeq: 1,
    lastLedgerId: 4,
    lastEntryType: 'DEPOSIT',
    occurredAt: 1_719_000_100_000,
    updatedAt: '2026-06-22T10:05:00Z',
  },
])

const businessEvents = db<BusinessEventEntity>('businessEvents', () => [
  {
    id: 1,
    domain: 'economy',
    dedupKey: 'ledger-1001',
    action: 'DEPOSIT',
    nodeUuid: 'node-a',
    instanceUuid: 'inst-1',
    operator: 'admin',
    payloadJson: JSON.stringify({
      type: 'event',
      event: 'economy.change',
      domain: 'economy',
      dedupKey: 'ledger-1001',
      data: {
        playerName: 'Steve',
        currency: 'coin',
        zoneId: '0',
        entryType: 'DEPOSIT',
        signedAmount: '100.00',
        balanceAfter: '1000.00',
        ledgerId: 'ledger-1001',
        occurredAt: '1719000000000',
      },
    }),
    occurredAt: 1_719_000_000_000,
    createdAt: '2026-06-22T10:00:00Z',
  },
])

const inventorySnapshots = db<InventorySnapshotEntity>('inventorySnapshots', () => [
  {
    id: 1,
    instanceId: 1,
    player: 'Steve',
    exists: true,
    online: false,
    dataVersion: 42,
    inventory: [
      { slot: 0, material: 'DIAMOND_SWORD', amount: 1, displayName: '锋利之刃', nbtBase64: 'mock-sword' },
      { slot: 8, material: 'COOKED_BEEF', amount: 32, nbtBase64: 'mock-beef' },
      { slot: 12, material: 'TORCH', amount: 64, nbtBase64: 'mock-torch' },
    ],
    enderChest: [
      { slot: 0, material: 'ENDER_PEARL', amount: 16, nbtBase64: 'mock-pearl' },
    ],
    basicAttrs: { health: 20, foodLevel: 18, xpLevel: 30, xpProgress: 0.5, xpTotal: 1395, gameMode: 'SURVIVAL' },
  },
])

// ======================== 辅助：业务能力清单 ========================

/** 默认业务能力清单：经济域含只读 balance / leaderboard + 写 transfer / deposit / withdraw（贴合 BusinessSegment 渲染）。 */
function defaultManifest() {
  return {
    domains: {
      economy: {
        actions: [
          { action: 'balance', args: ['player', 'currency'], readOnly: true },
          { action: 'leaderboard', args: ['currency'], readOnly: true },
          { action: 'transfer', args: ['from', 'to', 'currency', 'amount'], readOnly: false },
          { action: 'deposit', args: ['player', 'currency', 'amount'], readOnly: false },
          { action: 'withdraw', args: ['player', 'currency', 'amount'], readOnly: false },
        ],
      },
      inventory: {
        actions: [
          { action: 'view', args: ['player'], readOnly: true },
          { action: 'writeBasicAttrs', args: ['player', 'base', 'edited'], readOnly: false },
        ],
      },
    },
  }
}

function selectBatchTargets(body: PluginBatchDeployMockRequest) {
  const rows = db<PluginTargetInstance>('instances').list()
  const ids = body.target?.ids
  if (ids && ids.length > 0) {
    return rows.filter((inst) => ids.includes(inst.id))
  }
  const filter = body.target?.filter
  if (!filter) return []
  return rows.filter((inst) => {
    if (filter.nodeId !== undefined && inst.nodeId !== Number(filter.nodeId)) return false
    if (filter.role && inst.role !== filter.role) return false
    if (filter.status && inst.status !== filter.status) return false
    if (filter.q && !inst.name.toLowerCase().includes(filter.q.toLowerCase())) return false
    return true
  })
}

function asText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function mockPluginAssetName(assetId: number): string {
  const names: Record<number, string> = {
    2: 'ViaVersion-5.0.1.jar',
  }
  return names[assetId] ?? `plugin-${assetId}.jar`
}

const MANAGED_PLUGIN_DIRS = ['plugins', 'mods', 'resourcepacks', 'datapacks'] as const

type ManagedPluginDir = (typeof MANAGED_PLUGIN_DIRS)[number]

const PLUGIN_DIR_EXT: Record<ManagedPluginDir, string> = {
  plugins: '.jar',
  mods: '.jar',
  resourcepacks: '.zip',
  datapacks: '.zip',
}

function isManagedPluginDir(value: string): value is ManagedPluginDir {
  return (MANAGED_PLUGIN_DIRS as readonly string[]).includes(value)
}

function normalizePluginDir(value: FormDataEntryValue | string | null): ManagedPluginDir {
  const dir = typeof value === 'string' ? value : ''
  return isManagedPluginDir(dir) ? dir : 'plugins'
}

function pluginUploadName(file: FormDataEntryValue | null, dir: ManagedPluginDir): string {
  if (typeof file === 'string' && file) return file
  const fileLike = file as { name?: unknown } | null
  if (typeof fileLike?.name === 'string' && fileLike.name) return fileLike.name
  return `uploaded-${plugins.list().length + 1}${PLUGIN_DIR_EXT[dir]}`
}

function pluginUploadSize(file: FormDataEntryValue | null): number {
  const fileLike = file as { size?: unknown } | null
  return typeof fileLike?.size === 'number' ? fileLike.size : 0
}

function isValidPluginUploadName(dir: ManagedPluginDir, name: string): boolean {
  return (
    name !== '' &&
    !name.includes('/') &&
    !name.includes('\\') &&
    !name.includes('..') &&
    !name.endsWith('.disabled') &&
    name.toLowerCase().endsWith(PLUGIN_DIR_EXT[dir])
  )
}

// ======================== Handlers ========================

export const handlers = [
  // ---------- 插件 / 模组 ----------

  // 列出某实例 plugins/mods/resourcepacks/datapacks 制品（按 instanceId 过滤）。
  domainRoute('get', '/instances/:id/plugins', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    return HttpResponse.json(plugins.list((p) => p.instanceId === id))
  }),

  // 上传插件：制品入库后部署到实例目标目录 → 列表新增一行（写操作联动）。
  domainRoute('post', '/instances/:id/plugins', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const form = await info.request.formData()
    const file = form.get('file')
    const dir = normalizePluginDir(form.get('dir'))
    const overwrite = form.get('overwrite') === 'true'
    const name = pluginUploadName(file, dir)
    const size = pluginUploadSize(file)
    if (!isValidPluginUploadName(dir, name)) {
      return HttpResponse.json({ error: 'INVALID_NAME', message: '非法的插件文件名' }, { status: 400 })
    }
    const existing = plugins.find((p) => p.instanceId === id && p.dir === dir && p.name === name)
    if (existing && !overwrite) {
      return HttpResponse.json({ error: 'FILE_EXISTS', message: '插件文件已存在' }, { status: 409 })
    }
    if (existing && overwrite) {
      plugins.update(existing.id, {
        enabled: true,
        size: size || existing.size,
        modTime: Math.floor(Date.now() / 1000),
      })
      return HttpResponse.json({ name, dir, deployed: true, overwritten: true }, { status: 201 })
    }
    const row = plugins.insert({
      instanceId: id,
      name,
      dir,
      enabled: true,
      size,
      modTime: Math.floor(Date.now() / 1000),
    })
    return HttpResponse.json({ name: row.name, dir: row.dir, deployed: true }, { status: 201 })
  }),

  // 删除插件：从列表移除（写操作联动）。name 在 URL，dir 在 query。
  domainRoute('delete', '/instances/:id/plugins/:name', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const name = decodeURIComponent(String(info.params.name))
    const dir = normalizePluginDir(new URL(info.request.url).searchParams.get('dir'))
    const target = plugins.find((p) => p.instanceId === id && p.name === name && p.dir === dir)
    if (target) plugins.remove(target.id)
    return HttpResponse.json({ deleted: true })
  }),

  // 启用 / 禁用切换：翻转 enabled（写操作联动，列表状态变化）。
  domainRoute('post', '/instances/:id/plugins/:name/toggle', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const name = decodeURIComponent(String(info.params.name))
    const dir = normalizePluginDir(new URL(info.request.url).searchParams.get('dir'))
    const target = plugins.find((p) => p.instanceId === id && p.name === name && p.dir === dir)
    if (!target) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '插件不存在' }, { status: 404 })
    }
    plugins.update(target.id, { enabled: !target.enabled })
    return HttpResponse.json({ enabled: !target.enabled })
  }),

  // 批量部署：从制品库选择插件资产，写入多个目标实例的 plugins/ 或 mods/。
  domainRoute('post', '/plugins/batch-deploy', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as PluginBatchDeployMockRequest
    const assetIds = body.assetIds ?? []
    const ids = body.target?.ids ?? []
    const filter = body.target?.filter
    if (assetIds.length === 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '请选择插件制品' }, { status: 400 })
    }
    if (ids.length > 0 && filter) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'ids 与 filter 只能指定一个' }, { status: 400 })
    }
    if (ids.length === 0 && !filter) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '请选择目标实例' }, { status: 400 })
    }

    const assets = db<PluginAssetRow>('assets')
    const selectedAssets = assetIds.map((id) => assets.get(id))
    if (selectedAssets.some((asset) => !asset)) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '插件制品不存在' }, { status: 404 })
    }
    if (selectedAssets.some((asset) => asset?.type !== 'plugin')) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '只能部署插件类型制品' }, { status: 400 })
    }

    const targets = selectBatchTargets(body)
    const skipped = Math.max(ids.length - targets.length, 0)
    if (targets.length === 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '没有可部署的目标实例' }, { status: 400 })
    }

    const dir = normalizePluginDir(body.destination ?? 'plugins')
    const results: Array<{ instanceId: number; assetId: number; ok: boolean; error?: string }> = []
    let success = 0
    let failed = 0
    for (const inst of targets) {
      for (const asset of selectedAssets) {
        if (!asset) continue
        const name = asset.filename || asset.name || mockPluginAssetName(asset.id)
        const existing = plugins.find((p) => p.instanceId === inst.id && p.dir === dir && p.name === name)
        if (existing && !body.overwrite) {
          failed += 1
          results.push({ instanceId: inst.id, assetId: asset.id, ok: false, error: `${name} 已存在` })
          continue
        }
        if (existing) {
          plugins.update(existing.id, { enabled: true, modTime: Math.floor(Date.now() / 1000) })
        } else {
          plugins.insert({
            instanceId: inst.id,
            name,
            dir,
            enabled: true,
            size: asset.size,
            modTime: Math.floor(Date.now() / 1000),
          })
        }
        success += 1
        results.push({ instanceId: inst.id, assetId: asset.id, ok: true })
      }
    }

    return HttpResponse.json({
      total: targets.length + skipped,
      success,
      failed,
      skipped,
      results,
      requestedInstances: targets.length + skipped,
      requestedAssets: assetIds.length,
      succeeded: success,
    })
  }),

  // ---------- 探针在线更新 ----------

  domainRoute('get', '/artifact-packages/serverprobe', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      package: { id: 1, key: 'serverprobe', name: 'ServerProbe', assetType: 'server-probe', defaultVersionId: 1 },
      sources: [{ id: 1, packageId: 1, provider: 'github-release', name: '官方 GitHub Releases', enabled: true, lastSyncedAt: '2026-08-24T00:00:00Z', lastError: '' }],
      versions: [{ id: 1, packageId: 1, sourceId: 1, version: '0.2.0', releaseRef: 'v0.2.0', assetName: 'ServerProbe-0.2.0.jar', expectedSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a', assetId: 1, cachedAt: '2026-08-24T00:00:00Z', lastError: '' }],
    })
  }),

  domainRoute('post', '/artifact-packages/serverprobe/sources/:id/sync', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ created: 0 })
  }),

  domainRoute('post', '/artifact-packages/serverprobe/versions/:id/cache', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ id: Number(info.params.id), packageId: 1, sourceId: 1, version: '0.2.0', releaseRef: 'v0.2.0', assetName: 'ServerProbe-0.2.0.jar', expectedSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a', assetId: 1, cachedAt: '2026-08-24T00:00:00Z', lastError: '' })
  }),

  domainRoute('put', '/artifact-packages/serverprobe/default-version', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as { versionId?: number }
    return HttpResponse.json({ defaultVersionId: body.versionId ?? 0 })
  }),

  domainRoute('get', '/nodes/:id/probe-version', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ nodeId: Number(info.params.id), versionId: 0 })
  }),

  domainRoute('put', '/nodes/:id/probe-version', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json().catch(() => ({}))) as { versionId?: number }
    return HttpResponse.json({ nodeId: Number(info.params.id), versionId: body.versionId ?? 0 })
  }),

  domainRoute('get', '/probe-versions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      package: { id: 1, key: 'serverprobe', name: 'ServerProbe', assetType: 'server-probe', defaultVersionId: 1 },
      versions: [{ id: 1, packageId: 1, sourceId: 1, version: '0.2.0', releaseRef: 'v0.2.0', assetName: 'ServerProbe-0.2.0.jar', expectedSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a', assetId: 1, cachedAt: '2026-08-24T00:00:00Z', lastError: '' }],
    })
  }),

  domainRoute('get', '/instances/:id/probe-version', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    return HttpResponse.json({
      instanceId: id,
      versionId: 0,
      resolvedVersion: { id: 1, packageId: 1, sourceId: 1, version: '0.2.0', releaseRef: 'v0.2.0', assetName: 'ServerProbe-0.2.0.jar', expectedSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a', assetId: 1, cachedAt: '2026-08-24T00:00:00Z', lastError: '' },
      origin: 'global',
    })
  }),

  domainRoute('put', '/instances/:id/probe-version', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json().catch(() => ({}))) as { versionId?: number }
    return HttpResponse.json({ instanceId: id, deployed: true, restarted: false, versionId: body.versionId ?? 0, version: '0.2.0', message: '探针 jar 已就位，下次重启生效' })
  }),

  domainRoute('get', '/instances/:id/probe/update', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    return HttpResponse.json({
      instanceId: id,
      instanceUuid: `inst-${id}`,
      probeConnected: true,
      versionId: 1,
      version: '0.2.0',
      versionSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a',
      versionOrigin: 'global',
      lastPushedAt: '2026-06-22T10:00:00Z',
    })
  }),

  domainRoute('post', '/instances/:id/probe/update', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json().catch(() => ({}))) as { restart?: boolean }
    return HttpResponse.json({
      instanceId: id,
      deployed: true,
      restarted: !!body.restart,
      probeConnected: true,
      versionId: 1,
      version: '0.2.0',
      message: body.restart ? '已推送并重启' : '已推送，下次重启生效',
    })
  }),

  // ---------- 玩家：在线 / 踢封解封 ----------

  // 在线玩家聚合（含后端探针可用性降级标注）。
  domainRoute('get', '/players', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      players: [
        { name: 'Alice', instanceId: 1, instanceName: 'lobby' },
        { name: 'Bob', instanceId: 2, instanceName: 'survival' },
      ],
      backends: [
        { instanceId: 1, instanceName: 'lobby', available: true },
        { instanceId: 2, instanceName: 'survival', available: true },
      ],
    })
  }),

  domainRoute('post', '/players/:name/kick', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const name = decodeURIComponent(String(info.params.name))
    return HttpResponse.json({
      player: name,
      action: 'kick',
      total: 1,
      succeeded: 1,
      failed: 0,
      results: [{ instanceId: 1, instanceName: 'lobby', ok: true, output: 'kicked' }],
    })
  }),

  // 封禁：写入封禁记录（写操作联动，bans 列表新增）。
  domainRoute('post', '/players/:name/ban', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const name = decodeURIComponent(String(info.params.name))
    const body = (await info.request.json().catch(() => ({}))) as { reason?: string }
    bans.insert({
      uuid: `b-${name}`,
      playerName: name,
      reason: body.reason || '',
      scope: 'global',
      scopeId: 0,
      operatorId: 1,
      active: true,
      createdAt: new Date().toISOString(),
      unbannedAt: null,
      operator: { id: 1, username: 'admin' },
    })
    return HttpResponse.json({
      player: name,
      action: 'ban',
      total: 1,
      succeeded: 1,
      failed: 0,
      results: [{ instanceId: 1, instanceName: 'lobby', ok: true, output: 'banned' }],
    })
  }),

  // 解封：把该玩家仍生效的封禁记录置为失效（写操作联动）。
  domainRoute('post', '/players/:name/unban', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const name = decodeURIComponent(String(info.params.name))
    for (const b of bans.list((x) => x.playerName === name && x.active)) {
      bans.update(b.id, { active: false, unbannedAt: new Date().toISOString() })
    }
    return HttpResponse.json({
      player: name,
      action: 'unban',
      total: 1,
      succeeded: 1,
      failed: 0,
      results: [{ instanceId: 1, instanceName: 'lobby', ok: true, output: 'pardoned' }],
    })
  }),

  // 封禁记录查询（player 模糊 / active 过滤）。
  domainRoute('get', '/bans', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const player = url.searchParams.get('player')
    const activeOnly = url.searchParams.get('active') === 'true'
    return HttpResponse.json(
      bans.list(
        (b) =>
          (!player || b.playerName.toLowerCase().includes(player.toLowerCase())) &&
          (!activeOnly || b.active),
      ),
    )
  }),

  // ---------- 玩家：白名单 ----------

  domainRoute('get', '/instances/:id/whitelist', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = whitelists.find((w) => w.instanceId === id)
    return HttpResponse.json({ instanceId: id, available: true, players: row?.players ?? [] })
  }),

  // 白名单增删：改 players 数组（写操作联动）。
  domainRoute('post', '/instances/:id/whitelist', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { action, player } = (await info.request.json()) as { action: 'add' | 'remove'; player: string }
    let row = whitelists.find((w) => w.instanceId === id)
    if (!row) row = whitelists.insert({ instanceId: id, players: [] })
    const next =
      action === 'add'
        ? Array.from(new Set([...row.players, player]))
        : row.players.filter((p) => p !== player)
    whitelists.update(row.id, { players: next })
    return HttpResponse.json({ instanceId: id, available: true, players: next })
  }),

  // ---------- 经济：镜像 / 排行 / 流水 ----------

  // 余额镜像（按 player / currency 过滤，逐 node→zone 行）。平台级路径（无实例前缀，见 @/api/economy.ts）。
  domainRoute('get', '/business/economy/mirror', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const player = url.searchParams.get('player')
    const currency = url.searchParams.get('currency')
    return HttpResponse.json({
      balances: economyMirror.list(
        (r) => (!player || r.playerName === player) && (!currency || r.currency === currency),
      ),
    })
  }),

  // 某货币余额倒序 Top-N（旁路排行，从镜像派生）。平台级路径（无实例前缀）。
  domainRoute('get', '/business/economy/leaderboard', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const currency = url.searchParams.get('currency') || ''
    const rows = economyMirror
      .list((r) => r.currency === currency)
      .sort((a, b) => Number(b.balance) - Number(a.balance))
      .map((r, idx) => ({
        rank: idx + 1,
        playerName: r.playerName,
        currency: r.currency,
        nodeUuid: r.nodeUuid,
        zoneId: r.zoneId,
        balance: r.balance,
      }))
    return HttpResponse.json({ currency, rows })
  }),

  // 通用业务事件流（domain=economy 过滤后供前端解析经济流水）。平台级路径（无实例前缀）。
  domainRoute('get', '/business/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const domain = url.searchParams.get('domain')
    return HttpResponse.json({
      events: businessEvents.list((e) => !domain || e.domain === domain),
    })
  }),

  // ---------- 业务：manifest / 下发 ----------

  // 业务能力清单（JBIS 元查询）。
  domainRoute('get', '/instances/:id/business/manifest', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    return HttpResponse.json({
      instanceId: id,
      domain: 'jbis',
      action: 'manifest',
      available: true,
      output: defaultManifest(),
      error: '',
    })
  }),

  // 业务命令下发：读动作回查询结果，写动作（transfer/deposit/withdraw）联动镜像余额并补一条流水。
  domainRoute('post', '/instances/:id/business', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json()) as {
      domain: string
      action: string
      payload: string
      write?: boolean
      reason?: string
    }
    let payload: Record<string, unknown>
    try {
      payload = JSON.parse(body.payload || '{}')
    } catch {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'payload 非法 JSON' }, { status: 400 })
    }

    let output: unknown = { ok: true, action: body.action }

    if (body.domain === 'inventory' && body.action === 'view') {
      const player = asText(payload.player)
      const row = inventorySnapshots.find((r) => r.instanceId === id && r.player === player)
      output = row
        ? {
            exists: row.exists,
            player: row.player,
            online: row.online,
            dataVersion: row.dataVersion,
            inventory: row.inventory,
            enderChest: row.enderChest,
            basicAttrs: row.basicAttrs,
          }
        : { exists: false, player, online: false, dataVersion: 0, inventory: [], enderChest: [], basicAttrs: null }
    } else if (body.write && body.domain === 'inventory' && body.action === 'writeBasicAttrs') {
      const player = asText(payload.player)
      const row = inventorySnapshots.find((r) => r.instanceId === id && r.player === player)
      const edited = payload.edited as { basicAttrs?: Record<string, unknown> } | undefined
      const basicAttrs = edited?.basicAttrs
      if (row && basicAttrs) {
        const nextVersion = row.dataVersion + 1
        inventorySnapshots.update(row.id, { basicAttrs, dataVersion: nextVersion })
        output = { success: true, online: row.online, newDataVersion: nextVersion, errorCode: '' }
      } else {
        output = { success: false, online: false, newDataVersion: 0, errorCode: 'PLAYER_NOT_FOUND' }
      }
    } else if (body.domain === 'economy' && body.action === 'balance') {
      const player = asText(payload.player)
      const currency = asText(payload.currency)
      const row = economyMirror.find(
        (r) => r.playerName === player && r.currency === currency,
      )
      output = { player, currency, balance: row?.balance ?? '0' }
    } else if (body.write && body.domain === 'economy') {
      // 写动作联动：deposit 加额、withdraw 扣额（按字符串数值简化处理，mock 不追求 BigDecimal 精度）。
      const target = body.action === 'transfer' ? asText(payload.to) : asText(payload.player)
      const currency = asText(payload.currency)
      const amount = asText(payload.amount)
      const row = economyMirror.find((r) => r.playerName === target && r.currency === currency)
      if (row) {
        const delta = body.action === 'withdraw' ? -Number(amount) : Number(amount)
        const next = (Number(row.balance) + delta).toFixed(2)
        economyMirror.update(row.id, { balance: next })
        output = { player: target, currency, balance: next }
      } else {
        output = { player: target, currency, applied: true }
      }
    }

    return HttpResponse.json({
      instanceId: id,
      domain: body.domain,
      action: body.action,
      available: true,
      output,
      error: '',
    })
  }),
]
