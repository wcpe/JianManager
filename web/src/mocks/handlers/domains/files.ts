import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'

/**
 * 文件与归档域 mock handler（FR-204）。覆盖 web/src/api/{files,fileVersions,storage,archive}.ts
 * 的全部 endpoint：实例工作目录文件树（列举/读/写/删/改名/上传/下载/搜索/打包）、文件版本（列举/diff/回滚）、
 * 平台存储概览/浏览/缓存清理、归档浏览/读条目/反编译。
 *
 * 字段严格对齐既有 api/*.ts 的 TS interface 与 docs/API.md 的响应结构。
 * 所有端点受 requireAuth 保护（文件 / 存储均需登录态）。
 * 跨实体联动：写文件→读回新内容、文件树新增；写改前快照→版本列表增长；存储缓存清理→概览刷新可见。
 */

/* ============================ 实例文件树 ============================ */

/** 假后端的一个文件树节点。content 仅文件有意义；目录无 content。size 由 content 派生（写时算）。 */
interface FileNode {
  /** registry 主键：`${instanceId}:${path}`，保证多实例隔离。 */
  id: string
  instanceId: number
  /** 相对工作目录、以 / 分隔的路径（无前导 /）。 */
  path: string
  isDir: boolean
  size: number
  modTime: number
  /** 文件文本内容（目录为空串）。 */
  content: string
}

/** 文件版本快照（对齐 service.FileVersion / api/fileVersions.ts）。 */
interface FileVersionRow {
  id: number
  instanceId: number
  filePath: string
  size: number
  authorId: number
  createdAt: string
  rollbackOfVersionId?: number
  /** 该版本对应的历史内容（mock 内部用于回滚写回）。 */
  content: string
}

const FILES_SEED_TIME = 1_719_100_000

// 实例 1 工作目录的种子文件树：根级 server.properties + plugins/ 目录 + plugins 下两个文件 + 一个 jar。
const files = db<FileNode>('files', () => [
  node(1, 'server.properties', false, 'motd=A Minecraft Server\nmax-players=20\nonline-mode=true\n'),
  node(1, 'plugins', true, ''),
  node(1, 'plugins/Essentials.jar', false, 'PK mock-jar-binary'),
  node(1, 'plugins/config.yml', false, 'debug: false\nlanguage: zh\n'),
  node(1, 'world', true, ''),
  node(1, 'world/level.dat', false, 'mock-nbt'),
])

// 文件版本种子：plugins/config.yml 已有一条历史版本（验证版本列表非空 + diff/回滚联动）。
const fileVersions = db<FileVersionRow>('fileVersions', () => [
  {
    id: 1,
    instanceId: 1,
    filePath: 'plugins/config.yml',
    size: 24,
    authorId: 1,
    createdAt: new Date(FILES_SEED_TIME * 1000).toISOString(),
    content: 'debug: true\nlanguage: en\n',
  },
])

function node(instanceId: number, path: string, isDir: boolean, content: string): FileNode {
  return {
    id: `${instanceId}:${path}`,
    instanceId,
    path,
    isDir,
    size: isDir ? 0 : byteLength(content),
    modTime: FILES_SEED_TIME,
    content,
  }
}

/** UTF-8 字节长度（mock 不必精确，但中文等多字节需正确以贴近后端 size）。 */
function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

/** 取某节点（按实例 + 路径）。 */
function getNode(instanceId: number, path: string): FileNode | undefined {
  return files.find((f) => f.instanceId === instanceId && f.path === path)
}

/** 某路径是否为目录 dir 的直接子项（dir 为空串表示根）。 */
function isDirectChild(path: string, dir: string): boolean {
  if (dir === '') return !path.includes('/')
  if (!path.startsWith(`${dir}/`)) return false
  return !path.slice(dir.length + 1).includes('/')
}

