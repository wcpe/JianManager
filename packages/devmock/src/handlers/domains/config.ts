import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'

/**
 * 配置与数据库域 mock handler（FR-205）。
 * 覆盖两组能力：
 * - **配置引擎**（FR-031/FR-071，`web/src/api/configs.ts`）：发现 / 列出 / 读 / 写（文本+字段）/
 *   跨文件校验 / 版本列表 / diff / 回滚。写操作联动版本表：write → 新增版本、versions 读回。
 * - **数据库资源管理器**（FR-084，`web/src/api/db.ts`）：表清单 + 分页/排序/过滤的只读行浏览。
 *
 * 字段保真：响应结构严格匹配上述两个 api 模块的 TS interface（`schemaJson` 为字符串化 JSON——
 * 见全局记忆「JSON 字符串字段前端解析」，前端 `JSON.parse` 后再用）。
 * 受保护端点（除 discover/list/read 等只读浏览仍按既有权限走 requireAuth）首行 requireAuth。
 */

/** 假后端的单个实例配置文件（read 返回的字段在此就地建模，写操作回写 content/fields）。 */
export interface MockConfigFile {
  /** 复合主键：`${instanceId}:${path}`，使多实例配置不串集合。 */
  id: string
  instanceId: number
  path: string
  format: string
  size: number
  updatedAt: number
  /** 命中内置 schema → 可走表单模式。 */
  supported: boolean
  content: string
  fields: { key: string; value: string; type: string; description?: string; line?: number }[]
  /** 字符串化 ModelSchema（无 schema 时为空串）。 */
  schemaJson: string
}

/** 配置版本（write/rollback 生成，versions 倒序读回，diff 取两版内容）。 */
export interface MockConfigVersion {
  id: number
  instanceId: number
  filePath: string
  message: string
  authorId: number
  createdAt: string
  content: string
  rollbackOfVersionId?: number
}

/** CP 数据库的一张表（行只读浏览；敏感列由 sensitive 列定义标注，值在 resolver 内打码）。 */
export interface MockDbTable {
  id: string
  name: string
  columns: { name: string; type: string; sensitive: boolean }[]
  rows: Record<string, unknown>[]
}

/**
 * 受管配置项来源登记（FR-451）。复合主键 `${instanceId}:${itemKey}`。
 * 二态互斥：inline 内联值（平台持有）/ file 文件引用（平台不覆写）。
 */
export interface MockConfigSource {
  id: string
  instanceId: number
  itemKey: string
  source: 'inline' | 'file'
  inlineValue: string
  filePath: string
  fileKey: string
}

/** 配置基线（模板）（FR-458），键为 (scopeKey, filePath)。 */
export interface MockConfigBaseline {
  id: number
  scopeKey: string
  filePath: string
  content: string
  contentHash: string
  message: string
  authorId: number
  createdAt: string
  updatedAt: string
}

/** 明面化的 server.properties 关键项（顺序即渲染顺序，与后端 surfacePropKeys 对齐）。 */
const SURFACE_PROPS: { key: string; group: string; description: string; type: string; choices?: string[] }[] = [
  { key: 'server-port', group: '网络与身份', description: '监听端口（server-port）', type: 'int' },
  { key: 'enable-query', group: '网络与身份', description: '启用 GameSpy4 Query', type: 'bool', choices: ['true', 'false'] },
  { key: 'query.port', group: '网络与身份', description: 'Query 监听端口', type: 'int' },
  { key: 'view-distance', group: '世界与性能', description: '视距（区块）', type: 'int' },
  { key: 'max-players', group: '展示与容量', description: '最大玩家数', type: 'int' },
  { key: 'motd', group: '展示与容量', description: '服务器描述', type: 'string' },
  { key: 'online-mode', group: '网络与身份', description: '正版验证', type: 'bool', choices: ['true', 'false'] },
]

/** 解析 properties 正文为键值映射（跳过空行与 # 注释）。 */
function parseProperties(content: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of content.split('\n')) {
    const t = line.trim()
    if (!t || t.startsWith('#')) continue
    const i = t.indexOf('=')
    if (i < 0) continue
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return out
}

