import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'

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

const now = () => new Date().toISOString()
/** SSE 输出用（与 bots.ts 同做法）。 */
const encoder = new TextEncoder()

// ── 对话运行时的内存态（FR-519/520 界面验证用） ──
interface MockConversation {
  id: number
  title: string
  selectedModelId: number | null
  createdAt: string
  updatedAt: string
}
interface MockMessage {
  id: number
  conversationId: number
  role: 'user' | 'assistant' | 'tool' | string
  content: string
  createdAt: string
}
interface MockEvent {
  seq: number
  type: string
  data: unknown
  createdAt: string
}

let conversations: MockConversation[] = [
  // 预置历史会话：抽屉的「加载更多」要真的有第二页才验证得到（每页 20 条）。
  //
  // id 从 9001 起而不是 1：mock 里多个域各自从 1 开始分配 id，而列表 key 用的是裸 id；
  // 从 1 起会让「历史会话 4」与首页「需要关注」的第 4 条在同一个父级下撞 key
  // （实测报 "Encountered two children with the same key, 4"，且与助手无关的页面也受影响）。
  ...Array.from({ length: 23 }, (_, i) => {
    const n = 23 - i
    return {
      id: 9000 + n,
      title: n === 23 ? '为什么 server-0009 反复退出？' : `历史会话 ${n}`,
      selectedModelId: null,
      createdAt: new Date(Date.now() - n * 3600_000).toISOString(),
      updatedAt: new Date(Date.now() - n * 3600_000).toISOString(),
    }
  }),
]
const messages = new Map<number, MockMessage[]>()
// 首条种子会话带一轮真实问答：否则刷新后「选中会话但列表空」看起来像不回显历史，
// 验证不到 effectiveConversationId → messages 这条链路。
messages.set(9001, [
  {
    id: 1,
    conversationId: 9001,
    role: 'user',
    content: '为什么 server-0009 反复退出？',
    createdAt: new Date(Date.now() - 26 * 3600_000).toISOString(),
  },
  {
    id: 2,
    conversationId: 9001,
    role: 'assistant',
    content: '（mock 历史消息）server-0009 在最近 6 小时内退出 4 次，最后一次的退出码是 1。',
    createdAt: new Date(Date.now() - 25.9 * 3600_000).toISOString(),
  },
])
const runEvents = new Map<number, MockEvent[]>()
const runStatuses = new Map<number, string>()
/** 幂等键 → 已受理的 run（同键同内容复用）。 */
const idemIndex = new Map<string, { runId: number; text: string }>()
let nextConversationId = 9100
let nextMessageId = 3
let nextRunId = 1