/** 把文件树投影为某目录的直接子项列表（对齐 service.FileInfo，目录在前再按名排序）。 */
function listDir(instanceId: number, dir: string) {
  const norm = dir.replace(/^\/+|\/+$/g, '')
  return files
    .list((f) => f.instanceId === instanceId && isDirectChild(f.path, norm))
    .map((f) => ({ name: f.path.split('/').pop() ?? f.path, isDir: f.isDir, size: f.size, modTime: f.modTime }))
    .sort((a, b) => (a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1))
}

/** 取 query 的 path（相对工作目录，去前后 /）。 */
function queryPath(request: Request, key = 'path'): string {
  return (new URL(request.url).searchParams.get(key) ?? '').replace(/^\/+|\/+$/g, '')
}

/** 写文件：存在则覆盖（先快照旧内容入 fileVersions，FR-051 联动），否则新建；自动补建缺失父目录。 */
function writeNode(instanceId: number, path: string, content: string): void {
  const existing = getNode(instanceId, path)
  if (existing && !existing.isDir) {
    // 覆盖写前快照当前内容（改前自动快照，FR-051）。
    const nextId = (fileVersions.list().at(-1)?.id ?? 0) + 1
    fileVersions.insert({
      id: nextId,
      instanceId,
      filePath: path,
      size: existing.size,
      authorId: 1,
      createdAt: new Date().toISOString(),
      content: existing.content,
    })
    files.update(existing.id, { content, size: byteLength(content), modTime: Math.floor(Date.now() / 1000) })
    return
  }
  ensureParents(instanceId, path)
  files.insert(node(instanceId, path, false, content))
}

/** 为某路径补建所有缺失的父目录（与后端「按路径自动建父目录」一致）。 */
function ensureParents(instanceId: number, path: string): void {
  const segs = path.split('/')
  let acc = ''
  for (let i = 0; i < segs.length - 1; i++) {
    acc = acc === '' ? segs[i] : `${acc}/${segs[i]}`
    if (!getNode(instanceId, acc)) files.insert(node(instanceId, acc, true, ''))
  }
}

