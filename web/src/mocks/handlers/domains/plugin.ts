import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { requireAuth } from '@/mocks/auth-middleware'

/**
 * 插件 / 玩家 / 经济 / 业务 / 探针域 mock handler（FR-206 域簇，照 spec §7 范式）。
 *
 * 覆盖端点：
 *   - 插件（@/api/plugins.ts）：列表 / 上传 / 删除 / 启用禁用切换
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

// ======================== 集合声明（唯一带 seedFn 处） ========================

const plugins = db<PluginRow>('plugins', () => [
  { id: 1, instanceId: 1, name: 'EssentialsX.jar', dir: 'plugins', enabled: true, size: 1_048_576, modTime: 1_710_000_000 },
  { id: 2, instanceId: 1, name: 'WorldEdit.jar', dir: 'plugins', enabled: true, size: 2_097_152, modTime: 1_710_100_000 },
  { id: 3, instanceId: 1, name: 'OldMod.jar', dir: 'mods', enabled: false, size: 524_288, modTime: 1_709_000_000 },
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
    },
  }
}

// ======================== Handlers ========================

export const handlers = [
  // ---------- 插件 / 模组 ----------

  // 列出某实例 plugins/ 与 mods/ 插件（按 instanceId 过滤）。
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
    const dir = (form.get('dir') as string) || 'plugins'
    const name =
      file instanceof File && file.name ? file.name : `uploaded-${plugins.list().length + 1}.jar`
    const row = plugins.insert({
      instanceId: id,
      name,
      dir,
      enabled: true,
      size: file instanceof File ? file.size : 0,
      modTime: Math.floor(Date.now() / 1000),
    })
    return HttpResponse.json({ name: row.name, dir: row.dir, deployed: true })
  }),

  // 删除插件：从列表移除（写操作联动）。name 在 URL，dir 在 query。
  domainRoute('delete', '/instances/:id/plugins/:name', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const name = decodeURIComponent(String(info.params.name))
    const dir = new URL(info.request.url).searchParams.get('dir') || 'plugins'
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
    const dir = new URL(info.request.url).searchParams.get('dir') || 'plugins'
    const target = plugins.find((p) => p.instanceId === id && p.name === name && p.dir === dir)
    if (!target) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '插件不存在' }, { status: 404 })
    }
    plugins.update(target.id, { enabled: !target.enabled })
    return HttpResponse.json({ enabled: !target.enabled })
  }),

  // ---------- 探针在线更新 ----------

  domainRoute('get', '/instances/:id/probe/update', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    return HttpResponse.json({
      instanceId: id,
      instanceUuid: `inst-${id}`,
      probeConnected: true,
      embeddedVersion: '0.1.0',
      embeddedFingerprint: 'abc123',
      embeddedAvailable: true,
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
      embeddedVersion: '0.1.0',
      embeddedFingerprint: 'abc123',
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
    let payload: Record<string, string> = {}
    try {
      payload = JSON.parse(body.payload || '{}')
    } catch {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'payload 非法 JSON' }, { status: 400 })
    }

    let output: unknown = { ok: true, action: body.action }

    if (body.domain === 'economy' && body.action === 'balance') {
      const row = economyMirror.find(
        (r) => r.playerName === payload.player && r.currency === payload.currency,
      )
      output = { player: payload.player, currency: payload.currency, balance: row?.balance ?? '0' }
    } else if (body.write && body.domain === 'economy') {
      // 写动作联动：deposit 加额、withdraw 扣额（按字符串数值简化处理，mock 不追求 BigDecimal 精度）。
      const target = body.action === 'transfer' ? payload.to : payload.player
      const row = economyMirror.find((r) => r.playerName === target && r.currency === payload.currency)
      if (row) {
        const delta = body.action === 'withdraw' ? -Number(payload.amount) : Number(payload.amount)
        const next = (Number(row.balance) + delta).toFixed(2)
        economyMirror.update(row.id, { balance: next })
        output = { player: target, currency: payload.currency, balance: next }
      } else {
        output = { player: target, currency: payload.currency, applied: true }
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
