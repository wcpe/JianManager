import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'

/**
 * 节点级日志采集域 mock handler（FR-503 节点端 + FR-504 手动导入归档）。
 *
 * 覆盖 api 模块：`api/nodes.ts` 的节点端 3 条（GET/PUT/DELETE
 * `/nodes/:id/settings/log-acquisition`）+ `api/logRuntime.ts` 的
 * `POST /nodes/:id/log-runtime/ingest/import-archives`。
 *
 * **为什么必须单独补这个域**：平台端 seed 在 client.ts（`GET/PUT /settings` 能读写那 5 个
 * `log.acq.*` 行），但节点详情「日志采集」分段走的是**另一组端点**。mock 模式下未实现的端点
 * 确定性失败（vite.config.ts 把未命中请求 bypass 到 127.0.0.1:9 ⇒ ECONNREFUSED），
 * 该分段会恒为「加载日志采集配置失败」——`pnpm dev:mock` 与 e2e 里主交付界面直接不可用。
 *
 * 语义与真后端对齐（不是"能返回 200"）：
 * - 四列 `default / platform / effective / override` + `overridden` 与
 *   `service.LogAcqSettingItem` 同构；`effective = override ?? platform`；
 * - platform 列取自 `settings` 集合的 `log.acq.*` 行（设置页保存后节点视图随即变化），
 *   缺行时回落到内置基线；
 * - 写入校验同口径：只接受 5 个平台键、布尔键限 `true|false`、周期须为正 duration、
 *   路径覆盖须为「值为字符串的 JSON 对象」——否则 422（真后端 validateSettingValue）；
 * - 写仅平台管理员（真后端 UpdateNode/DeleteNode 走 requirePlatformAdmin）。
 */

/** 与后端 `LogAcqPlatformKeys()` 同序（前端展示顺序即此顺序）。 */
const PLATFORM_KEYS = [
  'log.acq.rules_enabled',
  'log.acq.realtime_tail',
  'log.acq.gz_import_enabled',
  'log.acq.gz_scan_interval',
  'log.acq.path_overrides',
] as const

/** 内置基线（等价 worker.yml 基线，同后端 `defaultValue` 口径）。 */
const BUILTIN_DEFAULTS: Record<string, string> = {
  'log.acq.rules_enabled': 'true',
  'log.acq.realtime_tail': 'true',
  'log.acq.gz_import_enabled': 'false',
  'log.acq.gz_scan_interval': '',
  'log.acq.path_overrides': '',
}

/** 布尔型键（同后端 validateSettingValue 的三项校验）。 */
const BOOLEAN_KEYS = new Set([
  'log.acq.rules_enabled',
  'log.acq.realtime_tail',
  'log.acq.gz_import_enabled',
])

/** settings 集合中我们只关心这三个字段（避免跨域强耦合 client.ts 的 Mock 类型）。 */
interface SettingsRowLike {
  id: number
  key: string
  value: string
}

/** 节点级覆盖行：真后端存在 platform_settings 的 `@node:<uuid>:<key>` 行，mock 单独一集合。 */
interface NodeLogAcqOverride {
  id: string
  nodeUuid: string
  key: string
  value: string
}

const overrides = db<NodeLogAcqOverride>('node-log-acq-overrides', () => [])

/** 节点种子行的最小子集（只为校验 :id 存在并取 uuid/name，不复制整个 NodeInfo）。 */
interface NodeRef {
  id: number
  uuid: string
  name: string
}

/** 解析 :id 为节点；不存在时返回响应（404），调用方直接 return。 */
function resolveNode(id: string): { ok: true; node: NodeRef } | { ok: false; response: Response } {
  const numeric = Number(id)
  // 惰性取集合：本模块与其他域簇的导入顺序不确定，不能在模块顶层抓 settings/nodes。
  const node = db<NodeRef>('nodes').find((n) => n.id === numeric)
  if (!node) {
    return {
      ok: false,
      response: HttpResponse.json({ error: 'NOT_FOUND', message: '节点不存在' }, { status: 404 }),
    }
  }
  return { ok: true, node }
}

