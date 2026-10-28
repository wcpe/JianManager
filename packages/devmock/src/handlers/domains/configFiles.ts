import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'

/**
 * 顶层「配置文件」域 mock handler（/config-files、/config-files/health、/config-files/adopt）。
 *
 * ## 与 `domains/config.ts` 的分工
 *
 * 那个文件覆盖的是**实例级**配置引擎（`/instances/:id/configs/*`，FR-031/FR-071）与
 * `/config-baselines`；本文件覆盖的是后来新增的**中央定义 + 按作用域分层**模型
 * （`web/src/api/configFiles.ts` 与 `configAdopt.ts`）。两者是不同资源，互不重叠。
 *
 * ## 为什么必须补
 *
 * 这四个顶层端点此前在 devmock 下**零实现**：`onUnhandledRequest: 'bypass'` 会把请求放行到
 * 代理目标（mock 模式下是刻意无监听的 `127.0.0.1:9`），于是页面只能看到骨架与「读取失败」。
 * 缺 mock 不等于前端有缺陷，但会让 e2e 永远覆盖不到新增页面。
 *
 * ## 字段保真
 *
 * 响应结构严格对齐 `configFiles.ts` / `configAdopt.ts` 的 TS interface；
 * 时间字段一律 RFC3339 字符串（后端 `time.Time` 的 JSON 形态），不要用 epoch 数字。
 */

/** 一个「配置文件」中央定义（对应 ConfigFileView）。 */
export interface MockConfigFileDefinition {
  id: number
  name: string
  format: 'yaml' | 'json' | 'properties'
  description: string
  schemaJson?: string
  sensitivePaths?: string
  createdBy: number
  createdAt: string
  updatedAt: string
  layerCount: number
}

/** 层作用域：分组级（任意分组树层级）或实例级。 */
export interface MockConfigLayer {
  id: number
  configFileId: number
  scopeType: 'group' | 'instance'
  scopeId: string
  versionNo: number
  content: string
  comment: string
  createdAt: string
}

/** RFC3339（后端 time.Time 的 JSON 形态）。 */
function rfc3339(offsetMinutes = 0): string {
  return new Date(Date.now() - offsetMinutes * 60_000).toISOString()
}

const SEED_CREATED = '2026-10-20T09:00:00.000Z'

/** 中央配置文件定义。刻意留一个 JSON、一个 properties，覆盖三种 format 的渲染分支。 */
const configFileDefinitions = db<MockConfigFileDefinition>('config-file-definitions', () => [
  {
    id: 1,
    name: 'server.properties',
    format: 'properties',
    description: 'Minecraft 服务端主配置（端口、难度、白名单开关）',
    createdBy: 1,
    createdAt: SEED_CREATED,
    updatedAt: rfc3339(30),
    layerCount: 2,
  },
  {
    id: 2,
    name: 'bukkit.yml',
    format: 'yaml',
    description: 'Bukkit 核心参数（区块加载、刷怪上限）',
    schemaJson: JSON.stringify({ type: 'object', properties: { 'chunk-gc.period-in-ticks': { type: 'integer' } } }),
    createdBy: 1,
    createdAt: SEED_CREATED,
    updatedAt: rfc3339(120),
    layerCount: 1,
  },
  {
    id: 3,
    name: 'plugins/AllinGame/config.json',
    format: 'json',
    description: 'AllinGame 玩法参数（JSON 嵌套，供表单模式渲染）',
    schemaJson: JSON.stringify({
      type: 'object',
      properties: { matchDurationSeconds: { type: 'integer', title: '对局时长（秒）' } },
    }),
    sensitivePaths: JSON.stringify(['database.password']),
    createdBy: 1,
    createdAt: SEED_CREATED,
    updatedAt: rfc3339(240),
    layerCount: 0,
  },
])