export const handlers = [
  // ---- 列举目录（GET /instances/:id/files?path=）----
  domainRoute('get', '/instances/:id/files', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    return HttpResponse.json(listDir(instanceId, queryPath(info.request)))
  }),

  // ---- 读取文件文本（GET /instances/:id/files/read?path=）----
  domainRoute('get', '/instances/:id/files/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const target = getNode(instanceId, queryPath(info.request))
    if (!target || target.isDir) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '文件不存在' }, { status: 404 })
    }
    return HttpResponse.text(target.content)
  }),

  // ---- 写入文件（POST /instances/:id/files/write）----
  domainRoute('post', '/instances/:id/files/write', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { path, content } = (await info.request.json()) as { path: string; content: string }
    writeNode(instanceId, path.replace(/^\/+/, ''), content)
    return HttpResponse.json({ ok: true })
  }),

  // ---- 删除文件/目录（DELETE /instances/:id/files），递归删子项 ----
  domainRoute('delete', '/instances/:id/files', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { path } = (await info.request.json()) as { path: string }
    const target = path.replace(/^\/+|\/+$/g, '')
    files
      .list((f) => f.instanceId === instanceId && (f.path === target || f.path.startsWith(`${target}/`)))
      .forEach((f) => files.remove(f.id))
    return HttpResponse.json({ ok: true })
  }),

  // ---- 重命名/移动（POST /instances/:id/files/rename）----
  domainRoute('post', '/instances/:id/files/rename', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { oldPath, newPath } = (await info.request.json()) as { oldPath: string; newPath: string }
    const from = oldPath.replace(/^\/+|\/+$/g, '')
    const to = newPath.replace(/^\/+|\/+$/g, '')
    // 重命名该节点及其全部子孙路径前缀。
    files
      .list((f) => f.instanceId === instanceId && (f.path === from || f.path.startsWith(`${from}/`)))
      .forEach((f) => {
        const nextPath = f.path === from ? to : `${to}${f.path.slice(from.length)}`
        files.remove(f.id)
        files.insert({ ...f, id: `${instanceId}:${nextPath}`, path: nextPath, modTime: Math.floor(Date.now() / 1000) })
      })
    return HttpResponse.json({ ok: true })
  }),

  // ---- 上传（POST /instances/:id/files/upload，multipart）：写入 form 的 file 内容 ----
  domainRoute('post', '/instances/:id/files/upload', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const form = await info.request.formData()
    const dest = String(form.get('path') ?? '').replace(/^\/+/, '')
    const file = form.get('file')
    const content = file instanceof File ? await file.text() : String(file ?? '')
    if (dest) writeNode(instanceId, dest, content)
    return HttpResponse.json({ ok: true })
  }),

  // ---- 下载（GET /instances/:id/files/download?path=）：返回文件原始字节 ----
  domainRoute('get', '/instances/:id/files/download', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const target = getNode(instanceId, queryPath(info.request))
    if (!target || target.isDir) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '文件不存在' }, { status: 404 })
    }
    return new HttpResponse(target.content, { headers: { 'Content-Type': 'application/octet-stream' } })
  }),

  // ---- 跨文件搜索（POST /instances/:id/files/search）：对齐 SearchResult ----
  domainRoute('post', '/instances/:id/files/search', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { query, mode = 'content' } = (await info.request.json()) as {
      query: string
      mode?: 'content' | 'filename'
      maxResults?: number
    }
    const q = (query ?? '').toLowerCase()
    const hits: Array<{ path: string; line: number; snippet: string }> = []
    if (q) {
      for (const f of files.list((x) => x.instanceId === instanceId && !x.isDir)) {
        if (mode === 'filename') {
          if (f.path.toLowerCase().includes(q)) hits.push({ path: f.path, line: 0, snippet: '' })
          continue
        }
        const lines = f.content.split('\n')
        lines.forEach((ln, i) => {
          if (ln.toLowerCase().includes(q)) hits.push({ path: f.path, line: i + 1, snippet: ln })
        })
      }
    }
    return HttpResponse.json({ hits, truncated: false, indexing: false })
  }),

  // ---- 批量打包下载（POST /instances/:id/files/archive）：返回伪 zip 字节流 ----
  domainRoute('post', '/instances/:id/files/archive', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { paths } = (await info.request.json()) as { paths: string[] }
    if (!Array.isArray(paths) || paths.length === 0) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'paths 为空' }, { status: 400 })
    }
    return new HttpResponse(`PKmock-zip:${paths.join(',')}`, {
      headers: { 'Content-Type': 'application/zip', 'Content-Disposition': 'attachment; filename="files.zip"' },
    })
  }),

  /* ============================ 文件版本（FR-051） ============================ */

  // ---- 列出某文件版本（GET /instances/:id/files/versions?path=），按 ID 倒序 ----
  domainRoute('get', '/instances/:id/files/versions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const filePath = queryPath(info.request)
    // 投影为对外 FileVersion（剔除 mock 内部的 content / instanceId），按 ID 倒序（最新在前）。
    const rows = fileVersions
      .list((v) => v.instanceId === instanceId && v.filePath === filePath)
      .sort((a, b) => b.id - a.id)
      .map((v) => ({
        id: v.id,
        filePath: v.filePath,
        size: v.size,
        authorId: v.authorId,
        createdAt: v.createdAt,
        ...(v.rollbackOfVersionId ? { rollbackOfVersionId: v.rollbackOfVersionId } : {}),
      }))
    return HttpResponse.json(rows)
  }),

  // ---- 版本 diff（GET /instances/:id/files/diff?path=&from=&to=）----
  domainRoute('get', '/instances/:id/files/diff', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const url = new URL(info.request.url)
    const fromId = Number(url.searchParams.get('from'))
    const toId = Number(url.searchParams.get('to'))
    const filePath = queryPath(info.request)
    const from = fileVersions.find((v) => v.id === fromId)
    const to = toId
      ? fileVersions.find((v) => v.id === toId)?.content
      : getNode(instanceId, filePath)?.content
    const diff = from
      ? `--- v${fromId}\n+++ v${toId || 'current'}\n-${from.content}\n+${to ?? ''}`
      : ''
    return HttpResponse.json({ fromVersionId: fromId, toVersionId: toId, unifiedDiff: diff, binary: false })
  }),

  // ---- 回滚（POST /instances/:id/files/rollback）：写回旧内容 + 新增一条回滚版本 ----
  domainRoute('post', '/instances/:id/files/rollback', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number((info.params as { id: string }).id)
    const { path, versionId } = (await info.request.json()) as { path: string; versionId: number }
    const filePath = path.replace(/^\/+|\/+$/g, '')
    const target = fileVersions.find((v) => v.id === versionId)
    if (!target) return HttpResponse.json({ error: 'NOT_FOUND', message: '版本不存在' }, { status: 404 })
    // 回滚前快照当前内容，再写回旧内容（与后端「回滚前自动快照」一致）。
    writeNode(instanceId, filePath, target.content)
    const newId = (fileVersions.list().at(-1)?.id ?? 0) + 1
    fileVersions.insert({
      id: newId,
      instanceId,
      filePath,
      size: byteLength(target.content),
      authorId: 1,
      createdAt: new Date().toISOString(),
      rollbackOfVersionId: versionId,
      content: target.content,
    })
    return HttpResponse.json({ versionId: newId })
  }),

  /* ============================ 归档浏览 / 反编译（FR-075） ============================ */

  // ---- 列举归档条目（GET /instances/:id/files/archive/entries?path=）----
  domainRoute('get', '/instances/:id/files/archive/entries', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // mock 固定返回一个最小 jar 内容结构（plugin.yml + 一个 class），贴合 ArchiveEntries。
    return HttpResponse.json({
      entries: [
        { name: 'plugin.yml', isDir: false, size: 120, compressedSize: 90, modified: FILES_SEED_TIME, crc32: 111111 },
        { name: 'com/', isDir: true, size: 0, compressedSize: 0, modified: FILES_SEED_TIME, crc32: 0 },
        {
          name: 'com/example/Foo.class',
          isDir: false,
          size: 540,
          compressedSize: 320,
          modified: FILES_SEED_TIME,
          crc32: 222222,
        },
      ],
      truncated: false,
    })
  }),

  // ---- 读取归档内条目（GET /instances/:id/files/archive/read?path=&entry=）----
  domainRoute('get', '/instances/:id/files/archive/read', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const entry = new URL(info.request.url).searchParams.get('entry') ?? ''
    const binary = entry.endsWith('.class')
    const text = binary ? '' : `name: MockPlugin\nmain: com.example.Foo\nversion: 1.0\n`
    return new HttpResponse(text, {
      headers: {
        'Content-Type': 'application/octet-stream',
        ...(binary ? { 'X-Binary': 'true' } : {}),
      },
    })
  }),

  // ---- 反编译（POST /instances/:id/files/decompile）：返回成功的伪源码 ----
  domainRoute('post', '/instances/:id/files/decompile', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { entry } = (await info.request.json()) as { path: string; entry?: string }
    return HttpResponse.json({
      success: true,
      source: `/*\n * Decompiled with CFR 0.152 (mock).\n */\npublic class ${
        entry?.split('/').pop()?.replace('.class', '') ?? 'Foo'
      } {\n}\n`,
      truncated: false,
      decompiler: 'CFR 0.152',
    })
  }),

  /* ============================ 平台存储（FR-083） ============================ */

  // ---- 存储概览（GET /storage/overview）----。dirs/总计实时从 storageDirs 派生，故 cache 清理后概览随之变化。
  domainRoute('get', '/storage/overview', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const dirs = storageDirs.list()
    return HttpResponse.json({
      base: '/data/jianmanager',
      dirs,
      totalSize: dirs.reduce((s, d) => s + d.size, 0),
      totalFiles: dirs.reduce((s, d) => s + d.fileCount, 0),
      archive: { hotCount: 3, archivedCount: 1, externalCount: 0, hotSize: 48_234_123, archivedSize: 2048, externalSize: 0 },
    })
  }),

  // ---- 数据根目录浏览（GET /storage/files?path=）----
  domainRoute('get', '/storage/files', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const path = queryPath(info.request)
    return HttpResponse.json(listStorageDir(path))
  }),

  // ---- 缓存清理（POST /storage/cache/clear）：清空 cache 子项，概览随之归零联动 ----
  domainRoute('post', '/storage/cache/clear', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const cache = storageDirs.find((d) => d.path === 'cache')
    const removed = cache?.fileCount ?? 0
    if (cache) storageDirs.update(cache.id, { size: 0, fileCount: 0 })
    // 删除 cache 目录下的浏览子项。
    storageFiles
      .list((f) => f.parent === 'cache')
      .forEach((f) => storageFiles.remove(f.id))
    return HttpResponse.json({ removed })
  }),
]