/** 假后端用的确定性哈希（形如 sha256 的 64 位十六进制；保真契约在形状而非密码学强度）。 */
function mockHash(content: string): string {
  let h1 = 0x811c9dc5
  let h2 = 0x01000193
  for (let i = 0; i < content.length; i++) {
    const c = content.charCodeAt(i)
    h1 = Math.imul(h1 ^ c, 0x01000193)
    h2 = Math.imul(h2 + c, 0x85ebca6b) ^ (h2 >>> 13)
  }
  const hex = (n: number) => (n >>> 0).toString(16).padStart(8, '0')
  const seed = `${hex(h1)}${hex(h2)}${hex(h1 ^ h2)}${hex(Math.imul(h1, h2))}`
  return (seed + seed).slice(0, 64)
}

/**
 * 构造实例受管配置项清单（FR-451）。GET/PUT surface 共用，保证响应与
 * `web/src/api/configSurface.ts` 的 `ConfigSurfaceItem` 同构。
 * 未登记项按隐含默认：启动项内联取实例现值、props 项文件隐含（读 server.properties 预览）。
 */
function buildSurfaceItems(instanceId: number): Record<string, unknown>[] {
  const inst = instances().get(instanceId)
  const rows = configSources.list((r) => r.instanceId === instanceId)
  const byKey = new Map(rows.map((r) => [r.itemKey, r]))
  const propsValues = parseProperties(findConfig(instanceId, 'server.properties')?.content ?? '')
  const items: Record<string, unknown>[] = []

  const startupItems: { itemKey: string; description: string; value: string }[] = [
    { itemKey: 'startup.command', description: '服务端启动命令（对下次启动生效）', value: inst?.startCommand ?? '' },
    { itemKey: 'startup.launchSpec', description: 'MC 结构化启动规格（JSON，对下次启动生效）', value: '' },
  ]
  for (const s of startupItems) {
    const row = byKey.get(s.itemKey)
    if (row && row.source === 'file') {
      items.push({
        itemKey: s.itemKey, source: 'file', registered: true, filePath: row.filePath, fileKey: row.fileKey,
        effectiveValue: '', effectiveSource: 'file', editable: false, group: '启动参数',
        description: s.description, type: 'string',
      })
    } else {
      const v = row?.inlineValue ?? s.value
      items.push({
        itemKey: s.itemKey, source: 'inline', registered: !!row, inlineValue: v,
        effectiveValue: v, effectiveSource: 'inline', editable: true, group: '启动参数',
        description: s.description, type: 'string',
      })
    }
  }

  for (const p of SURFACE_PROPS) {
    const itemKey = `props.${p.key}`
    const row = byKey.get(itemKey)
    if (row && row.source === 'inline') {
      items.push({
        itemKey, source: 'inline', registered: true, inlineValue: row.inlineValue,
        effectiveValue: row.inlineValue, effectiveSource: 'inline', editable: true,
        group: p.group, description: p.description, type: p.type, choices: p.choices,
      })
    } else {
      items.push({
        itemKey, source: 'file', registered: !!row, filePath: row?.filePath ?? 'server.properties',
        fileKey: row?.fileKey ?? p.key, effectiveValue: propsValues[row?.fileKey ?? p.key] ?? '',
        effectiveSource: 'file', editable: false, group: p.group, description: p.description,
        type: p.type, choices: p.choices,
      })
    }
  }
  return items
}

const SERVER_PROPERTIES = [
  'server-port=25565',
  'online-mode=true',
  'motd=A Mock Minecraft Server',
  'max-players=20',
].join('\n')

const SERVER_PROPERTIES_SCHEMA = JSON.stringify({
  name: 'server.properties',
  description: 'Minecraft 服务端核心配置',
  format: 'properties',
  fields: {
    'server-port': { key: 'server-port', type: 'int', default: '25565', description: '监听端口', group: '网络与身份' },
    'online-mode': { key: 'online-mode', type: 'bool', default: 'true', description: '正版验证', group: '网络与身份' },
    motd: { key: 'motd', type: 'string', default: 'A Minecraft Server', description: '服务器描述', group: '展示与容量' },
    'max-players': { key: 'max-players', type: 'int', default: '20', description: '最大玩家数', group: '展示与容量' },
  },
})

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const configFiles = db<MockConfigFile>('configFiles', () => [
  {
    id: '1:server.properties',
    instanceId: 1,
    path: 'server.properties',
    format: 'properties',
    size: SERVER_PROPERTIES.length,
    updatedAt: 1710000000,
    supported: true,
    content: SERVER_PROPERTIES,
    fields: [
      { key: 'server-port', value: '25565', type: 'int', description: '监听端口', line: 1 },
      { key: 'online-mode', value: 'true', type: 'bool', description: '正版验证', line: 2 },
      { key: 'motd', value: 'A Mock Minecraft Server', type: 'string', description: '服务器描述', line: 3 },
      { key: 'max-players', value: '20', type: 'int', description: '最大玩家数', line: 4 },
    ],
    schemaJson: SERVER_PROPERTIES_SCHEMA,
  },
  {
    id: '1:config/paper-global.yml',
    instanceId: 1,
    path: 'config/paper-global.yml',
    format: 'yaml',
    size: 64,
    updatedAt: 1710000100,
    supported: false,
    content: 'proxies:\n  velocity:\n    enabled: false\n    secret: ""\n',
    fields: [],
    schemaJson: '',
  },
])

