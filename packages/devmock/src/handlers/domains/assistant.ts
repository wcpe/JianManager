import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'

/**
 * AI 助手配置域 mock（FR-514，见 ADR-105）。
 *
 * 复刻真后端的关键语义，而不是「对什么都答应」，否则 mock 模式下看不见缺陷：
 * - 管理面要求平台管理员（非管理员 403），与 `router/assistant.go` 的
 *   `requireAssistantAdmin` 同口径；
 * - 凭据**永不回显**——响应里只有 `hasCredential` 标志，没有明文字段；
 * - 删除仍有子模型的供应商回 409；
 * - 同一供应商下 modelId 重复回 409；
 * - 明文 http 地址回 422（对应后端受保护出站的准入）。
 */

interface MockProvider {
  id: number
  name: string
  protocol: string
  baseUrl: string
  credentialMode: string
  credentialEnv?: string
  hasCredential: boolean
  enabled: boolean
  version: number
  createdAt: string
  updatedAt: string
}

interface MockModel {
  id: number
  providerId: number
  modelId: string
  displayName: string
  streaming: boolean
  tools: boolean
  contextWindow: number | null
  maxOutputTokens: number | null
  enabled: boolean
  version: number
}

// 内存态：mock 进程内保持，足以支撑「建 → 读 → 改 → 删」的界面验证。
let providers: MockProvider[] = []
let models: MockModel[] = []
let defaultModelId: number | null = null
// 出站放行段与设置的乐观并发版本（与后端 assistant_settings 单行同构）。
let allowPrivateCidrs: string[] = []
let settingsVersion = 1
let nextProviderId = 1
let nextModelId = 1

const now = () => new Date().toISOString()

/** 平台管理员守卫：返回 null 表示放行，否则返回应直接回给客户端的响应。 */
function deniedFor(info: { request: Request }): Response | null {
  return requirePlatformAdmin(info)
}

/** 按请求体构造/更新供应商视图。 */
function providerView(body: Record<string, unknown>, id: number, version: number): MockProvider {
  const mode = String(body.credentialMode ?? 'none')
  const env = String(body.credentialEnv ?? '')
  const secret = String(body.credentialSecret ?? '')
  return {
    id,
    name: String(body.name ?? ''),
    protocol: String(body.protocol ?? 'openai-chat'),
    baseUrl: String(body.baseUrl ?? ''),
    credentialMode: mode,
    credentialEnv: mode === 'env' ? env : undefined,
    // 只有真的提供了凭据（或本来就有）才算已配置。
    hasCredential: mode === 'env' ? env !== '' : mode === 'stored' ? secret !== '' : false,
    enabled: body.enabled !== false,
    version,
    createdAt: now(),
    updatedAt: now(),
  }
}

/** 校验供应商入参，返回错误消息或 null。 */
function validateProvider(body: Record<string, unknown>): string | null {
  const protocol = String(body.protocol ?? '')
  if (!['openai-chat', 'openai-responses', 'anthropic'].includes(protocol)) {
    return `不支持的协议: ${protocol}`
  }
  const baseUrl = String(body.baseUrl ?? '')
  if (!baseUrl.startsWith('https://')) {
    return '仅支持 https 地址'
  }
  if (baseUrl.includes('?') || baseUrl.includes('#')) {
    return '地址不得带 query 或 fragment'
  }
  const mode = String(body.credentialMode ?? '')
  if (!['none', 'env', 'stored'].includes(mode)) {
    return `未知凭据模式: ${mode}`
  }
  if (mode === 'env' && !/^JIANMANAGER_ASSISTANT_[A-Z0-9_]{1,64}$/.test(String(body.credentialEnv ?? ''))) {
    return '环境变量名须形如 JIANMANAGER_ASSISTANT_XXX'
  }
  return null
}

/** 把模型补上供应商名与协议（列表需要，避免前端再拉一次供应商）。 */
function modelView(m: MockModel) {
  const p = providers.find((x) => x.id === m.providerId)
  return {
    ...m,
    providerName: p?.name ?? '',
    protocol: p?.protocol ?? '',
    providerEnabled: p?.enabled ?? false,
  }
}

