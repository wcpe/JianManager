import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'

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
    'server-port': { key: 'server-port', type: 'int', default: '25565', description: '监听端口' },
    'online-mode': { key: 'online-mode', type: 'bool', default: 'true', description: '正版验证' },
    motd: { key: 'motd', type: 'string', default: 'A Minecraft Server', description: '服务器描述' },
    'max-players': { key: 'max-players', type: 'int', default: '20', description: '最大玩家数' },
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

  // ── 数据库资源管理器（FR-084，只读）────────────────────────────────────────

  // 列出 CP 数据库全部表及行数（仅平台管理员，requireAuth 兜底）。
  domainRoute('get', '/db/tables', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const tables = dbTables.list().map((t) => ({ name: t.name, rowCount: t.rows.length }))
    return HttpResponse.json({ tables })
  }),

  // 分页查询某表的行（敏感列脱敏 + 排序 + 简单过滤）。
  domainRoute('get', '/db/tables/:name/rows', (info) => {
    const denied = requireAuth(info)
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

    let rows = [...tbl.rows]
    if (filterColumn && validCol(filterColumn) && filterValue) {
      rows = rows.filter((r) => String(r[filterColumn] ?? '').includes(filterValue))
    }
    if (sort && validCol(sort)) {
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
