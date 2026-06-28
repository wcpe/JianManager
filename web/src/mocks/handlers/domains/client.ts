import { http, HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { requireAuth } from '@/mocks/auth-middleware'

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

/** 频道下未吊销密钥数（列表 keyCount）。 */
const keyCountOf = (channelId: string) =>
  keys.list((k) => k.channelId === channelId && !k.revoked).length

/** 频道当前 latest 版本号（取该频道最大版本，无则 0）。 */
const latestVersionOf = (channelId: string) =>
  versions.list((v) => v.channelId === channelId).reduce((m, v) => Math.max(m, v.version), 0)

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

/** 构造频道分发统计（匹配 ClientDistStats）。 */
function buildStats(channelId: string, days: number) {
  const downloads = Array.from({ length: Math.min(days, 3) }, (_, i) => ({
    day: `2026-06-${String(20 + i).padStart(2, '0')}`,
    requests: 100 + i * 20,
    bytes: (100 + i * 20) * 1_000_000,
  }))
  return {
    channelId,
    days,
    downloads,
    versions: [
      { version: 2, requests: 240 },
      { version: 1, requests: 60 },
    ],
    results: [
      { result: 'success', count: 280 },
      { result: 'rolled-back', count: 12 },
      { result: 'error', count: 8 },
    ],
    successRate: 0.93,
    rollbackRate: 0.04,
    activeMachines: 42,
    topIps: [
      { ip: '203.0.113.1', count: 30 },
      { ip: '198.51.100.7', count: 18 },
    ],
  }
}

/** 构造频道观测视图（匹配 ClientDistObservability，FR-217）。 */
function buildObservability(channelId: string, range: string) {
  const series = Array.from({ length: 3 }, (_, i) => ({
    ts: `2026-06-2${5 + i}T10:00:00Z`,
    manifestPulls: 120 + i * 10,
    artifactPulls: 35 + i * 5,
    downloadBytes: (120 + i * 10) * 70_000,
    casHit: 20 + i,
    casMiss: 15,
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
      casHit: 240,
      casMiss: 160,
      updateTotal: 360,
      updateSuccess: 330,
      updateFailStatic: 10,
      updateRolledBack: 12,
      updateError: 8,
      successRate: 0.9167,
      failStaticRate: 0.0278,
      rollbackRate: 0.0333,
      casHitRate: 0.6,
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

  // 发布版本：单调递增版本号、切 latest。
  domainRoute('post', '/client-channels/:channelId/versions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const channelId = String(info.params.channelId)
    const body = (await info.request.json()) as {
      files: import('@/api/clientVersions').ManifestFile[]
      managedDirs: string[]
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

  // 分发统计（FR-095）。
  domainRoute('get', '/client-dist/stats', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const channelId = url.searchParams.get('channelId') ?? ''
    const days = Number(url.searchParams.get('days') ?? 30)
    return HttpResponse.json(buildStats(channelId, days))
  }),

  // 分发观测（FR-217）：时序 + 分布 + 汇总。
  domainRoute('get', '/client-dist/observability', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const channelId = url.searchParams.get('channelId') ?? ''
    const range = url.searchParams.get('range') ?? '7d'
    return HttpResponse.json(buildObservability(channelId, range))
  }),

  // 内嵌更新器 jar 信息（FR-107 接入引导）。
  domainRoute('get', '/client-dist/updater-jars', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({
      version: '0.9.0',
      wedge: { available: true, size: 32_768 },
      core: { available: true, size: 1_048_576 },
    })
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
    value: '30',
    editable: true,
    sensitive: false,
    overridden: false,
    effectiveImmediately: false,
  },
  { id: 3, key: 'jdk.mirror', value: 'https://mirror.example.com', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  { id: 4, key: 'database.dsn', value: 'sqlite://****', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
  { id: 5, key: 'jwt.secret', value: '****', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
])