/* ============================ 存储集合声明 ============================ */

/** 数据根 FHS 子目录占用（对齐 service.DirUsage）。 */
interface StorageDirRow {
  id: number
  path: string
  label: string
  size: number
  fileCount: number
  exists: boolean
  clearable: boolean
}

/** 数据根浏览子项（对齐 service.FileEntry，复用 explorer FileInfo 同形）。parent 为内部归属目录。 */
interface StorageFileRow {
  id: number
  /** 所属相对目录（空串=数据根）。 */
  parent: string
  name: string
  isDir: boolean
  size: number
  modTime: number
}

const storageDirs = db<StorageDirRow>('storageDirs', () => [
  { id: 1, path: 'var/artifacts', label: 'artifacts', size: 48_234_123, fileCount: 12, exists: true, clearable: false },
  { id: 2, path: 'opt/jdks', label: 'jdks', size: 320_000_000, fileCount: 240, exists: true, clearable: false },
  { id: 3, path: 'var/servers', label: 'servers', size: 1_048_576, fileCount: 30, exists: true, clearable: false },
  { id: 4, path: 'cache', label: 'cache', size: 4096, fileCount: 2, exists: true, clearable: true },
  { id: 5, path: 'var/log', label: 'log', size: 8192, fileCount: 3, exists: true, clearable: false },
])