const configVersions = db<MockConfigVersion>('configVersions', () => [
  {
    id: 1,
    instanceId: 1,
    filePath: 'server.properties',
    message: '初始化配置',
    authorId: 1,
    createdAt: '2024-03-09T10:00:00Z',
    content: 'server-port=25565\nonline-mode=true\nmotd=A Minecraft Server\nmax-players=20',
  },
  {
    id: 2,
    instanceId: 1,
    filePath: 'server.properties',
    message: '改 motd 与玩家上限',
    authorId: 1,
    createdAt: '2024-03-09T11:00:00Z',
    content: SERVER_PROPERTIES,
  },
])

const dbTables = db<MockDbTable>('dbTables', () => [
  {
    id: 'users',
    name: 'users',
    columns: [
      { name: 'id', type: 'INTEGER', sensitive: false },
      { name: 'username', type: 'VARCHAR(64)', sensitive: false },
      { name: 'password_hash', type: 'VARCHAR(255)', sensitive: true },
      { name: 'role', type: 'INTEGER', sensitive: false },
    ],
    rows: [
      { id: 1, username: 'admin', password_hash: '$2a$10$abcdefghijklmnopqrstuv', role: 10 },
      { id: 2, username: 'operator', password_hash: '$2a$10$wxyz0123456789abcdefgh', role: 1 },
      { id: 3, username: 'viewer', password_hash: '$2a$10$zzzzzzzzzzzzzzzzzzzzzz', role: 0 },
    ],
  },
  {
    id: 'instances',
    name: 'instances',
    columns: [
      { name: 'id', type: 'INTEGER', sensitive: false },
      { name: 'name', type: 'VARCHAR(128)', sensitive: false },
      { name: 'status', type: 'VARCHAR(32)', sensitive: false },
    ],
    rows: [
      { id: 1, name: 'survival', status: 'RUNNING' },
      { id: 2, name: 'creative', status: 'STOPPED' },
    ],
  },
])

// 受管配置项登记（FR-451）：种子给实例 1 显式登记两项（query.port 内联 / motd 文件引用），
// 其余项走「未登记」的隐含默认（启动项内联、props 文件隐含），与后端旧实例平滑过渡语义一致。
const configSources = db<MockConfigSource>('configSources', () => [
  {
    id: '1:props.query.port',
    instanceId: 1,
    itemKey: 'props.query.port',
    source: 'inline',
    inlineValue: '25566',
    filePath: 'server.properties',
    fileKey: 'query.port',
  },
  {
    id: '1:props.motd',
    instanceId: 1,
    itemKey: 'props.motd',
    source: 'file',
    inlineValue: '',
    filePath: 'server.properties',
    fileKey: 'motd',
  },
])

// 配置基线（FR-458）：种子一条 group:2（生存分组）的 server.properties 基线，供漂移/收敛演示。
const configBaselines = db<MockConfigBaseline>('configBaselines', () => [
  {
    id: 1,
    scopeKey: 'group:2',
    filePath: 'server.properties',
    content: 'server-port=25565\nonline-mode=true\nmotd=Baseline MOTD\nmax-players=20',
    contentHash: mockHash('server-port=25565\nonline-mode=true\nmotd=Baseline MOTD\nmax-players=20'),
    message: '生存分组基线',
    authorId: 1,
    createdAt: '2026-03-01T00:00:00Z',
    updatedAt: '2026-03-01T00:00:00Z',
  },
])

/**
 * 只读引用实例域集合。
 * **必须惰性取用**：`db(name)` 首次调用决定种子，若无 seedFn 则建空集合且不再播种——
 * 本模块（config.ts）在 instance.ts 之前被 eager import 时若顶层直接 `db('instances')`，
 * 会把实例集合建成空集、吞掉 instance.ts 的种子。故一律在 handler 内惰性取用（此时已播种）。
 */