export const handlers = [
  // 可用模型（所有登录用户，不含端点与凭据）
  domainRoute('get', '/assistant/models', () => {
    const items = models
      .filter((m) => m.enabled && providers.find((p) => p.id === m.providerId)?.enabled)
      .map((m) => {
        const p = providers.find((x) => x.id === m.providerId)
        return {
          id: m.id,
          modelId: m.modelId,
          displayName: m.displayName,
          protocol: p?.protocol ?? '',
          streaming: m.streaming,
          tools: m.tools,
        }
      })
    const visibleDefault = items.some((x) => x.id === defaultModelId) ? defaultModelId : null
    return HttpResponse.json({ items, defaultModelId: visibleDefault })
  }),

  // ── 管理面 ────────────────────────────────────────────────
  domainRoute('get', '/assistant/admin/providers', ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    return HttpResponse.json({ items: providers })
  }),

  domainRoute('post', '/assistant/admin/providers', async ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const body = (await request.json()) as Record<string, unknown>
    const invalid = validateProvider(body)
    if (invalid) return HttpResponse.json({ error: 'INVALID_CONFIG', message: invalid }, { status: 422 })
    if (providers.some((p) => p.name === String(body.name ?? ''))) {
      return HttpResponse.json({ error: 'CONFLICT', message: '名称已存在' }, { status: 409 })
    }
    const created = providerView(body, nextProviderId++, 1)
    providers.push(created)
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/assistant/admin/providers/:id', async ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    const idx = providers.findIndex((p) => p.id === id)
    if (idx < 0) return HttpResponse.json({ error: 'NOT_FOUND' }, { status: 404 })
    const body = (await request.json()) as Record<string, unknown>
    const invalid = validateProvider(body)
    if (invalid) return HttpResponse.json({ error: 'INVALID_CONFIG', message: invalid }, { status: 422 })
    const prev = providers[idx]
    if (body.version !== undefined && Number(body.version) !== prev.version) {
      return HttpResponse.json({ error: 'CONFLICT', message: '数据已被他人修改' }, { status: 409 })
    }
    const updated = providerView(body, id, prev.version + 1)
    // 未提供新密钥时保留「已配置」状态（凭据三态里的「保留」）。
    if (updated.credentialMode === 'stored' && !updated.hasCredential && prev.hasCredential) {
      updated.hasCredential = true
    }
    providers[idx] = updated
    return HttpResponse.json(updated)
  }),

  domainRoute('delete', '/assistant/admin/providers/:id', ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    if (models.some((m) => m.providerId === id)) {
      return HttpResponse.json({ error: 'IN_USE', message: '该供应商下还有模型' }, { status: 409 })
    }
    const before = providers.length
    providers = providers.filter((p) => p.id !== id)
    if (providers.length === before) return HttpResponse.json({ error: 'NOT_FOUND' }, { status: 404 })
    return HttpResponse.json({ message: '已删除' })
  }),

  domainRoute('get', '/assistant/admin/models', ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    return HttpResponse.json({ items: models.map(modelView) })
  }),

  domainRoute('post', '/assistant/admin/models', async ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const body = (await request.json()) as Record<string, unknown>
    const providerId = Number(body.providerId ?? 0)
    if (!providers.some((p) => p.id === providerId)) {
      return HttpResponse.json({ error: 'INVALID_CONFIG', message: '供应商不存在' }, { status: 422 })
    }
    const modelId = String(body.modelId ?? '').trim()
    if (!modelId) {
      return HttpResponse.json({ error: 'INVALID_CONFIG', message: '模型标识必填' }, { status: 422 })
    }
    if (models.some((m) => m.providerId === providerId && m.modelId === modelId)) {
      return HttpResponse.json({ error: 'CONFLICT', message: '同供应商下模型标识重复' }, { status: 409 })
    }
    const created: MockModel = {
      id: nextModelId++,
      providerId,
      modelId,
      displayName: String(body.displayName ?? ''),
      streaming: body.streaming !== false,
      tools: body.tools === true,
      contextWindow: body.contextWindow == null ? null : Number(body.contextWindow),
      maxOutputTokens: body.maxOutputTokens == null ? null : Number(body.maxOutputTokens),
      enabled: body.enabled !== false,
      version: 1,
    }
    models.push(created)
    return HttpResponse.json(modelView(created), { status: 201 })
  }),

  domainRoute('put', '/assistant/admin/models/:id', async ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    const idx = models.findIndex((m) => m.id === id)
    if (idx < 0) return HttpResponse.json({ error: 'NOT_FOUND' }, { status: 404 })
    const body = (await request.json()) as Record<string, unknown>
    const prev = models[idx]
    if (body.version !== undefined && Number(body.version) !== prev.version) {
      return HttpResponse.json({ error: 'CONFLICT', message: '数据已被他人修改' }, { status: 409 })
    }
    models[idx] = {
      ...prev,
      providerId: Number(body.providerId ?? prev.providerId),
      modelId: String(body.modelId ?? prev.modelId),
      displayName: String(body.displayName ?? ''),
      streaming: body.streaming !== false,
      tools: body.tools === true,
      contextWindow: body.contextWindow == null ? null : Number(body.contextWindow),
      maxOutputTokens: body.maxOutputTokens == null ? null : Number(body.maxOutputTokens),
      enabled: body.enabled !== false,
      version: prev.version + 1,
    }
    return HttpResponse.json(modelView(models[idx]))
  }),

  domainRoute('delete', '/assistant/admin/models/:id', ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    const before = models.length
    models = models.filter((m) => m.id !== id)
    if (models.length === before) return HttpResponse.json({ error: 'NOT_FOUND' }, { status: 404 })
    // 删掉默认模型时一并清空默认指针（与后端同语义）。
    if (defaultModelId === id) defaultModelId = null
    return HttpResponse.json({ message: '已删除' })
  }),

  // 助手全局设置（默认模型 + 出站放行段）按**单例资源**整体读写，
  // 与后端 GET/PUT /assistant/admin/settings 同构。
  domainRoute('get', '/assistant/admin/settings', ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    return HttpResponse.json({
      defaultModelId,
      allowPrivateCidrs: allowPrivateCidrs,
      version: settingsVersion,
      // mock 环境按 dev 处理（后端 dev_mode 才允许 http 明文）。
      insecureAllowed: true,
    })
  }),

  domainRoute('put', '/assistant/admin/settings', async ({ request }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const body = (await request.json()) as {
      defaultModelId?: number | null
      allowPrivateCidrs?: string[]
      version?: number
    }
    // 与后端一致的 version 乐观并发。
    if (body.version !== undefined && body.version !== 0 && body.version !== settingsVersion) {
      return HttpResponse.json({ error: 'CONFLICT', message: '数据已被他人修改，请刷新后重试' }, { status: 409 })
    }
    const nextDefault = body.defaultModelId ?? null
    if (nextDefault !== null) {
      const target = models.find((m) => m.id === nextDefault)
      if (!target) return HttpResponse.json({ error: 'INVALID_CONFIG', message: '模型不存在' }, { status: 422 })
      if (!target.enabled) {
        return HttpResponse.json({ error: 'INVALID_CONFIG', message: '不能把已停用的模型设为默认' }, { status: 422 })
      }
      const prov = providers.find((p) => p.id === target.providerId)
      if (!prov?.enabled) {
        return HttpResponse.json({ error: 'INVALID_CONFIG', message: '该模型的供应商已停用' }, { status: 422 })
      }
    }
    // CIDR 逐项校验：非法项整单拒绝并点名（与后端同语义）。
    const nextCidrs: string[] = []
    for (const raw of body.allowPrivateCidrs ?? []) {
      const item = String(raw).trim()
      if (item === '') continue
      if (!/^[0-9a-fA-F.:]+\/\d{1,3}$/.test(item)) {
        return HttpResponse.json(
          { error: 'INVALID_CONFIG', message: `参数不合规: 不是合法的 CIDR「${item}」（示例：10.0.0.0/8 或 192.168.1.0/24）` },
          { status: 422 },
        )
      }
      if (!nextCidrs.includes(item)) nextCidrs.push(item)
    }
    // 整体替换语义：未提供的字段等于置空。
    defaultModelId = nextDefault
    allowPrivateCidrs = nextCidrs
    settingsVersion += 1
    return HttpResponse.json({
      defaultModelId,
      allowPrivateCidrs,
      version: settingsVersion,
      insecureAllowed: true,
    })
  }),

  // 探测端点：**上游失败仍是 200 + ok:false**（与后端一致的结论语义）。
  // mock 里没有真实上游，故固定返回一条「看起来像上游拒绝」的结论，
  // 让前端的结果块（分类码 + 处置建议）能被真实渲染出来。
  domainRoute('post', '/assistant/admin/providers/:id/test', ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    if (!providers.some((p) => p.id === id)) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '对象不存在' }, { status: 404 })
    }
    const hasModel = models.some((m) => m.providerId === id)
    if (!hasModel) {
      return HttpResponse.json(
        { error: 'INVALID_CONFIG', message: '参数不合规: 该供应商下还没有模型，无法探测（请先添加一个模型）' },
        { status: 422 },
      )
    }
    return HttpResponse.json({ ok: true, latencyMs: 412 })
  }),

  domainRoute('post', '/assistant/admin/models/:id/test', ({ request, params }) => {
    const denied = deniedFor({ request })
    if (denied) return denied
    const id = Number(params.id)
    const target = models.find((m) => m.id === id)
    if (!target) return HttpResponse.json({ error: 'NOT_FOUND', message: '对象不存在' }, { status: 404 })
    const withTools = target.tools
    if (withTools) {
      return HttpResponse.json({
        ok: true,
        latencyMs: 830,
        textPreview: 'pong',
        promptTokens: 9,
        outputTokens: 2,
        toolCallParsed: true,
      })
    }
    return HttpResponse.json({
      ok: true,
      latencyMs: 620,
      textPreview: 'pong',
      promptTokens: 9,
      outputTokens: 2,
      toolCallParsed: false,
    })
  }),
]