const storageFiles = db<StorageFileRow>('storageFiles', () => [
  { id: 1, parent: '', name: 'var', isDir: true, size: 0, modTime: FILES_SEED_TIME },
  { id: 2, parent: '', name: 'opt', isDir: true, size: 0, modTime: FILES_SEED_TIME },
  { id: 3, parent: '', name: 'cache', isDir: true, size: 0, modTime: FILES_SEED_TIME },
  { id: 4, parent: 'var', name: 'artifacts', isDir: true, size: 0, modTime: FILES_SEED_TIME },
  { id: 5, parent: 'var', name: 'log', isDir: true, size: 0, modTime: FILES_SEED_TIME },
  { id: 6, parent: 'cache', name: 'tmp-build.log', isDir: false, size: 2048, modTime: FILES_SEED_TIME },
  { id: 7, parent: 'cache', name: 'resolve.json', isDir: false, size: 2048, modTime: FILES_SEED_TIME },
])

/** 投影数据根某目录的直接子项（目录在前再按名排序，对齐 StorageFileEntry）。 */
function listStorageDir(path: string) {
  return storageFiles
    .list((f) => f.parent === path)
    .map((f) => ({ name: f.name, isDir: f.isDir, size: f.size, modTime: f.modTime }))
    .sort((a, b) => (a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1))
}