interface InstanceRow {
  id: number
  name: string
  role: string
  type: string
  status: string
  tags: string
  startCommand: string
}
const instances = () => db<InstanceRow>('instances')
const groupMembers = () => db<{ id: number; groupId: number; instanceId: number }>('instanceGroupMembers')
const groupNodes = () => db<{ id: number; parentId: number | null }>('instanceGroups')

/** 基线 scope 解析：all/instance:/group:（含子树）/tag: → 实例行。 */
function resolveScopeInstances(scopeKey: string): { id: number; name: string }[] {
  const rows = instances().list()
  const key = scopeKey.trim()
  if (!key || key === 'all') return rows.map((r) => ({ id: r.id, name: r.name }))
  const [kind, raw] = key.split(':')
  if (kind === 'instance') {
    const id = Number(raw)
    const one = rows.find((r) => r.id === id)
    return one ? [{ id: one.id, name: one.name }] : []
  }
  if (kind === 'group') {
    const root = Number(raw)
    const descendants = new Set<number>([root])
    let grew = true
    while (grew) {
      grew = false
      for (const g of groupNodes().list()) {
        if (g.parentId != null && descendants.has(g.parentId) && !descendants.has(g.id)) {
          descendants.add(g.id)
          grew = true
        }
      }
    }
    const ids = new Set(
      groupMembers().list().filter((m) => descendants.has(m.groupId)).map((m) => m.instanceId),
    )
    return rows.filter((r) => ids.has(r.id)).map((r) => ({ id: r.id, name: r.name }))
  }
  if (kind === 'tag') {
    return rows
      .filter((r) => {
        try {
          const tags = JSON.parse(r.tags || '[]')
          return Array.isArray(tags) && tags.includes(raw)
        } catch {
          return false
        }
      })
      .map((r) => ({ id: r.id, name: r.name }))
  }
  return []
}

/** 实例某配置文件的当前内容哈希：优先最新配置版本，缺失则现场读文件内容。 */
function currentConfigHash(instanceId: number, filePath: string): string {
  const versions = configVersions
    .list((v) => v.instanceId === instanceId && v.filePath === filePath)
    .sort((a, b) => b.id - a.id)
  if (versions.length > 0) return mockHash(versions[0].content)
  const cfg = findConfig(instanceId, filePath)
  return mockHash(cfg?.content ?? '')
}

const MASKED = '******'

/** 解析 read/write 的 `?path=` 或 body.path 对应的配置文件（按 instanceId+path）。 */
function findConfig(instanceId: number, path: string | null): MockConfigFile | undefined {
  if (!path) return undefined
  return configFiles.find((c) => c.instanceId === instanceId && c.path === path)
}

/** 生成下一个版本号（全集合自增，与真实自增主键语义一致）。 */
function nextVersionId(): number {
  return configVersions.list().reduce((mx, v) => Math.max(mx, v.id), 0) + 1
}

export function seed(): void {
  // 集合声明即播种；本函数供聚合器显式调用以保证幂等（reset 已覆盖测试隔离）。
  configFiles.reset()
  configVersions.reset()
  dbTables.reset()
  configSources.reset()
  configBaselines.reset()
}