/** 平台列：settings 集合中的 `log.acq.*` 行（平台覆盖）> 内置基线。 */
function platformValue(key: string): string {
  const row = db<SettingsRowLike>('settings').find((r) => r.key === key)
  return row?.value ?? BUILTIN_DEFAULTS[key] ?? ''
}

/** 组装节点对照视图（四列 + overridden），同 `service.LogAcqSettingsForNode` 的字段名。 */
function nodeAcqView(node: NodeRef) {
  return {
    nodeUuid: node.uuid,
    nodeName: node.name,
    items: PLATFORM_KEYS.map((key) => {
      const override = overrides.find((o) => o.nodeUuid === node.uuid && o.key === key)
      const platform = platformValue(key)
      return {
        key,
        default: BUILTIN_DEFAULTS[key] ?? '',
        platform,
        effective: override ? override.value : platform,
        override: override ? override.value : '',
        overridden: !!override,
      }
    }),
  }
}

/** Go duration 文本（前端/后端同一条正则：只认正数，零值另判）。 */
const DURATION_RE = /^([0-9]+(\.[0-9]+)?(ns|us|µs|μs|ms|s|m|h))+$/

/**
 * 按键校验值，返回错误消息或 null（合法）。规则与后端 `validateSettingValue`
 * 及前端 `settings-form.ts` 同口径——三处漂移时 mock 会放行真后端拒绝的值。
 */
function validateValue(key: string, value: string): string | null {
  const v = value.trim()
  if (BOOLEAN_KEYS.has(key)) {
    return v === 'true' || v === 'false' ? null : '日志采集开关须为 true|false'
  }
  if (key === 'log.acq.gz_scan_interval') {
    // 空 = 未配置（回落 worker.yml 基线）；非空须为正 duration（写 0 会被拒）。
    if (v === '') return null
    if (!DURATION_RE.test(v) || !/[1-9]/.test(v.replace(/ns|us|µs|μs|ms|s|m|h/g, ' '))) {
      return 'gz 扫描周期须留空（未配置）或为正的 Go duration（如 1h）'
    }
    return null
  }
  if (key === 'log.acq.path_overrides') {
    if (v === '') return null
    try {
      const parsed = JSON.parse(v) as unknown
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return '路径覆盖须为 JSON 对象文本（形如 {"inst:1":"proxy.log.0"}）'
      }
      // 值必须是字符串（后端反序列化到 map[string]string，值非字符串即 422）。
      for (const val of Object.values(parsed as Record<string, unknown>)) {
        if (typeof val !== 'string') {
          return '路径覆盖须为 JSON 对象文本（值为字符串）'
        }
      }
      return null
    } catch {
      return '路径覆盖须为 JSON 对象文本（形如 {"inst:1":"proxy.log.0"}）'
    }
  }
  return null
}

/** 写入覆盖（已存在的键更新，不重复插入）。 */
function putOverride(nodeUuid: string, key: string, value: string): void {
  const id = `${nodeUuid}:${key}`
  if (overrides.get(id)) overrides.update(id, { value })
  else overrides.insert({ id, nodeUuid, key, value })
}