/** 作用域层版本（每个 scope 一条最新版本即可满足列表与 effective 计算）。 */
const configLayers = db<MockConfigLayer>('config-file-layers', () => [
  {
    id: 1,
    configFileId: 1,
    scopeType: 'group',
    scopeId: '1',
    versionNo: 1,
    content: 'difficulty=normal\nspawn-protection=16\n',
    comment: 'region1 基线',
    createdAt: rfc3339(300),
  },
  {
    id: 2,
    configFileId: 1,
    scopeType: 'instance',
    scopeId: '321',
    versionNo: 2,
    content: 'difficulty=hard\nspawn-protection=8\n',
    comment: 'game-01 覆写',
    createdAt: rfc3339(30),
  },
  {
    id: 3,
    configFileId: 2,
    scopeType: 'group',
    scopeId: '1',
    versionNo: 1,
    content: 'chunk-gc:\n  period-in-ticks: 600\n',
    comment: 'region1 基线',
    createdAt: rfc3339(120),
  },
])

function nextLayerId(): number {
  return configLayers.list().reduce((mx, l) => Math.max(mx, l.id), 0) + 1
}

export const handlers = [
  // ── 健康概览（configAdopt.ts 的 useConfigHealth）──────────────────────────
  //
  // `notice` 是后端原话：只陈述 JM 侧事实，不判定 Beacon 侧开关（前端刻意不改写）。
  domainRoute('get', '/config-files/health', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const files = configFileDefinitions.list()
    const layers = configLayers.list()
    const lastWrite = layers
      .map((l) => l.createdAt)
      .sort()
      .at(-1)
    return HttpResponse.json({
      configFileCount: files.length,
      layerVersionCount: layers.length,
      ...(lastWrite ? { lastWriteAt: lastWrite } : {}),
      notice: '本页只报告 JM 侧已纳管的配置定义与层版本数量；Beacon 侧的配置开关状态需到 Beacon 控制台查看。',
    })
  }),

  // ── 纳管（configAdopt.ts 的 useAdoptPreview / useAdopt）───────────────────
  //
  // preview 与真实纳管返回同一形状，仅 dryRun 不同——前端据此区分「预演」与「已写库」。
  // 逐文件结果里刻意混入 created / alreadyAdopted / failed 三种状态，
  // 让界面的「部分成功」展示分支有真实数据可渲染。
  domainRoute('post', '/config-files/adopt/preview', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { instanceIds?: number[]; paths?: string[] }
    const targets = body.instanceIds ?? []
    const paths = body.paths ?? ['server.properties']
    const results = targets.flatMap((instanceId) =>
      paths.map((path, index) => ({
        instanceId,
        instanceName: `game-${String(instanceId).padStart(2, '0')}`,
        path,
        name: path.split('/').pop() ?? path,
        format: path.endsWith('.json') ? 'json' : path.endsWith('.yml') || path.endsWith('.yaml') ? 'yaml' : 'properties',
        size: 512 + index * 128,
        contentHash: `sha256-${instanceId}-${index}`.padEnd(20, '0'),
        status: index === 0 ? 'would_create' : 'unchanged',
        dryRun: true,
      })),
    )
    return HttpResponse.json({
      results,
      instances: targets.map((instanceId) => ({ instanceId, discovered: paths.length, truncated: false })),
      summary: {
        total: results.length,
        created: results.filter((r) => r.status === 'would_create').length,
        appended: 0,
        unchanged: results.filter((r) => r.status === 'unchanged').length,
        alreadyAdopted: 0,
        failed: 0,
        instances: targets.length,
        dryRun: true,
      },
      dryRun: true,
    })
  }),

  domainRoute('post', '/config-files/adopt', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as { instanceIds?: number[]; paths?: string[] }
    const targets = body.instanceIds ?? []
    const paths = body.paths ?? ['server.properties']
    const results = targets.flatMap((instanceId) =>
      paths.map((path, index) => ({
        instanceId,
        instanceName: `game-${String(instanceId).padStart(2, '0')}`,
        path,
        name: path.split('/').pop() ?? path,
        format: path.endsWith('.json') ? 'json' : path.endsWith('.yml') || path.endsWith('.yaml') ? 'yaml' : 'properties',
        size: 512 + index * 128,
        contentHash: `sha256-${instanceId}-${index}`.padEnd(20, '0'),
        status: 'created',
        action: 'create',
        dryRun: false,
      })),
    )
    return HttpResponse.json({
      results,
      instances: targets.map((instanceId) => ({ instanceId, discovered: paths.length, truncated: false })),
      summary: {
        total: results.length,
        created: results.length,
        appended: 0,
        unchanged: 0,
        alreadyAdopted: 0,
        failed: 0,
        instances: targets.length,
        dryRun: false,
      },
      dryRun: false,
    })
  }),

  // ── 中央配置文件定义（configFiles.ts）─────────────────────────────────────
  domainRoute('get', '/config-files', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1))
    const pageSize = Math.max(1, Number(url.searchParams.get('pageSize') ?? 20))
    const all = configFileDefinitions.list()
    const start = (page - 1) * pageSize
    return HttpResponse.json({
      files: all.slice(start, start + pageSize),
      total: all.length,
      page,
      pageSize,
    })
  }),

  domainRoute('get', '/config-files/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const found = configFileDefinitions.list((f) => f.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '配置文件不存在' }, { status: 404 })
    return HttpResponse.json(found)
  }),

  domainRoute('post', '/config-files', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name?: string; format?: 'yaml' | 'json' | 'properties'; description?: string }
    const nextId = configFileDefinitions.list().reduce((mx, f) => Math.max(mx, f.id), 0) + 1
    const created: MockConfigFileDefinition = {
      id: nextId,
      name: body.name ?? `untitled-${nextId}`,
      format: body.format ?? 'yaml',
      description: body.description ?? '',
      createdBy: 1,
      createdAt: rfc3339(),
      updatedAt: rfc3339(),
      layerCount: 0,
    }
    configFileDefinitions.insert(created)
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/config-files/:id', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as Partial<MockConfigFileDefinition>
    const found = configFileDefinitions.list((f) => f.id === id)[0]
    if (!found) return HttpResponse.json({ code: 'NOT_FOUND', message: '配置文件不存在' }, { status: 404 })
    const updated = { ...found, ...body, id, updatedAt: rfc3339() }
    configFileDefinitions.update(id, body)
    return HttpResponse.json(updated)
  }),

  domainRoute('delete', '/config-files/:id', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    configFileDefinitions.remove(id)
    // 连带删除该定义的全部层版本，避免列表里出现孤儿 layerCount
    configLayers.list((l) => l.configFileId === id).forEach((l) => configLayers.remove(l.id))
    return new HttpResponse(null, { status: 204 })
  }),

  // ── 层版本（configFiles.ts 的 layers / effective）─────────────────────────
  domainRoute('get', '/config-files/:id/layers', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const layers = configLayers.list((l) => l.configFileId === id)
    // head = 每个 (scopeType, scopeId) 的最新版本
    const heads = new Map<string, MockConfigLayer>()
    for (const l of layers) {
      const key = `${l.scopeType}:${l.scopeId}`
      const prev = heads.get(key)
      if (!prev || l.versionNo > prev.versionNo) heads.set(key, l)
    }
    // ConfigLayerHead 的字段名是 scopeLevel / scopeRefId，且 createdAt 是 **Unix 秒**（后端 CreatedAt.Unix()），
    // 不是 RFC3339 字符串——两者混淆会让界面时间显示成 1970。
    return HttpResponse.json({
      layers: [...heads.values()].map((l) => ({
        scopeLevel: l.scopeType,
        scopeRefId: Number(l.scopeId),
        scopeName: l.scopeType === 'group' ? `region${l.scopeId}` : `game-${String(l.scopeId).padStart(2, '0')}`,
        versionId: l.id,
        versionNo: l.versionNo,
        contentHash: `sha256-${l.id}`.padEnd(20, '0'),
        remark: l.comment,
        createdBy: 1,
        createdAt: Math.floor(Date.parse(l.createdAt) / 1000),
        contentSize: new TextEncoder().encode(l.content).length,
      })),
    })
  }),

  domainRoute('post', '/config-files/:id/layers', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as { scopeType: 'group' | 'instance'; scopeId: string; content?: string; comment?: string }
    const same = configLayers.list((l) => l.configFileId === id && l.scopeType === body.scopeType && l.scopeId === body.scopeId)
    const layer: MockConfigLayer = {
      id: nextLayerId(),
      configFileId: id,
      scopeType: body.scopeType,
      scopeId: body.scopeId,
      versionNo: same.reduce((mx, l) => Math.max(mx, l.versionNo), 0) + 1,
      content: body.content ?? '',
      comment: body.comment ?? '',
      createdAt: rfc3339(),
    }
    configLayers.insert(layer)
    // 同步维护定义的 layerCount（界面用它显示「已有层数」）
    const def = configFileDefinitions.list((f) => f.id === id)[0]
    if (def) {
      const distinct = new Set(configLayers.list((l) => l.configFileId === id).map((l) => `${l.scopeType}:${l.scopeId}`))
      configFileDefinitions.update(def.id, { layerCount: distinct.size, updatedAt: rfc3339() })
    }
    return HttpResponse.json(layer, { status: 201 })
  }),

  domainRoute('get', '/config-files/:id/layers/:scopeType/:scopeId/history', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const p = info.params as { id: string; scopeType: string; scopeId: string }
    const versions = configLayers
      .list((l) => l.configFileId === Number(p.id) && l.scopeType === p.scopeType && l.scopeId === p.scopeId)
      .sort((a, b) => b.versionNo - a.versionNo)
    return HttpResponse.json({
      versions: versions.map((v) => ({
        id: v.id,
        versionNo: v.versionNo,
        contentHash: `sha256-${v.id}`.padEnd(20, '0'),
        content: v.content,
        remark: v.comment,
        createdBy: 1,
        createdAt: Math.floor(Date.parse(v.createdAt) / 1000),
      })),
    })
  }),

  domainRoute('delete', '/config-files/:id/layers/:scopeType/:scopeId', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const p = info.params as { id: string; scopeType: string; scopeId: string }
    configLayers
      .list((l) => l.configFileId === Number(p.id) && l.scopeType === p.scopeType && l.scopeId === p.scopeId)
      .forEach((l) => configLayers.remove(l.id))
    return new HttpResponse(null, { status: 204 })
  }),

  // 回退到指定历史版本（追加新版本，历史行不变——与后端语义一致）。
  domainRoute('post', '/config-files/:id/layers/:scopeType/:scopeId/rollback', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const p = info.params as { id: string; scopeType: string; scopeId: string }
    const body = (await info.request.json()) as { versionId?: number; remark?: string }
    const same = configLayers.list(
      (l) => l.configFileId === Number(p.id) && l.scopeType === p.scopeType && l.scopeId === p.scopeId,
    )
    const source = same.find((l) => l.id === body.versionId) ?? same.sort((a, b) => b.versionNo - a.versionNo)[0]
    if (!source) return HttpResponse.json({ code: 'NOT_FOUND', message: '版本不存在' }, { status: 404 })
    const rolled: MockConfigLayer = {
      id: nextLayerId(),
      configFileId: Number(p.id),
      scopeType: p.scopeType as 'group' | 'instance',
      scopeId: p.scopeId,
      versionNo: same.reduce((mx, l) => Math.max(mx, l.versionNo), 0) + 1,
      content: source.content,
      comment: body.remark ?? `回退到 v${source.versionNo}`,
      createdAt: rfc3339(),
    }
    configLayers.insert(rolled)
    return HttpResponse.json({
      id: rolled.id,
      versionNo: rolled.versionNo,
      contentHash: `sha256-${rolled.id}`.padEnd(20, '0'),
      content: rolled.content,
      remark: rolled.comment,
      createdBy: 1,
      createdAt: Math.floor(Date.parse(rolled.createdAt) / 1000),
      basedOnVersionId: source.id,
    }, { status: 201 })
  }),

  // effective：把 group 层与 instance 层合并后返回，让「按作用域解析」的分支可见。
  domainRoute('get', '/config-files/:id/effective', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const url = new URL(info.request.url)
    const instanceId = url.searchParams.get('instanceId') ?? ''
    const layers = configLayers
      .list((l) => l.configFileId === id)
      .filter((l) => l.scopeType === 'group' || (l.scopeType === 'instance' && l.scopeId === instanceId))
      .sort((a, b) => (a.scopeType === 'group' ? -1 : 1))
    return HttpResponse.json({
      configFileId: id,
      instanceId: Number(instanceId),
      content: layers.map((l) => l.content).join(''),
      appliedScopes: layers.map((l) => ({ scopeType: l.scopeType, scopeId: l.scopeId, versionNo: l.versionNo })),
    })
  }),
]