export const handlers = [
  // ── 配置引擎（FR-031 / FR-071）─────────────────────────────────────────────

  // 递归发现工作目录全部配置文件（扁平列表 + 截断标记）。
  domainRoute('get', '/instances/:id/configs/discover', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const files = configFiles
      .list((c) => c.instanceId === instanceId)
      .map((c) => ({ path: c.path, format: c.format, supported: c.supported }))
    return HttpResponse.json({ files, truncated: false })
  }),

  // 列出某目录内可管理配置文件（内置可识别格式）。
  domainRoute('get', '/instances/:id/configs', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const list = configFiles
      .list((c) => c.instanceId === instanceId)
      .map((c) => ({
        path: c.path,
        format: c.format,
        size: c.size,
        updatedAt: c.updatedAt,
        supported: c.supported,
      }))
    return HttpResponse.json(list)
  }),

  // 读取单配置文件：原文 + 字段 + schema JSON + 校验结果。
  domainRoute('get', '/instances/:id/configs/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const path = new URL(info.request.url).searchParams.get('path')
    const cfg = findConfig(instanceId, path)
    if (!cfg) return HttpResponse.json({ error: 'NOT_FOUND', message: '配置文件不存在' }, { status: 404 })
    return HttpResponse.json({
      path: cfg.path,
      format: cfg.format,
      content: cfg.content,
      fields: cfg.fields,
      schemaJson: cfg.schemaJson,
      validation: { valid: true, issues: [] },
    })
  }),

  // 文本模式写入配置，保存成功生成配置版本（联动 versions/read）。
  domainRoute('post', '/instances/:id/configs/write', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { path, content, message } = (await info.request.json()) as {
      path: string
      content: string
      message?: string
    }
    const cfg = findConfig(instanceId, path)
    if (cfg) configFiles.update(cfg.id, { content, size: content.length, updatedAt: Date.now() })
    const versionId = nextVersionId()
    configVersions.insert({
      id: versionId,
      instanceId,
      filePath: path,
      message: message ?? '',
      authorId: 1,
      createdAt: new Date().toISOString(),
      content,
    })
    return HttpResponse.json({ versionId, validation: { valid: true, issues: [] } })
  }),

  // 表单模式写入：字段级补丁回原文（保留注释），生成配置版本。
  domainRoute('post', '/instances/:id/configs/write-fields', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { path, fields, message } = (await info.request.json()) as {
      path: string
      fields: Record<string, string>
      message?: string
    }
    const cfg = findConfig(instanceId, path)
    if (cfg) {
      const nextFields = cfg.fields.map((f) => (f.key in fields ? { ...f, value: fields[f.key] } : f))
      const nextContent = nextFields.map((f) => `${f.key}=${f.value}`).join('\n')
      configFiles.update(cfg.id, { fields: nextFields, content: nextContent, updatedAt: Date.now() })
    }
    const versionId = nextVersionId()
    configVersions.insert({
      id: versionId,
      instanceId,
      filePath: path,
      message: message ?? '',
      authorId: 1,
      createdAt: new Date().toISOString(),
      content: cfg?.content ?? '',
    })
    return HttpResponse.json({ versionId, validation: { valid: true, issues: [] } })
  }),

  // 跨文件/跨实例一致性校验（返回 warning 列表，不影响写入）。
  domainRoute('post', '/instances/:id/configs/cross-check', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { content } = (await info.request.json()) as { path: string; content: string }
    // 简化规则：online-mode=true 且开了 velocity 转发即告警，否则通过。
    const issues =
      content.includes('online-mode=true') && content.includes('velocity')
        ? [{ level: 'warning', message: 'online-mode 与代理转发不配套', key: 'online-mode' }]
        : []
    return HttpResponse.json({ issues })
  }),

  // 列出某配置文件历史版本（按 ID 倒序）。
  domainRoute('get', '/instances/:id/configs/versions/:file', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const filePath = decodeURIComponent((info.params as { file: string }).file)
    const versions = configVersions
      .list((v) => v.instanceId === instanceId && v.filePath === filePath)
      .sort((a, b) => b.id - a.id)
      .map((v) => ({
        id: v.id,
        filePath: v.filePath,
        message: v.message,
        authorId: v.authorId,
        createdAt: v.createdAt,
        ...(v.rollbackOfVersionId ? { rollbackOfVersionId: v.rollbackOfVersionId } : {}),
      }))
    return HttpResponse.json(versions)
  }),

  // 配置版本差异（from/to 两版内容做简化 unified diff）。
  domainRoute('get', '/instances/:id/configs/diff/:file', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const fromId = Number(url.searchParams.get('from'))
    const toId = Number(url.searchParams.get('to'))
    const from = configVersions.get(fromId)
    const to = configVersions.get(toId)
    const fromContent = from?.content ?? ''
    const toContent = to?.content ?? ''
    const unifiedDiff = `--- #${fromId}\n+++ #${toId}\n-${fromContent.split('\n').join('\n-')}\n+${toContent
      .split('\n')
      .join('\n+')}`
    return HttpResponse.json({ fromVersionId: fromId, toVersionId: toId, unifiedDiff, fromContent, toContent })
  }),

  // 回滚配置到指定版本并生成新版本记录（联动 read：回写文件 content）。
  domainRoute('post', '/instances/:id/configs/rollback/:file', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const filePath = decodeURIComponent((info.params as { file: string }).file)
    const { versionId, message } = (await info.request.json()) as { versionId: number; message?: string }
    const target = configVersions.get(versionId)
    const cfg = findConfig(instanceId, filePath)
    if (cfg && target) configFiles.update(cfg.id, { content: target.content, updatedAt: Date.now() })
    const newId = nextVersionId()
    configVersions.insert({
      id: newId,
      instanceId,
      filePath,
      message: message ?? `回滚到 #${versionId}`,
      authorId: 1,
      createdAt: new Date().toISOString(),
      content: target?.content ?? '',
      rollbackOfVersionId: versionId,
    })
    return HttpResponse.json({ versionId: newId })
  }),

  // ── 配置源明面化（FR-451）────────────────────────────────────────────────

  // 受管配置项清单：启动项 + server.properties 关键项，每项显式声明来源与生效值预览。
  domainRoute('get', '/instances/:id/configs/surface', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    if (!instances().get(instanceId)) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    }
    return HttpResponse.json({ items: buildSurfaceItems(instanceId) })
  }),

  // 按项更新配置源：内联写回真源（启动命令/属性文件），文件引用仅登记。
  domainRoute('put', '/instances/:id/configs/surface', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const body = (await info.request.json()) as {
      items: { itemKey: string; source: 'inline' | 'file'; inlineValue?: string; filePath?: string; fileKey?: string }[]
    }
    for (const it of body.items ?? []) {
      const id = `${instanceId}:${it.itemKey}`
      const existing = configSources.get(id)
      const row: MockConfigSource = {
        id,
        instanceId,
        itemKey: it.itemKey,
        source: it.source,
        inlineValue: it.source === 'inline' ? (it.inlineValue ?? existing?.inlineValue ?? '') : '',
        filePath: it.filePath ?? existing?.filePath ?? '',
        fileKey: it.fileKey ?? existing?.fileKey ?? '',
      }
      if (existing) configSources.update(id, row)
      else configSources.insert(row)
      // 内联写回真源（对齐后端 applyInline）。
      if (it.source === 'inline') {
        if (it.itemKey === 'startup.command') {
          instances().update(instanceId, { startCommand: row.inlineValue })
        } else if (it.itemKey.startsWith('props.')) {
          const key = it.itemKey.slice('props.'.length)
          const cfg = findConfig(instanceId, 'server.properties')
          if (cfg) {
            const next = { ...parseProperties(cfg.content), [key]: row.inlineValue }
            const content = Object.entries(next).map(([k, v]) => `${k}=${v}`).join('\n')
            configFiles.update(cfg.id, { content, updatedAt: Date.now() })
          }
        }
      }
    }
    // 返回刷新后的完整清单（与 GET surface 同构，保证响应保真）。
    return HttpResponse.json({ items: buildSurfaceItems(instanceId) })
  }),

  // ── 配置基线（FR-458）────────────────────────────────────────────────────

  domainRoute('get', '/config-baselines', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ baselines: configBaselines.list().sort((a, b) => a.id - b.id) })
  }),

  domainRoute('post', '/config-baselines', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { scopeKey?: string; filePath?: string; content?: string; message?: string }
    if (!body.scopeKey || !body.filePath)
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'scopeKey/filePath 不能为空' }, { status: 422 })
    const content = body.content ?? ''
    const hash = mockHash(content)
    const existing = configBaselines.find((b) => b.scopeKey === body.scopeKey && b.filePath === body.filePath)
    if (existing) {
      const row = configBaselines.update(existing.id, {
        content, contentHash: hash, message: body.message ?? '', updatedAt: new Date().toISOString(),
      })
      return HttpResponse.json(row)
    }
    const row = configBaselines.insert({
      scopeKey: body.scopeKey, filePath: body.filePath, content, contentHash: hash,
      message: body.message ?? '', authorId: 1,
      createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
    })
    return HttpResponse.json(row)
  }),

  domainRoute('get', '/config-baselines/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const row = configBaselines.get(Number((info.params as { id: string }).id))
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '基线不存在' }, { status: 404 })
    return HttpResponse.json(row)
  }),

  domainRoute('delete', '/config-baselines/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    configBaselines.remove(Number((info.params as { id: string }).id))
    return HttpResponse.json({ deleted: true })
  }),

  // 漂移检测（只读）：scope 内逐台取当前哈希与基线比对。
  domainRoute('get', '/config-baselines/:id/drift', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const bl = configBaselines.get(Number((info.params as { id: string }).id))
    if (!bl) return HttpResponse.json({ error: 'NOT_FOUND', message: '基线不存在' }, { status: 404 })
    const items = resolveScopeInstances(bl.scopeKey).map((inst) => {
      const currentHash = currentConfigHash(inst.id, bl.filePath)
      return {
        instanceId: inst.id,
        instanceName: inst.name,
        drift: currentHash !== bl.contentHash,
        currentHash,
        baselineHash: bl.contentHash,
        hasVersion: configVersions.list((v) => v.instanceId === inst.id && v.filePath === bl.filePath).length > 0,
      }
    })
    return HttpResponse.json({ items, drifted: items.filter((i) => i.drift).length })
  }),

  // 一键收敛：对漂移实例推送基线内容并复核残余漂移。
  domainRoute('post', '/config-baselines/:id/converge', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const bl = configBaselines.get(Number((info.params as { id: string }).id))
    if (!bl) return HttpResponse.json({ error: 'NOT_FOUND', message: '基线不存在' }, { status: 404 })
    const targets = resolveScopeInstances(bl.scopeKey).filter(
      (inst) => currentConfigHash(inst.id, bl.filePath) !== bl.contentHash,
    )
    const results = targets.map((inst) => {
      const cfg = findConfig(inst.id, bl.filePath)
      if (cfg) configFiles.update(cfg.id, { content: bl.content, size: bl.content.length, updatedAt: Date.now() })
      const versionId = nextVersionId()
      configVersions.insert({
        id: versionId, instanceId: inst.id, filePath: bl.filePath,
        message: `配置基线收敛 #${bl.id}`, authorId: 1,
        createdAt: new Date().toISOString(), content: bl.content,
      })
      return { instanceId: inst.id, success: true, versionId }
    })
    return HttpResponse.json({
      baselineId: bl.id,
      targeted: targets.length,
      succeeded: results.length,
      failed: 0,
      results,
      residualDrift: [],
    })
  }),

  // ── 数据库资源管理器（FR-084，只读）────────────────────────────────────────

  // 列出 CP 数据库全部表及行数（仅平台管理员，requireAuth 兜底）。
  domainRoute('get', '/db/tables', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const tables = dbTables.list().map((t) => ({ name: t.name, rowCount: t.rows.length }))
    return HttpResponse.json({ tables })
  }),

  // 分页查询某表的行（敏感列脱敏 + 排序 + 简单过滤）。
  domainRoute('get', '/db/tables/:name/rows', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const name = (info.params as { name: string }).name
    const tbl = dbTables.find((t) => t.name === name)
    if (!tbl) return HttpResponse.json({ error: 'NOT_FOUND', message: '表不存在' }, { status: 404 })

    const url = new URL(info.request.url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1))
    const pageSize = Math.max(1, Number(url.searchParams.get('pageSize') ?? 50))
    const sort = url.searchParams.get('sort') ?? ''
    const order = (url.searchParams.get('order') ?? 'asc') as 'asc' | 'desc'
    const filterColumn = url.searchParams.get('filterColumn') ?? ''
    const filterValue = url.searchParams.get('filterValue') ?? ''
    const sensitiveCols = new Set(tbl.columns.filter((c) => c.sensitive).map((c) => c.name))
    const validCol = (col: string) => tbl.columns.some((c) => c.name === col)
    const queryableCol = (col: string) => validCol(col) && !sensitiveCols.has(col)

    let rows = [...tbl.rows]
    if (filterColumn && queryableCol(filterColumn) && filterValue) {
      rows = rows.filter((r) => String(r[filterColumn] ?? '').includes(filterValue))
    }
    if (sort && queryableCol(sort)) {
      rows.sort((a, b) => {
        const av = String(a[sort] ?? '')
        const bv = String(b[sort] ?? '')
        const cmp = av.localeCompare(bv, undefined, { numeric: true })
        return order === 'desc' ? -cmp : cmp
      })
    }
    const total = rows.length
    const pageRows = rows.slice((page - 1) * pageSize, page * pageSize).map((r) => {
      const masked: Record<string, unknown> = { ...r }
      for (const col of sensitiveCols) if (masked[col] != null) masked[col] = MASKED
      return masked
    })
    return HttpResponse.json({
      table: tbl.name,
      columns: tbl.columns,
      rows: pageRows,
      page,
      pageSize,
      total,
    })
  }),
]