// 内存态：mock 进程内保持，足以支撑「建 → 读 → 改 → 删」的界面验证。
let providers: MockProvider[] = [
  // 种子一条可用供应商 + 模型：否则对话面板整块被「尚未配置可用模型」挡住，
  // 提交 → SSE → 终态这条链路在 mock 模式下根本无法验证。
  {
    id: 1,
    name: 'Mock 供应商',
    protocol: 'openai-chat',
    baseUrl: 'https://api.example.com/v1',
    credentialMode: 'env',
    credentialEnv: 'MOCK_API_KEY',
    hasCredential: true,
    enabled: true,
    version: 1,
    createdAt: now(),
    updatedAt: now(),
  },
]
let models: MockModel[] = [
  {
    id: 1,
    providerId: 1,
    modelId: 'mock-chat-1',
    displayName: 'Mock Chat 1',
    streaming: true,
    tools: false,
    contextWindow: 128000,
    maxOutputTokens: 4096,
    enabled: true,
    version: 1,
  },
]
let defaultModelId: number | null = 1
// 出站放行段与设置的乐观并发版本（与后端 assistant_settings 单行同构）。
let allowPrivateCidrs: string[] = []
let settingsVersion = 1
let nextProviderId = 1
let nextModelId = 1


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
          providerId: m.providerId,
          // 后端会回填供应商名（service/assistant.go:373）：前端据此告知「数据发给谁」，
          // 缺了它界面上只剩 modelId，用户无法判断去向。
          providerName: p?.name ?? '',
          modelId: m.modelId,
          displayName: m.displayName,
          protocol: p?.protocol ?? '',
          streaming: m.streaming,
          tools: m.tools,
          contextWindow: m.contextWindow,
          maxOutputTokens: m.maxOutputTokens,
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

  // ── 对话运行时（FR-519/520 消费；后端契约见 router/assistant_chat.go） ──
  //
  // 复刻三条关键语义，避免 mock 下验证不到真实行为：
  //  - 越权一律 404（后端有意不用 403，前端不得据此提示「无权限」）；
  //  - 提交一轮回 **202** + runId，文本后续走 SSE 增量；
  //  - Idempotency-Key 同键同内容回原 run、同键异内容 409。
  domainRoute('get', '/assistant/conversations', ({ request }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const url = new URL(request.url)
    const limit = Math.min(Number(url.searchParams.get('limit') ?? 20) || 20, 100)
    const cursor = url.searchParams.get('cursor') ?? ''
    // 按 updatedAt 倒序，再用游标切页（游标是上一页最后一条的 id）。
    const sorted = [...conversations].sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1))
    let start = 0
    if (cursor) {
      const at = sorted.findIndex((c) => c.id === Number(cursor))
      start = at >= 0 ? at + 1 : 0
    }
    const items = sorted.slice(start, start + limit)
    const hasMore = start + limit < sorted.length
    return HttpResponse.json({ items, nextCursor: hasMore ? String(items[items.length - 1]?.id ?? '') : '' })
  }),

  domainRoute('post', '/assistant/conversations', async ({ request }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const body = (await request.json().catch(() => ({}))) as {
      title?: string
      selectedModelId?: number | null
    }
    const row: MockConversation = {
      id: nextConversationId++,
      title: (body.title ?? '').trim() || '新会话',
      selectedModelId: body.selectedModelId ?? null,
      createdAt: now(),
      updatedAt: now(),
    }
    conversations.unshift(row)
    return HttpResponse.json(row, { status: 201 })
  }),

  domainRoute('get', '/assistant/conversations/:id', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const row = conversations.find((c) => c.id === Number(params.id))
    if (!row) return HttpResponse.json({ error: 'CONVERSATION_NOT_FOUND', message: '会话不存在' }, { status: 404 })
    return HttpResponse.json(row)
  }),

  domainRoute('patch', '/assistant/conversations/:id', async ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const row = conversations.find((c) => c.id === Number(params.id))
    if (!row) return HttpResponse.json({ error: 'CONVERSATION_NOT_FOUND', message: '会话不存在' }, { status: 404 })
    const body = (await request.json().catch(() => ({}))) as { title?: string }
    if (typeof body.title === 'string') row.title = body.title.trim() || row.title
    row.updatedAt = now()
    return HttpResponse.json(row)
  }),

  domainRoute('delete', '/assistant/conversations/:id', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const id = Number(params.id)
    const at = conversations.findIndex((c) => c.id === id)
    if (at < 0) return HttpResponse.json({ error: 'CONVERSATION_NOT_FOUND', message: '会话不存在' }, { status: 404 })
    conversations.splice(at, 1)
    messages.delete(id)
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('get', '/assistant/conversations/:id/messages', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const id = Number(params.id)
    if (!conversations.some((c) => c.id === id)) {
      return HttpResponse.json({ error: 'CONVERSATION_NOT_FOUND', message: '会话不存在' }, { status: 404 })
    }
    return HttpResponse.json({ items: messages.get(id) ?? [] })
  }),

  /**
   * 提交一轮：回 202 + runId，并把这一轮的脚本事件挂到 runEvents。
   *
   * 回复内容是**固定的脚本**而不是模型输出——mock 的职责是让界面链路（提交 → 流 →
   * run_end）可验证，不是假装有模型。首条回复会说明这是 mock。
   */
  domainRoute('post', '/assistant/conversations/:id/runs', async ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const convId = Number(params.id)
    const conv = conversations.find((c) => c.id === convId)
    if (!conv) return HttpResponse.json({ error: 'CONVERSATION_NOT_FOUND', message: '会话不存在' }, { status: 404 })

    const body = (await request.json().catch(() => ({}))) as {
      text?: string
      contextSnapshot?: string | null
    }
    const text = (body.text ?? '').trim()
    if (text === '') return HttpResponse.json({ error: 'INVALID_REQUEST', message: '正文不能为空' }, { status: 400 })

    const idem = request.headers.get('Idempotency-Key') ?? ''
    // 幂等：同键同内容回原 run；同键异内容 409（与后端同语义）。
    const prev = idem ? idemIndex.get(idem) : undefined
    if (prev) {
      if (prev.text !== text) {
        return HttpResponse.json({ error: 'IDEMPOTENCY_CONFLICT', message: '同一幂等键用于不同内容' }, { status: 409 })
      }
      return HttpResponse.json(
        { runId: prev.runId, conversationId: convId, status: runStatuses.get(prev.runId) ?? 'running' },
        { status: 202 },
      )
    }

    const runId = nextRunId++
    if (idem) idemIndex.set(idem, { runId, text })

    // 用户消息立即落库（历史回看能看到自己的提问）。
    const list = messages.get(convId) ?? []
    list.push({
      id: nextMessageId++,
      conversationId: convId,
      role: 'user',
      content: text,
      createdAt: now(),
    })
    messages.set(convId, list)

    // 会话标题跟随首轮提问（与后端「首轮自动命名」同观感）。
    if (conv.title === '新会话') conv.title = text.slice(0, 40)
    conv.updatedAt = now()

    // 预置这一轮的脚本事件：三段增量 + usage + run_end。
    const reply =
      `（mock 回复，非真实模型）收到你的问题：「${text.slice(0, 60)}」。` +
      '当前处于 devmock 模式，未接入真实供应商；这里仅用于验证提交、流式渲染与终态收尾。'
    const chunks = [reply.slice(0, 24), reply.slice(24, 60), reply.slice(60)]
    const eventList: MockEvent[] = []
    let seq = 1
    for (const chunk of chunks) {
      if (chunk === '') continue
      eventList.push({ seq: seq++, type: 'text_delta', data: { text: chunk }, createdAt: now() })
    }
    eventList.push({ seq: seq++, type: 'usage', data: { promptTokens: 12, outputTokens: 48 }, createdAt: now() })
    eventList.push({ seq: seq++, type: 'run_end', data: { status: 'completed' }, createdAt: now() })
    runEvents.set(runId, eventList)
    runStatuses.set(runId, 'completed')

    // 助手消息落库（SSE 结束后历史回读即为这份内容）。
    list.push({
      id: nextMessageId++,
      conversationId: convId,
      role: 'assistant',
      content: reply,
      createdAt: now(),
    })

    return HttpResponse.json({ runId, conversationId: convId, status: 'queued' }, { status: 202 })
  }),

  /** 事件补读：裸数组（与后端一致）。 */
  domainRoute('get', '/assistant/runs/:runId/events', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const runId = Number(params.runId)
    const url = new URL(request.url)
    const after = Number(url.searchParams.get('after') ?? 0) || 0
    const all = runEvents.get(runId)
    if (!all) return HttpResponse.json({ error: 'RUN_NOT_FOUND', message: '运行不存在' }, { status: 404 })
    return HttpResponse.json(all.filter((e) => e.seq > after))
  }),

  /**
   * 事件流（SSE）：每帧是一个**自带 type 的 JSON**，前端只看 data。
   *
   * 刻意按 ~120ms 逐帧推送而不是一次 flush：界面的增量渲染逻辑（以及骨架屏到正文的
   * 过渡）只有真的分多帧才验证得到。
   */
  domainRoute('get', '/assistant/runs/:runId/stream', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const runId = Number(params.runId)
    const url = new URL(request.url)
    const after = Number(url.searchParams.get('after') ?? 0) || 0
    const all = (runEvents.get(runId) ?? []).filter((e) => e.seq > after)
    if (!runEvents.has(runId)) {
      return HttpResponse.json({ error: 'RUN_NOT_FOUND', message: '运行不存在' }, { status: 404 })
    }

    let closed = false
    let timer: ReturnType<typeof setTimeout> | null = null
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        let i = 0
        const pump = () => {
          if (closed) return
          if (i >= all.length) {
            closed = true
            controller.close()
            return
          }
          const frame = all[i++]
          controller.enqueue(encoder.encode(`data: ${JSON.stringify(frame)}\n\n`))
          timer = setTimeout(pump, 120)
        }
        // 首帧立即发：让 UI 不必等一个定时周期才出现第一个字。
        pump()
      },
      cancel() {
        closed = true
        if (timer) clearTimeout(timer)
      },
    })
    return new HttpResponse(stream, {
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' },
    })
  }),

  domainRoute('post', '/assistant/runs/:runId/cancel', ({ request, params }) => {
    const denied = requireAuth({ request })
    if (denied) return denied
    const runId = Number(params.runId)
    if (!runEvents.has(runId)) {
      return HttpResponse.json({ error: 'RUN_NOT_FOUND', message: '运行不存在' }, { status: 404 })
    }
    runStatuses.set(runId, 'cancelled')
    return HttpResponse.json({ ok: true })
  }),
]
