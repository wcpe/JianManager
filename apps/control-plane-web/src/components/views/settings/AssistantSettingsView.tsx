import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2, Star, PlugZap, RefreshCw } from 'lucide-react'
import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  FieldLabel,
  Input,
  NativeSelect,
  Panel,
  Switch,
  Table,
  Textarea,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { MCPConnectionsPanel } from '@/components/views/settings/MCPConnectionsPanel'
import { AssistantSkillsPanel } from '@/components/views/settings/AssistantSkillsPanel'
import type { DiscoverModelsResult } from '@/api/assistant'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import {
  ASSISTANT_CREDENTIAL_MODES,
  ASSISTANT_PROTOCOLS,
  credentialModeLabelKey,
  protocolLabelKey,
} from '@/api/assistant'
import type {
  AssistantAvailableModel,
  AssistantAdminSettings,
  AssistantModelTestResult,
  AssistantProbeResult,
  AssistantModel,
  AssistantProvider,
  ModelPayload,
  ProviderPayload,
} from '@/api/assistant'

/**
 * AI 助手配置视图（FR-514）。
 *
 * 受控边界（ADR-102 b 范式）：**不取数、不发请求、不弹 toast**。
 * 数据与加载/错误态经 props 注入；写操作以回调上报，由容器执行 mutation 与文案。
 *
 * 凭据三态在这里的落点：表单里「凭据模式」决定显示哪个输入框，
 * 编辑已配置的 stored 供应商时密钥框留空即表示保留——视图不读也拿不到原值。
 */
export interface AssistantSettingsViewProps {
  isPlatformAdmin: boolean
  providers?: AssistantProvider[]
  models?: AssistantModel[]
  availableModels?: AssistantAvailableModel[]
  defaultModelId?: number | null
  isLoading?: boolean
  isError?: boolean
  saving?: boolean
  onCreateProvider: (payload: ProviderPayload) => Promise<boolean>
  onUpdateProvider: (id: number, payload: ProviderPayload) => Promise<boolean>
  onDeleteProvider: (id: number) => Promise<boolean>
  onCreateModel: (payload: ModelPayload) => Promise<boolean>
  onUpdateModel: (id: number, payload: ModelPayload) => Promise<boolean>
  onDeleteModel: (id: number) => Promise<boolean>
  onSetDefaultModel: (modelId: number | null) => Promise<boolean>
  /** 显式探测供应商连通性（按钮触发，绝不自动调用）。 */
  onTestProvider: (id: number) => Promise<void>
  /** 用指定模型跑一次最小补全；withTools 时额外验证工具参数完整性。 */
  onTestModel: (id: number, withTools: boolean) => Promise<void>
  /**
   * 最近一次探测结果（key = `provider:<id>` / `model:<id>`）。
   *
   * 结果放行内而不是只弹 toast：分类建议是**要照着做**的内容
   * （换密钥 / 稍后重试 / 配 NO_PROXY），塞进 3 秒消失的提示等于没给。
   */
  probeResults?: Record<string, AssistantProbeResult | AssistantModelTestResult>
  /** 助手全局设置（默认模型 + 出站放行段）。 */
  settings?: AssistantAdminSettings
  settingsSaving?: boolean
  /** 保存放行段（后端整体替换，其余字段由容器补齐）；返回是否成功。 */
  onSaveEgress: (cidrs: string[]) => Promise<boolean>
  /** 保存自定义系统提示词（FR-541）。 */
  onSaveSystemPrompt?: (prompt: string) => Promise<boolean>
  /** 切换平台自带工具开关（FR-540）。 */
  onToggleBuiltinTools?: (enabled: boolean) => Promise<boolean>
  /** 从上游拉取模型候选（FR-538）。 */
  onDiscoverModels?: (providerId: number) => Promise<DiscoverModelsResult | null>
}

/** 供应商表单草稿。 */
interface ProviderDraft {
  id: number | null
  name: string
  protocol: string
  baseUrl: string
  enabled: boolean
  credentialMode: 'none' | 'env' | 'stored'
  credentialEnv: string
  credentialSecret: string
  clearCredential: boolean
  /** 出站代理模式（FR-539）。 */
  proxyMode: 'direct' | 'env' | 'custom'
  /** 自定义代理地址；编辑态留空表示保留原值。 */
  proxyUrl: string
  /** 现有代理是否带口令（只回布尔，地址与口令永不回显）。 */
  proxyHasAuth: boolean
  version: number
  hasCredential: boolean
}

/** 模型表单草稿。 */
interface ModelDraft {
  id: number | null
  providerId: number
  modelId: string
  displayName: string
  streaming: boolean
  tools: boolean
  contextWindow: string
  maxOutputTokens: string
  /** 思考强度（FR-541）；空串 = 不指定。 */
  reasoningEffort: string
  enabled: boolean
  version: number
}

/** 空供应商草稿。 */
function emptyProviderDraft(): ProviderDraft {
  return {
    id: null,
    name: '',
    protocol: 'openai-chat',
    baseUrl: '',
    enabled: true,
    credentialMode: 'none',
    credentialEnv: '',
    credentialSecret: '',
    clearCredential: false,
    proxyMode: 'direct',
    proxyUrl: '',
    proxyHasAuth: false,
    version: 0,
    hasCredential: false,
  }
}

/** 由现有供应商生成草稿（密钥框恒为空——永不回填已保存的值）。 */
function providerDraftOf(p: AssistantProvider): ProviderDraft {
  return {
    id: p.id,
    name: p.name,
    protocol: p.protocol,
    baseUrl: p.baseUrl,
    enabled: p.enabled,
    credentialMode: p.credentialMode,
    credentialEnv: p.credentialEnv ?? '',
    credentialSecret: '',
    clearCredential: false,
    proxyMode: 'direct',
    proxyUrl: '',
    proxyHasAuth: false,
    version: p.version,
    hasCredential: p.hasCredential,
  }
}

/** 空模型草稿。 */
function emptyModelDraft(providerId: number): ModelDraft {
  return {
    id: null,
    providerId,
    modelId: '',
    displayName: '',
    streaming: true,
    tools: false,
    contextWindow: '',
    maxOutputTokens: '',
    reasoningEffort: '',
    enabled: true,
    version: 0,
  }
}

/** 由现有模型生成草稿。 */
function modelDraftOf(m: AssistantModel): ModelDraft {
  return {
    id: m.id,
    providerId: m.providerId,
    modelId: m.modelId,
    displayName: m.displayName,
    streaming: m.streaming,
    tools: m.tools,
    contextWindow: m.contextWindow === null ? '' : String(m.contextWindow),
    maxOutputTokens: m.maxOutputTokens === null ? '' : String(m.maxOutputTokens),
    reasoningEffort: m.reasoningEffort ?? '',
    enabled: m.enabled,
    version: m.version,
  }
}