export const handlers = [
  // ── 节点日志采集对照视图（FR-503）────────────────────────────────────────
  domainRoute('get', '/nodes/:id/settings/log-acquisition', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const resolved = resolveNode(info.params.id as string)
    if (!resolved.ok) return resolved.response
    return HttpResponse.json(nodeAcqView(resolved.node))
  }),

  // 写节点覆盖（真后端仅平台管理员）：只接受 5 个平台键，逐键做值校验。
  domainRoute('put', '/nodes/:id/settings/log-acquisition', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const resolved = resolveNode(info.params.id as string)
    if (!resolved.ok) return resolved.response
    let body: { values?: Record<string, string> }
    try {
      body = (await info.request.json()) as { values?: Record<string, string> }
    } catch {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '请求参数错误' }, { status: 400 })
    }
    const values = body.values
    if (!values || typeof values !== 'object') {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '请求参数错误' }, { status: 400 })
    }
    // 先整体校验再落库：真后端对越界/非法值整体拒绝，不做部分写入（否则 mock 会
    // 造出「一半生效」的界面态，与真机不符）。
    for (const [key, value] of Object.entries(values)) {
      if (!PLATFORM_KEYS.includes(key as (typeof PLATFORM_KEYS)[number])) {
        return HttpResponse.json(
          { error: 'BUSINESS_ERROR', message: `不支持的日志采集配置项: ${key}` },
          { status: 422 },
        )
      }
      const invalid = validateValue(key, value)
      if (invalid) {
        return HttpResponse.json({ error: 'BUSINESS_ERROR', message: invalid }, { status: 422 })
      }
    }
    for (const [key, value] of Object.entries(values)) {
      putOverride(resolved.node.uuid, key, value)
    }
    return HttpResponse.json(nodeAcqView(resolved.node))
  }),

  // 清除节点覆盖（`?key=` 只清一项；缺省清全部 5 项）。删除幂等。
  domainRoute('delete', '/nodes/:id/settings/log-acquisition', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const resolved = resolveNode(info.params.id as string)
    if (!resolved.ok) return resolved.response
    const only = new URL(info.request.url).searchParams.get('key')?.trim() ?? ''
    if (only && !PLATFORM_KEYS.includes(only as (typeof PLATFORM_KEYS)[number])) {
      return HttpResponse.json(
        { error: 'BUSINESS_ERROR', message: `不支持的日志采集配置项: ${only}` },
        { status: 422 },
      )
    }
    const keys = only ? [only] : [...PLATFORM_KEYS]
    for (const key of keys) overrides.remove(`${resolved.node.uuid}:${key}`)
    return HttpResponse.json(nodeAcqView(resolved.node))
  }),

  // ── 手动导入归档（.gz，FR-504）────────────────────────────────────────────
  // 理由必填（审计凭据）；空 storageNamespace = 该节点全部源 ⇒ 仅平台管理员。
  // 与真后端一致：**不返回计数**（导入按 WAL 余量分批、可能跨多个采集轮），
  // 只回报「已触发且无错」，计数以 Worker 日志为准——避免面板做出"已完成"的假承诺。
  domainRoute('post', '/nodes/:id/log-runtime/ingest/import-archives', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const resolved = resolveNode(info.params.id as string)
    if (!resolved.ok) return resolved.response
    let body: { storageNamespace?: string; reason?: string }
    try {
      body = (await info.request.json()) as { storageNamespace?: string; reason?: string }
    } catch {
      return HttpResponse.json({ error: 'INVALID_REQUEST' }, { status: 400 })
    }
    const reason = (body.reason ?? '').trim()
    if (reason === '') {
      return HttpResponse.json(
        {
          error: 'REASON_REQUIRED',
          message: '手动导入归档必须给出理由（审计凭据需要记录为何可以导入）',
        },
        { status: 400 },
      )
    }
    const namespace = (body.storageNamespace ?? '').trim()
    if (namespace === '') {
      // 全节点范围导入仅平台管理员（与 authorizeImportAllSources 同判据）。
      const forbidden = requirePlatformAdmin(info)
      if (forbidden) return forbidden
    } else if (!/^inst:[0-9]+$/.test(namespace)) {
      return HttpResponse.json(
        {
          error: 'INVALID_REQUEST',
          message: '须提供实例存储命名空间（形如 inst:317）；留空表示本节点全部源',
        },
        { status: 400 },
      )
    }
    return HttpResponse.json({
      state: 'imported',
      message:
        '已触发归档导入扫描；导入计数见 Worker 日志。' +
        '批量导入按 WAL 余量分批，可能跨多个采集轮完成。',
    })
  }),
]