/** 空串转 null，其余按整数解析（非法值回 null，由后端拒绝）。 */
function intOrNull(raw: string): number | null {
  const trimmed = raw.trim()
  if (trimmed === '') return null
  const n = Number(trimmed)
  return Number.isFinite(n) && Number.isInteger(n) ? n : null
}

/**
 * 地址的**本地**校验（不发请求）。
 *
 * 为什么要在输入时校验：后端只在保存时回 422，用户得先填完、点保存、看报错、
 * 再回来改——而这类错误（少了 https、粘了带 query 的地址）都能在输入时就判出来。
 * 这里**不重复**后端的全部规则（比如 DNS 可达性），只挡语法层面的错，
 * 判定真源仍在服务端；提示语措辞也按「可能」而非「一定」写。
 *
 * 返回 i18n key 与是否阻断保存。
 */
function validateBaseUrl(raw: string): { key: string; fallback: string; blocking: boolean } | null {
  const value = raw.trim()
  if (value === '') {
    return { key: 'assistant.urlEmpty', fallback: '地址不能为空。', blocking: true }
  }
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return {
      key: 'assistant.urlInvalid',
      fallback: '不是合法的 URL（需含 https:// 与主机名）。',
      blocking: true,
    }
  }
  if (parsed.protocol !== 'https:') {
    // http 只在 dev 部署被后端放行；前端不知道部署模式，故提示为「生产只接受 https」，
    // 让用户在点保存前就知道该改什么。
    return {
      key: 'assistant.urlNotHttps',
      fallback: '生产部署只接受 https；当前地址将被拒绝。',
      blocking: false,
    }
  }
  if (parsed.search !== '' || parsed.hash !== '') {
    return { key: 'assistant.urlHasQuery', fallback: '地址不能带 query 参数。', blocking: true }
  }
  if (parsed.username !== '' || parsed.password !== '') {
    return {
      key: 'assistant.urlHasUserInfo',
      fallback: '地址不能内嵌用户名或密码。',
      blocking: true,
    }
  }
  return null
}

/**
 * 粗检是否为 CIDR（形如 `1.2.3.4/24` 或 IPv6）。
 *
 * 只用于「添加按钮是否可点」与就地提示，**不是**权威校验：
 * 网段合法性由后端解析判定，前端这层仅省掉一次必然失败的往返。
 */
function looksLikeCIDR(raw: string): boolean {
  const value = raw.trim()
  if (value === '') return false
  return /^[0-9a-fA-F.:]+\/\d{1,3}$/.test(value)
}

/**
 * 探测结论块（行内展示）。
 *
 * 刻意做成「就地、可停留、可选中的」一块内容而不是 toast：
 * 这里最要紧的信息是**下一步该做什么**（换密钥 / 稍后重试 / 配 NO_PROXY），
 * 它是需要照着操作并可能复述给同事的，3 秒消失的提示承载不了。
 */
function ProbeResultBlock({
  result,
  kind,
}: {
  result: AssistantProbeResult | AssistantModelTestResult
  kind: 'provider' | 'model'
}) {
  const { t } = useTranslation()
  const ok = result.ok
  // 模型测试结果里才有这些字段；供应商探测没有。
  const withModel = 'toolCallParsed' in result

  return (
    <div
      role="status"
      aria-live="polite"
      className={
        ok
          ? 'mt-2 rounded-md border border-border/60 bg-muted/30 px-3 py-2 text-xs'
          : 'mt-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs'
      }
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className={ok ? 'font-medium text-foreground' : 'font-medium text-destructive'}>
          {ok ? t('assistant.probeOk', '连通正常') : t('assistant.probeFailed', '测试失败')}
        </span>
        <span className="text-muted-foreground">
          {t('assistant.probeLatency', '耗时')} {result.latencyMs} ms
        </span>
        {result.classifiedError ? (
          // 分类码用等宽字体：它是要被复制进工单/搜索的东西。
          <code className="rounded bg-background/80 px-1 py-0.5 font-mono text-[11px]">
            {result.classifiedError}
          </code>
        ) : null}
        {withModel && ok ? (
          <span className="text-muted-foreground">
            {t('assistant.probeTools', '工具参数')}{' '}
            {result.toolCallParsed ? t('assistant.probeToolsOk', '已解析') : t('assistant.probeToolsNo', '未验证')}
          </span>
        ) : null}
      </div>
      {/* 失败时给出分类对应的处置建议——这是管理员真正要的东西。 */}
      {!ok ? (
        <p className="mt-1 text-muted-foreground">
          {probeHintText(result.classifiedError, t) || result.message}
        </p>
      ) : null}
      {withModel && ok && result.textPreview ? (
        <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground" title={result.textPreview}>
          {kind === 'model' ? `→ ${result.textPreview}` : result.textPreview}
        </p>
      ) : null}
    </div>
  )
}

/**
 * 分类码 → 处置建议。
 *
 * 与容器侧的 toast 文案共用同一套 key：两处必须说同一件事，
 * 否则用户会在提示里看到一种解释、在结果块里看到另一种。
 */
function probeHintText(code: string | undefined, t: (k: string, fb: string) => string): string {
  switch (code) {
    case 'upstream_auth':
      return t('assistant.hintAuth', '上游拒绝认证：请检查密钥是否正确、是否过期，以及该密钥是否被允许使用此模型。')
    case 'upstream_rate_limited':
      return t('assistant.hintRateLimited', '上游限流：请稍后再试，或检查该密钥的配额。')
    case 'upstream_rejected':
      return t('assistant.hintRejected', '上游拒绝了请求：请确认模型标识与所选协议是否匹配。')
    case 'upstream_error':
      return t('assistant.hintUpstreamError', '上游服务端错误：问题在供应商一侧，请稍后重试。')
    case 'upstream_timeout':
      return t('assistant.hintTimeout', '请求超时：请检查目标地址是否可达，以及网络出口是否正常。')
    case 'upstream_unreachable':
      return t('assistant.hintUnreachable', '无法连接到上游：请检查域名解析与网络连通性。')
    case 'upstream_stream_truncated':
      return t('assistant.hintTruncated', '上游响应意外中断：可能是链路不稳定或上游提前关闭了连接。')
    case 'egress_denied':
      return t('assistant.hintEgressDenied', '被出站准入拒绝：目标地址属于内网或被禁止的范围；如需放行，请到「出站准入」区块添加网段。')
    case 'egress_proxy_unsupported':
      return t('assistant.hintProxyUnsupported', '该目标会经代理出站，无法固定目标 IP，因此被拒绝。请为该域名配置 NO_PROXY，或改用直连。')
    case 'tool_call_invalid':
      return t('assistant.hintToolCallInvalid', '工具调用参数不完整：该模型可能不擅长严格按 schema 输出工具参数。')
    default:
      return ''
  }
}

/** AI 助手配置页。 */
export function AssistantSettingsView({
  isPlatformAdmin,
  providers,
  models,
  availableModels,
  defaultModelId,
  isLoading = false,
  isError = false,
  saving = false,
  onCreateProvider,
  onUpdateProvider,
  onDeleteProvider,
  onCreateModel,
  onUpdateModel,
  onDeleteModel,
  onSetDefaultModel,
  onTestProvider,
  onTestModel,
  probeResults,
  settings,
  settingsSaving = false,
  onSaveEgress,
  onSaveSystemPrompt,
  onToggleBuiltinTools,
  onDiscoverModels,
}: AssistantSettingsViewProps) {
  const { t } = useTranslation()
  const providerList = providers ?? []
  const modelList = models ?? []
  const available = availableModels ?? []

  const [providerDraft, setProviderDraft] = useState<ProviderDraft | null>(null)
  const [modelDraft, setModelDraft] = useState<ModelDraft | null>(null)
  const [pendingDeleteProvider, setPendingDeleteProvider] = useState<AssistantProvider | null>(null)
  const [pendingDeleteModel, setPendingDeleteModel] = useState<AssistantModel | null>(null)
  // 正在测试的行 id：只用来禁用该行按钮，避免连点产生多次付费调用。
  const [testingProviderId, setTestingProviderId] = useState<number | null>(null)
  const [testingModelId, setTestingModelId] = useState<number | null>(null)
  // 新增放行段的草稿。
  const [egressDraft, setEgressDraft] = useState('')
  // 系统提示词草稿（FR-541）。**受控草稿**而不是直接受 settings 控制：
  // 边输边请求会让每次按键都触发一次保存。
  const [systemPromptDraft, setSystemPromptDraft] = useState(settings?.systemPrompt ?? '')
  // 内置工具开关草稿。默认 true（与后端「未配置视为启用」一致）——
  // 若默认 false，面板会显示成「已关闭」而实际是启用的。
  const [builtinToolsDraft, setBuiltinToolsDraft] = useState(settings?.builtinToolsEnabled ?? true)
  // 设置是异步取回的：草稿必须跟随「已加载的那一份」重新对齐，
  // 否则首屏会用空串覆盖用户已保存的提示词（看起来像配置丢了）。
  // 用「记住上一次同步来源」的渲染期对齐代替 useEffect：
  // effect 会在提交后再渲染一帧，中间那一帧显示的是旧值。
  const [syncedSettingsVersion, setSyncedSettingsVersion] = useState<number | null>(null)
  // 模型发现（FR-538）：候选清单与「正在拉取」。
  const [discovering, setDiscovering] = useState(false)
  const [discovered, setDiscovered] = useState<DiscoverModelsResult | null>(null)
  // 勾选的候选模型 ID（默认排除已配置项，避免重复导入）。
  const [discoverSelected, setDiscoverSelected] = useState<Record<string, boolean>>({})
  const [discoverProviderId, setDiscoverProviderId] = useState<number | null>(null)
  if (settings && settings.version !== syncedSettingsVersion) {
    setSyncedSettingsVersion(settings.version)
    setSystemPromptDraft(settings.systemPrompt ?? '')
    setBuiltinToolsDraft(settings.builtinToolsEnabled ?? true)
  }
  // 地址的本地校验结果（仅用于就地提示；真源仍在后端）。
  const urlIssue = providerDraft ? validateBaseUrl(providerDraft.baseUrl) : null
  // 摘要条用的派生值：都是已取回数据的本地聚合，不额外发请求。
  const providersEnabledCount = providerList.filter((p) => p.enabled).length
  const defaultModelName = (() => {
    if (defaultModelId === null || defaultModelId === undefined) return null
    const hit = modelList.find((m) => m.id === defaultModelId)
    if (!hit) return null
    return hit.displayName || hit.modelId
  })()

  /** 提交供应商表单。 */
  const submitProvider = async () => {
    if (!providerDraft) return
    const payload: ProviderPayload = {
      name: providerDraft.name,
      protocol: providerDraft.protocol,
      baseUrl: providerDraft.baseUrl,
      enabled: providerDraft.enabled,
      credentialMode: providerDraft.credentialMode,
      credentialEnv: providerDraft.credentialEnv,
      credentialSecret: providerDraft.credentialSecret,
      clearCredential: providerDraft.clearCredential,
      proxyMode: providerDraft.proxyMode,
      // 编辑态留空 = 保留原地址（后端据此不覆盖）；custom 首次必须填写。
      proxyUrl: providerDraft.proxyUrl,
      version: providerDraft.version,
    }
    const ok =
      providerDraft.id === null
        ? await onCreateProvider(payload)
        : await onUpdateProvider(providerDraft.id, payload)
    if (ok) setProviderDraft(null)
  }

  /**
   * 拉取上游模型候选。
   *
   * 默认选中「未配置过」的项：已配置的再导入一次只会撞唯一索引，
   * 对用户来说是一次没必要的报错。
   */
  const runDiscover = async () => {
    if (!onDiscoverModels) return
    const providerId = discoverProviderId ?? providerList[0]?.id
    if (!providerId) return
    setDiscovering(true)
    try {
      const res = await onDiscoverModels(providerId)
      if (!res) return
      setDiscovered(res)
      setDiscoverProviderId(providerId)
      const initial: Record<string, boolean> = {}
      for (const d of res.discovered) initial[d.modelId] = !d.alreadyConfigured
      setDiscoverSelected(initial)
    } finally {
      setDiscovering(false)
    }
  }

  /**
   * 导入勾选的候选。
   *
   * **逐个创建而不是批量接口**：`contextWindow` / `tools` 这些能力声明不能从模型名猜，
   * 而批量导入只能写一份「通用默认」，等于把「能力未声明」的模型塞进可选集，
   * 之后表现为「聊天报不支持工具」，问题却出在配置阶段。
   * 因此导入只建最小条目（enabled、streaming 开），能力留待管理员逐个确认。
   */
  const importDiscovered = async () => {
    if (!discovered || !discoverProviderId) return
    const targets = discovered.discovered.filter((d) => discoverSelected[d.modelId])
    let ok = 0
    for (const d of targets) {
      const created = await onCreateModel({
        providerId: discoverProviderId,
        modelId: d.modelId,
        displayName: d.modelId,
        streaming: true,
        tools: false,
        contextWindow: null,
        maxOutputTokens: null,
        reasoningEffort: '',
        enabled: true,
      })
      if (created) ok += 1
    }
    if (ok > 0) {
      setDiscovered(null)
      setDiscoverSelected({})
    }
  }

  /** 提交模型表单。 */
  const submitModel = async () => {
    if (!modelDraft) return
    const payload: ModelPayload = {
      providerId: modelDraft.providerId,
      modelId: modelDraft.modelId,
      displayName: modelDraft.displayName,
      streaming: modelDraft.streaming,
      tools: modelDraft.tools,
      contextWindow: intOrNull(modelDraft.contextWindow),
      maxOutputTokens: intOrNull(modelDraft.maxOutputTokens),
      reasoningEffort: modelDraft.reasoningEffort,
      enabled: modelDraft.enabled,
      version: modelDraft.version,
    }
    const ok =
      modelDraft.id === null ? await onCreateModel(payload) : await onUpdateModel(modelDraft.id, payload)
    if (ok) setModelDraft(null)
  }

  /** 探测供应商：包一层「置忙 → 调用 → 复位」，避免同一行被连点。 */
  const runTestProvider = async (id: number) => {
    setTestingProviderId(id)
    try {
      await onTestProvider(id)
    } finally {
      setTestingProviderId(null)
    }
  }

  /** 测试模型，同上。 */
  const runTestModel = async (id: number, withTools: boolean) => {
    setTestingModelId(id)
    try {
      await onTestModel(id, withTools)
    } finally {
      setTestingModelId(null)
    }
  }


  return (
    <PageShell data-page="assistant-settings">
      <PageHeader
        title={t('assistant.title', 'AI 助手')}
        description={t('assistant.subtitle', '配置模型供应商、模型与默认模型。')}
      />

      {!isPlatformAdmin ? (
        // 文案必须放进 AlertDescription：Alert 根节点是 grid-cols-[0_1fr]，
        // 直接把文本塞在根上会落进宽度为 0 的第一列，中文因此一字一行竖排
        // （真机截图实测到的问题）。
        <Alert variant="warning">
          <AlertDescription>{t('assistant.adminOnly', '仅平台管理员可配置 AI 助手。')}</AlertDescription>
        </Alert>
      ) : isLoading ? (
        <Panel bodyClassName="py-10">
          <p className="text-center text-sm text-muted-foreground">{t('common.loading')}</p>
        </Panel>
      ) : isError ? (
        <Alert variant="destructive">
          <AlertDescription>{t('assistant.loadFailed', '加载 AI 配置失败。')}</AlertDescription>
        </Alert>
      ) : (
        <>
          {available.length === 0 ? (
            <Alert variant="info">
              <AlertDescription>
                {t('assistant.notConfigured', '尚未配置可用模型。添加供应商与模型后，助手才能开始工作。')}
              </AlertDescription>
            </Alert>
          ) : (
            /* 摘要条：这些数字此前已经取回却只用于「未配置」判断，
               管理员在页面上看不到「我现在到底有几个能用的模型」。 */
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-border/60 bg-muted/30 px-4 py-2.5 text-sm">
              <span className="text-muted-foreground">
                {t('assistant.summaryProviders', '供应商')}
                <span className="ml-1.5 font-medium text-foreground">
                  {providersEnabledCount} / {providerList.length}
                </span>
              </span>
              <span className="text-muted-foreground">
                {t('assistant.summaryModels', '可用模型')}
                <span className="ml-1.5 font-medium text-foreground">
                  {available.length} / {modelList.length}
                </span>
              </span>
              <span className="text-muted-foreground">
                {t('assistant.summaryDefault', '默认模型')}
                <span className="ml-1.5 font-medium text-foreground">
                  {defaultModelName ?? t('assistant.summaryDefaultUnset', '未设置')}
                </span>
              </span>
            </div>
          )}

          {/* 供应商 */}
          <Panel
            title={t('assistant.providers', '供应商')}
            bodyClassName="p-0"
            actions={
              <Button size="sm" onClick={() => setProviderDraft(emptyProviderDraft())}>
                <Plus className="size-4" />
                {t('assistant.addProvider', '添加供应商')}
              </Button>
            }
          >
            {providerList.length === 0 ? (
              <EmptyState
                title={t('assistant.providersEmpty', '还没有供应商')}
                description={t('assistant.providersEmptyHint', '供应商提供模型来源与凭据；没有它，助手无法调用任何模型。')}
                action={
                  <Button size="sm" onClick={() => setProviderDraft(emptyProviderDraft())}>
                    <Plus className="size-4" />
                    {t('assistant.addProvider', '添加供应商')}
                  </Button>
                }
              />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('assistant.name', '名称')}</TableHead>
                    <TableHead>{t('assistant.protocolLabel', '协议')}</TableHead>
                    <TableHead>{t('assistant.baseUrl', '地址')}</TableHead>
                    <TableHead>{t('assistant.credentialLabel', '凭据')}</TableHead>
                    <TableHead>{t('assistant.status', '状态')}</TableHead>
                    <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {providerList.map((p) => {
                    const probe = probeResults?.[`provider:${p.id}`]
                    return (
                    <Fragment key={p.id}>
                    <TableRow>
                      <TableCell className="font-medium">{p.name}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {/* 不再裸显示 openai-chat 这类内部枚举值——i18n 里早有对应名称。 */}
                        {(() => {
                          const key = protocolLabelKey(p.protocol)
                          return key ? t(key, p.protocol) : p.protocol
                        })()}
                      </TableCell>
                      <TableCell className="max-w-[22rem] truncate text-muted-foreground" title={p.baseUrl}>
                        {p.baseUrl}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <span className="text-muted-foreground">
                            {(() => {
                              const key = credentialModeLabelKey(p.credentialMode)
                              return key ? t(key, p.credentialMode) : p.credentialMode
                            })()}
                          </span>
                          {p.hasCredential ? (
                            <Badge variant="secondary">{t('assistant.credentialSet', '已配置')}</Badge>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={p.enabled ? 'secondary' : 'outline'}>
                          {p.enabled ? t('assistant.enabled', '启用') : t('assistant.disabled', '停用')}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t('assistant.testProvider', '测试连接')}
                            title={t('assistant.testProvider', '测试连接')}
                            disabled={testingProviderId === p.id}
                            onClick={() => void runTestProvider(p.id)}
                          >
                            <PlugZap className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t('common.edit', '编辑')}
                            onClick={() => setProviderDraft(providerDraftOf(p))}
                          >
                            <Pencil className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t('common.delete', '删除')}
                            onClick={() => setPendingDeleteProvider(p)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                    {probe ? (
                      <TableRow key={`${p.id}-probe`} className="hover:bg-transparent">
                        <TableCell colSpan={6} className="pt-0">
                          <ProbeResultBlock result={probe} kind="provider" />
                        </TableCell>
                      </TableRow>
                    ) : null}
                    </Fragment>
                    )
                  })}
                </TableBody>
              </Table>
            )}
          </Panel>

          {/* 模型 */}
          <Panel
            title={t('assistant.models', '模型')}
            bodyClassName="p-0"
            actions={
              <div className="flex items-center gap-2">
                {/* 从上游拉取候选（FR-538）：只读预览，勾选后才逐个落库。
                    手抄模型 ID 必错，而抄错的表现是「探测通过、聊天 404 model not found」，
                    排查会被引向凭据与网络。 */}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={providerList.length === 0 || discovering}
                  onClick={() => void runDiscover()}
                >
                  <RefreshCw className="size-4" />
                  {discovering
                    ? t('assistant.discovering', '拉取中…')
                    : t('assistant.discoverModels', '从上游拉取')}
                </Button>
                <Button
                  size="sm"
                  disabled={providerList.length === 0}
                  onClick={() => setModelDraft(emptyModelDraft(providerList[0]?.id ?? 0))}
                >
                  <Plus className="size-4" />
                  {t('assistant.addModel', '添加模型')}
                </Button>
              </div>
            }
          >
            {modelList.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-muted-foreground">
                {t('assistant.modelsEmpty', '还没有模型。每个供应商下至少需要一个模型才能使用助手。')}
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('assistant.modelId', '模型标识')}</TableHead>
                    <TableHead>{t('assistant.provider', '供应商')}</TableHead>
                    <TableHead>{t('assistant.capabilities', '能力')}</TableHead>
                    <TableHead>{t('assistant.status', '状态')}</TableHead>
                    <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {modelList.map((m) => {
                    const isDefault = defaultModelId === m.id
                    const probe = probeResults?.[`model:${m.id}`]
                    return (
                      <Fragment key={m.id}>
                      <TableRow>
                        <TableCell>
                          <div className="flex items-center gap-1.5">
                            <span className="font-mono text-xs">{m.modelId}</span>
                            {isDefault && <Badge variant="secondary">{t('assistant.isDefault', '默认')}</Badge>}
                          </div>
                          {m.displayName && (
                            <div className="text-xs text-muted-foreground">{m.displayName}</div>
                          )}
                        </TableCell>
                        <TableCell className="text-muted-foreground">
                          {m.providerName}
                          {!m.providerEnabled && (
                            <span className="ml-1 text-xs">
                              ({t('assistant.providerDisabled', '供应商已停用')})
                            </span>
                          )}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {[m.streaming ? 'stream' : null, m.tools ? 'tools' : null]
                            .filter(Boolean)
                            .join(' · ') || '—'}
                        </TableCell>
                        <TableCell>
                          <Badge variant={m.enabled ? 'secondary' : 'outline'}>
                            {m.enabled ? t('assistant.enabled', '启用') : t('assistant.disabled', '停用')}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={t('assistant.testModel', '测试模型')}
                              title={t('assistant.testModel', '测试模型')}
                              disabled={testingModelId === m.id}
                              onClick={() => void runTestModel(m.id, m.tools)}
                            >
                              <PlugZap className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={t('assistant.setDefault', '设为默认')}
                              disabled={saving || isDefault || !m.enabled || !m.providerEnabled}
                              onClick={() => void onSetDefaultModel(m.id)}
                            >
                              <Star className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={t('common.edit', '编辑')}
                              onClick={() => setModelDraft(modelDraftOf(m))}
                            >
                              <Pencil className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={t('common.delete', '删除')}
                              onClick={() => setPendingDeleteModel(m)}
                            >
                              <Trash2 className="size-4" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                      {probe ? (
                        <TableRow className="hover:bg-transparent">
                          <TableCell colSpan={6} className="pt-0">
                            <ProbeResultBlock result={probe} kind="model" />
                          </TableCell>
                        </TableRow>
                      ) : null}
                      </Fragment>
                    )
                  })}
                </TableBody>
              </Table>
            )}
          </Panel>

          {/* 系统提示词与平台自带工具（FR-541 / FR-540）。
              两者放同一面板：它们共同决定「助手知道什么、能碰什么」，
              分开摆会让人以为只有其中一项在影响行为。 */}
          <Panel title={t('assistant.promptTitle', '系统提示词与内置工具')}>
            <div className="space-y-4">
              <div className="space-y-1">
                <FieldLabel htmlFor="assistant-system-prompt">
                  {t('assistant.systemPrompt', '自定义系统提示词')}
                </FieldLabel>
                <Textarea
                  id="assistant-system-prompt"
                  className="min-h-24"
                  value={systemPromptDraft}
                  placeholder={t(
                    'assistant.systemPromptPlaceholder',
                    '留空则只用内置基线。内容只影响「怎么回答」，不改变助手能做什么——写操作仍需逐次确认。',
                  )}
                  onChange={(e) => setSystemPromptDraft(e.target.value)}
                />
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    disabled={settingsSaving || !onSaveSystemPrompt}
                    onClick={() => void onSaveSystemPrompt?.(systemPromptDraft)}
                  >
                    {t('common.save', '保存')}
                  </Button>
                  <p className="text-xs text-muted-foreground">
                    {t('assistant.systemPromptHint', '对新的对话生效；历史消息不会被追溯改写。')}
                  </p>
                </div>
              </div>

              <div className="flex items-start justify-between gap-4 rounded-md border p-3">
                <div className="space-y-1">
                  <p className="text-sm font-medium">
                    {t('assistant.builtinTools', '启用平台自带工具')}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {t(
                      'assistant.builtinToolsHint',
                      '关闭后助手将看不到任何平台工具（读也看不到）。写操作本来就是逐次确认的，因此这一项只影响「能不能查/能不能提议」。',
                    )}
                  </p>
                </div>
                <Switch
                  aria-label={t('assistant.builtinTools', '启用平台自带工具')}
                  checked={builtinToolsDraft}
                  disabled={settingsSaving}
                  onCheckedChange={(next) => {
                    setBuiltinToolsDraft(next)
                    void onToggleBuiltinTools?.(next)
                  }}
                />
              </div>
            </div>
          </Panel>

          {/* 技能（FR-541）：目录 + SKILL.md，展示正文与被跳过项。 */}
          <AssistantSkillsPanel enabled={isPlatformAdmin} />

          {/* 外部 MCP 连接（FR-517）：独立面板，避免把这一大块塞进本文件。 */}
          <MCPConnectionsPanel />

          {/* 出站准入：AI 出站能打到哪些非公网地址。
              这是页面上唯一能「扩大可达范围」的地方，因此风险提示与逐条校验都要显式。 */}
          <Panel title={t('assistant.egressTitle', '出站准入')}>
            <div className="space-y-3">
              <p className="text-xs text-muted-foreground">
                {t(
                  'assistant.egressSubtitle',
                  '默认只允许 https 公网目标；云元数据地址永远拒绝。只有在这里显式填写的网段才会被放行。',
                )}
              </p>
              <Alert variant="warning">
                <AlertDescription>
                  {t(
                    'assistant.egressRisk',
                    '放行后，该网段内任何能被 HTTP 访问的服务都可能成为模型请求的目标。请只填你确实要接的模型网关所在网段。',
                  )}
                </AlertDescription>
              </Alert>

              {settings && settings.allowPrivateCidrs.length > 0 ? (
                <ul className="divide-y divide-border/60 rounded-md border border-border/60">
                  {settings.allowPrivateCidrs.map((cidr) => (
                    <li key={cidr} className="flex items-center justify-between gap-2 px-3 py-2">
                      <code className="font-mono text-xs">{cidr}</code>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={t('assistant.egressRemove', '移除该网段')}
                        disabled={settingsSaving}
                        onClick={() =>
                          void onSaveEgress(settings.allowPrivateCidrs.filter((c) => c !== cidr))
                        }
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </li>
                  ))}
                </ul>
              ) : (
                <EmptyState
                  title={t('assistant.egressEmpty', '没有放行任何私网段')}
                  description={t('assistant.egressEmptyHint', '只接公网模型供应商时保持为空即可。')}
                />
              )}

              {/* 新增一条网段：前端只做「是否像 CIDR」的粗检，真源仍在后端。 */}
              <div className="flex items-start gap-2">
                <div className="flex-1 space-y-1">
                  <FieldLabel htmlFor="assistant-egress-cidr">
                    {t('assistant.egressAddLabel', '添加网段')}
                  </FieldLabel>
                  <Input
                    id="assistant-egress-cidr"
                    value={egressDraft}
                    placeholder="192.0.2.0/24"
                    onChange={(e) => setEgressDraft(e.target.value)}
                  />
                  {egressDraft.trim() !== '' && !looksLikeCIDR(egressDraft) ? (
                    <span className="block text-xs text-destructive">
                      {t('assistant.egressInvalid', '格式应为 CIDR，例如 10.0.0.0/8 或 192.168.1.0/24。')}
                    </span>
                  ) : null}
                </div>
                <Button
                  className="mt-6"
                  variant="outline"
                  size="sm"
                  disabled={
                    settingsSaving || egressDraft.trim() === '' || !looksLikeCIDR(egressDraft)
                  }
                  onClick={async () => {
                    const next = [...(settings?.allowPrivateCidrs ?? []), egressDraft.trim()]
                    if (await onSaveEgress(next)) setEgressDraft('')
                  }}
                >
                  <Plus className="size-4" />
                  {t('assistant.egressAdd', '添加')}
                </Button>
              </div>

              {settings && !settings.insecureAllowed ? (
                <p className="text-xs text-muted-foreground">
                  {t(
                    'assistant.egressInsecureOff',
                    '当前部署只接受 https 目标；即使放行网段，http:// 地址仍会被拒绝。',
                  )}
                </p>
              ) : null}
            </div>
          </Panel>
        </>
      )}

      {/* 供应商表单：内容自适应模态（ui-modals 规则） */}
      <Dialog open={providerDraft !== null} onOpenChange={(open) => !open && setProviderDraft(null)}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>
              {providerDraft?.id === null
                ? t('assistant.addProvider', '添加供应商')
                : t('assistant.editProvider', '编辑供应商')}
            </DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-3">
            {providerDraft && (
              <>
                <div className="space-y-1">
                  <FieldLabel htmlFor="assistant-provider-name" required>
                    {t('assistant.name', '名称')}
                  </FieldLabel>
                  <Input
                    id="assistant-provider-name"
                    value={providerDraft.name}
                    onChange={(e) => setProviderDraft({ ...providerDraft, name: e.target.value })}
                  />
                </div>
                <div className="space-y-1">
                  <FieldLabel htmlFor="assistant-provider-protocol" required>
                    {t('assistant.protocolLabel', '协议')}
                  </FieldLabel>
                  <NativeSelect
                    id="assistant-provider-protocol"
                    value={providerDraft.protocol}
                    onChange={(e) => setProviderDraft({ ...providerDraft, protocol: e.target.value })}
                  >
                    {ASSISTANT_PROTOCOLS.map((p) => (
                      <option key={p.value} value={p.value}>
                        {t(p.labelKey, p.value)}
                      </option>
                    ))}
                  </NativeSelect>
                </div>
                <div className="space-y-1">
                  <FieldLabel htmlFor="assistant-provider-base-url" required>
                    {t('assistant.baseUrl', '地址')}
                  </FieldLabel>
                  <Input
                    id="assistant-provider-base-url"
                    value={providerDraft.baseUrl}
                    placeholder="https://api.example.com/v1"
                    aria-invalid={urlIssue?.blocking === true}
                    onChange={(e) => setProviderDraft({ ...providerDraft, baseUrl: e.target.value })}
                  />
                  {/* 输入时就地提示，而不是等保存后 422：这类错误（少了 https、
                      粘了带 query 的地址）都能在本地判出来。 */}
                  {urlIssue ? (
                    <span
                      className={
                        urlIssue.blocking
                          ? 'block text-xs text-destructive'
                          : 'block text-xs text-muted-foreground'
                      }
                    >
                      {/* 带中文兜底：缺词条时也不让页面显示裸 i18n key。 */}
                      {t(urlIssue.key, urlIssue.fallback)}
                    </span>
                  ) : (
                    <span className="block text-xs text-muted-foreground">
                      {t('assistant.baseUrlHint', '仅支持 https，不能带 query 或内嵌凭据。')}
                    </span>
                  )}
                </div>
                <div className="space-y-1">
                  <FieldLabel htmlFor="assistant-provider-credential-mode">
                    {t('assistant.credentialLabel', '凭据')}
                  </FieldLabel>
                  <NativeSelect
                    id="assistant-provider-credential-mode"
                    value={providerDraft.credentialMode}
                    onChange={(e) =>
                      setProviderDraft({
                        ...providerDraft,
                        credentialMode: e.target.value as ProviderDraft['credentialMode'],
                        clearCredential: false,
                      })
                    }
                  >
                    {ASSISTANT_CREDENTIAL_MODES.map((m) => (
                      <option key={m.value} value={m.value}>
                        {t(m.labelKey, m.value)}
                      </option>
                    ))}
                  </NativeSelect>
                </div>
                {providerDraft.credentialMode === 'env' && (
                  <div className="space-y-1">
                    <FieldLabel htmlFor="assistant-provider-credential-env" required>
                      {t('assistant.credentialEnv', '环境变量名')}
                    </FieldLabel>
                    <Input
                      id="assistant-provider-credential-env"
                      value={providerDraft.credentialEnv}
                      placeholder="JIANMANAGER_ASSISTANT_PROVIDER_KEY"
                      onChange={(e) => setProviderDraft({ ...providerDraft, credentialEnv: e.target.value })}
                    />
                    <span className="block text-xs text-muted-foreground">
                      {t('assistant.credentialEnvHint', '只读进程环境，不读文件；变量须以 JIANMANAGER_ASSISTANT_ 开头。')}
                    </span>
                  </div>
                )}
                {providerDraft.credentialMode === 'stored' && (
                  <div className="space-y-1">
                    <div className="flex items-center justify-between gap-2">
                      <FieldLabel htmlFor="assistant-provider-credential-secret">
                        {t('assistant.credentialSecret', 'API 密钥')}
                      </FieldLabel>
                      {/* 三态显式化：以前「已保存」只能靠「留空＝保留」这一句提示表达，
                          用户看不出当前到底有没有存过密钥、也无法明确清除。 */}
                      {providerDraft.hasCredential ? (
                        <div className="flex items-center gap-1.5">
                          <Badge variant="secondary">{t('assistant.credentialSet', '已配置')}</Badge>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="h-6 px-2 text-xs"
                            onClick={() =>
                              setProviderDraft({
                                ...providerDraft,
                                credentialSecret: '',
                                clearCredential: true,
                              })
                            }
                          >
                            {t('assistant.credentialClear', '清除')}
                          </Button>
                        </div>
                      ) : null}
                    </div>
                    <Input
                      id="assistant-provider-credential-secret"
                      type="password"
                      value={providerDraft.credentialSecret}
                      disabled={providerDraft.clearCredential}
                      placeholder={
                        providerDraft.clearCredential
                          ? t('assistant.credentialWillClear', '保存后将清除已存密钥')
                          : providerDraft.hasCredential
                            ? t('assistant.credentialKeepHint', '留空表示保留已保存的密钥')
                            : t('assistant.credentialRequiredHint', '必填：保存后仅显示「已配置」')
                      }
                      onChange={(e) =>
                        setProviderDraft({ ...providerDraft, credentialSecret: e.target.value })
                      }
                    />
                    {providerDraft.clearCredential ? (
                      <span className="block text-xs text-destructive">
                        {t('assistant.credentialClearWarning', '将清除密钥并把凭据模式改为「无」。')}
                      </span>
                    ) : null}
                  </div>
                )}
                {/* 出站代理（FR-539）。
                    默认直连是刻意的：不静默走进程环境代理，否则本机为别的用途
                    设的 HTTPS_PROXY 会把模型出站改道，而面板上看着还是「直连」。 */}
                <div className="space-y-1 rounded-md border border-border/60 px-3 py-2">
                  <FieldLabel htmlFor="assistant-provider-proxy-mode">
                    {t('assistant.proxyMode', '出站代理')}
                  </FieldLabel>
                  <NativeSelect
                    id="assistant-provider-proxy-mode"
                    value={providerDraft.proxyMode}
                    onChange={(e) =>
                      setProviderDraft({
                        ...providerDraft,
                        proxyMode: e.target.value as ProviderDraft['proxyMode'],
                      })
                    }
                  >
                    <option value="direct">{t('assistant.proxyDirect', '直连（不读环境代理）')}</option>
                    <option value="env">{t('assistant.proxyEnv', '跟随进程环境（HTTP(S)_PROXY）')}</option>
                    <option value="custom">{t('assistant.proxyCustom', '自定义代理')}</option>
                  </NativeSelect>

                  {providerDraft.proxyMode === 'custom' && (
                    <div className="space-y-1 pt-1">
                      <Input
                        aria-label={t('assistant.proxyUrl', '代理地址')}
                        placeholder={t('assistant.proxyUrlPlaceholder', 'http://host:port 或 socks5://host:port')}
                        value={providerDraft.proxyUrl}
                        onChange={(e) =>
                          setProviderDraft({ ...providerDraft, proxyUrl: e.target.value })
                        }
                      />
                      <p className="text-xs text-muted-foreground">
                        {providerDraft.proxyHasAuth
                          ? t(
                              'assistant.proxyHasAuth',
                              '已保存过带口令的代理地址；地址与口令不会回显，留空表示保持原值。',
                            )
                          : t(
                              'assistant.proxyHint',
                              '支持 http / https / socks5。地址只在保存时上传，之后不回显。',
                            )}
                      </p>
                    </div>
                  )}
                </div>

                {/* 用设计系统的 Switch 而不是原生 checkbox：
                    原生控件在各浏览器的默认尺寸与焦点环都不受 token 控制，
                    且这里语义本来就是「开关」而非「多项选择之一」。 */}
                <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2">
                  <div className="min-w-0">
                    <FieldLabel htmlFor="assistant-provider-enabled" className="block">
                      {t('assistant.enabled', '启用')}
                    </FieldLabel>
                    <span className="block text-xs text-muted-foreground">
                      {t('assistant.enabledHint', '停用后该供应商不参与可用模型摘要，也不可被探测。')}
                    </span>
                  </div>
                  <Switch
                    id="assistant-provider-enabled"
                    checked={providerDraft.enabled}
                    onCheckedChange={(v) => setProviderDraft({ ...providerDraft, enabled: v })}
                    aria-label={t('assistant.enabled', '启用')}
                  />
                </div>
              </>
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setProviderDraft(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button onClick={() => void submitProvider()} disabled={saving}>
              {saving ? t('common.saving', '保存中…') : t('common.save', '保存')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 模型表单 */}
      <Dialog open={modelDraft !== null} onOpenChange={(open) => !open && setModelDraft(null)}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>
              {modelDraft?.id === null
                ? t('assistant.addModel', '添加模型')
                : t('assistant.editModel', '编辑模型')}
            </DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-3">
            {modelDraft && (
              <>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.provider', '供应商')}</span>
                  <NativeSelect
                    aria-label={t('assistant.provider', '供应商')}
                    value={String(modelDraft.providerId)}
                    onChange={(e) =>
                      setModelDraft({ ...modelDraft, providerId: Number(e.target.value) })
                    }
                  >
                    {providerList.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </NativeSelect>
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.modelId', '模型标识')}</span>
                  <Input
                    value={modelDraft.modelId}
                    placeholder={t('assistant.modelIdHint', '上游的原始模型名，例如 gpt-4o-mini')}
                    onChange={(e) => setModelDraft({ ...modelDraft, modelId: e.target.value })}
                  />
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.displayName', '显示名')}</span>
                  <Input
                    value={modelDraft.displayName}
                    onChange={(e) => setModelDraft({ ...modelDraft, displayName: e.target.value })}
                  />
                </label>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1">
                    <FieldLabel htmlFor="assistant-model-context-window">
                      {t('assistant.contextWindow', '上下文窗口')}
                    </FieldLabel>
                    <div className="relative">
                      <Input
                        id="assistant-model-context-window"
                        type="number"
                        min={1}
                        inputMode="numeric"
                        className="pr-12"
                        value={modelDraft.contextWindow}
                        placeholder={t('assistant.undeclared', '未声明')}
                        onChange={(e) => setModelDraft({ ...modelDraft, contextWindow: e.target.value })}
                      />
                      <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
                        tokens
                      </span>
                    </div>
                  </div>
                  <div className="space-y-1">
                    <FieldLabel htmlFor="assistant-model-max-output">
                      {t('assistant.maxOutput', '输出上限')}
                    </FieldLabel>
                    <div className="relative">
                      <Input
                        id="assistant-model-max-output"
                        type="number"
                        min={1}
                        inputMode="numeric"
                        className="pr-12"
                        value={modelDraft.maxOutputTokens}
                        placeholder={t('assistant.undeclared', '未声明')}
                        onChange={(e) => setModelDraft({ ...modelDraft, maxOutputTokens: e.target.value })}
                      />
                      <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
                        tokens
                      </span>
                    </div>
                  </div>
                </div>
                {/* 思考强度（FR-541）：空选项表示不指定，由上游用自身默认。
                    不默认选中某一档：那样会让「没配过」和「配了 medium」看起来一样。 */}
                <div className="space-y-1">
                  <FieldLabel htmlFor="assistant-model-reasoning-effort">
                    {t('assistant.reasoningEffort', '思考强度')}
                  </FieldLabel>
                  <NativeSelect
                    id="assistant-model-reasoning-effort"
                    value={modelDraft.reasoningEffort}
                    onChange={(e) =>
                      setModelDraft({ ...modelDraft, reasoningEffort: e.target.value })
                    }
                  >
                    <option value="">{t('assistant.reasoningDefault', '不指定（由上游默认）')}</option>
                    <option value="minimal">{t('assistant.reasoningMinimal', 'minimal（最快）')}</option>
                    <option value="low">{t('assistant.reasoningLow', 'low')}</option>
                    <option value="medium">{t('assistant.reasoningMedium', 'medium')}</option>
                    <option value="high">{t('assistant.reasoningHigh', 'high（最深）')}</option>
                  </NativeSelect>
                  <p className="text-xs text-muted-foreground">
                    {t(
                      'assistant.reasoningHint',
                      '按协议自动映射：OpenAI 系用 reasoning_effort，Anthropic 用 thinking 预算。未知值不会下发。',
                    )}
                  </p>
                </div>
                <div className="flex gap-4 text-sm">
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={modelDraft.streaming}
                      onChange={(e) => setModelDraft({ ...modelDraft, streaming: e.target.checked })}
                    />
                    {t('assistant.streaming', '流式')}
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={modelDraft.tools}
                      onChange={(e) => setModelDraft({ ...modelDraft, tools: e.target.checked })}
                    />
                    {t('assistant.tools', '工具调用')}
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={modelDraft.enabled}
                      onChange={(e) => setModelDraft({ ...modelDraft, enabled: e.target.checked })}
                    />
                    {t('assistant.enabled', '启用')}
                  </label>
                </div>
              </>
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setModelDraft(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button onClick={() => void submitModel()} disabled={saving}>
              {saving ? t('common.saving', '保存中…') : t('common.save', '保存')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认 */}
      <DangerConfirm
        open={pendingDeleteProvider !== null}
        title={t('assistant.deleteProviderTitle', '删除供应商')}
        description={t('assistant.deleteProviderDesc', '删除后其下模型将无法使用。存在模型时会被拒绝。')}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={() => {
          const target = pendingDeleteProvider
          setPendingDeleteProvider(null)
          if (target) void onDeleteProvider(target.id)
        }}
        onCancel={() => setPendingDeleteProvider(null)}
      />
      <DangerConfirm
        open={pendingDeleteModel !== null}
        title={t('assistant.deleteModelTitle', '删除模型')}
        description={t('assistant.deleteModelDesc', '若它是默认模型，默认设置会一并清空。')}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={() => {
          const target = pendingDeleteModel
          setPendingDeleteModel(null)
          if (target) void onDeleteModel(target.id)
        }}
        onCancel={() => setPendingDeleteModel(null)}
      />

      {/* 模型发现结果（FR-538）：勾选后逐个导入。
          显示 truncated 与 skipped 是刻意的：只回「有哪些」会让
          「上游明明有 30 个，这里只有 29 个」无从解释。 */}
      <Dialog open={discovered !== null} onOpenChange={(open) => !open && setDiscovered(null)}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>{t('assistant.discoverTitle', '从上游拉取的模型')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody>
            {discovered && discovered.discovered.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t('assistant.discoverEmpty', '上游没有返回任何模型。请确认该供应商的列表端点可用。')}
              </p>
            ) : (
              <div className="space-y-2">
                {discovered?.discovered.map((d) => (
                  <label key={d.modelId} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={discoverSelected[d.modelId] ?? false}
                      onChange={(e) =>
                        setDiscoverSelected({ ...discoverSelected, [d.modelId]: e.target.checked })
                      }
                    />
                    <span className="font-mono">{d.modelId}</span>
                    {d.alreadyConfigured && (
                      <Badge variant="secondary">{t('assistant.discoverConfigured', '已配置')}</Badge>
                    )}
                  </label>
                ))}
                {discovered?.truncated && (
                  <p className="text-xs text-muted-foreground">
                    {t('assistant.discoverTruncated', '上游条目超过上限，仅列出前一部分。')}
                  </p>
                )}
                {discovered?.skipped && Object.keys(discovered.skipped).length > 0 && (
                  <div className="text-xs text-muted-foreground">
                    {t('assistant.discoverSkipped', '部分条目被跳过：')}
                    {Object.entries(discovered.skipped).map(([k, v]) => (
                      <div key={k} className="font-mono">
                        {k} — {v}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDiscovered(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button
              disabled={!discovered || !Object.values(discoverSelected).some(Boolean)}
              onClick={() => void importDiscovered()}
            >
              {t('assistant.discoverImport', '导入选中项')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageShell>
  )
}
