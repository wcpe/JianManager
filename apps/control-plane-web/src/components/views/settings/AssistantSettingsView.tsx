import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2, Star } from 'lucide-react'
import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  Input,
  NativeSelect,
  Panel,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { ASSISTANT_CREDENTIAL_MODES, ASSISTANT_PROTOCOLS } from '@/api/assistant'
import type {
  AssistantAvailableModel,
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
}: AssistantSettingsViewProps) {
  const { t } = useTranslation()
  const providerList = providers ?? []
  const modelList = models ?? []
  const available = availableModels ?? []

  const [providerDraft, setProviderDraft] = useState<ProviderDraft | null>(null)
  const [modelDraft, setModelDraft] = useState<ModelDraft | null>(null)
  const [pendingDeleteProvider, setPendingDeleteProvider] = useState<AssistantProvider | null>(null)
  const [pendingDeleteModel, setPendingDeleteModel] = useState<AssistantModel | null>(null)

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
      version: providerDraft.version,
    }
    const ok =
      providerDraft.id === null
        ? await onCreateProvider(payload)
        : await onUpdateProvider(providerDraft.id, payload)
    if (ok) setProviderDraft(null)
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
      enabled: modelDraft.enabled,
      version: modelDraft.version,
    }
    const ok =
      modelDraft.id === null ? await onCreateModel(payload) : await onUpdateModel(modelDraft.id, payload)
    if (ok) setModelDraft(null)
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
          {available.length === 0 && (
            <Alert variant="info">
              <AlertDescription>
                {t('assistant.notConfigured', '尚未配置可用模型。添加供应商与模型后，助手才能开始工作。')}
              </AlertDescription>
            </Alert>
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
              <p className="px-4 py-8 text-center text-sm text-muted-foreground">
                {t('assistant.providersEmpty', '还没有供应商。')}
              </p>
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
                  {providerList.map((p) => (
                    <TableRow key={p.id}>
                      <TableCell className="font-medium">{p.name}</TableCell>
                      <TableCell className="text-muted-foreground">{p.protocol}</TableCell>
                      <TableCell className="max-w-[22rem] truncate text-muted-foreground" title={p.baseUrl}>
                        {p.baseUrl}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <span className="text-muted-foreground">{p.credentialMode}</span>
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
                  ))}
                </TableBody>
              </Table>
            )}
          </Panel>

          {/* 模型 */}
          <Panel
            title={t('assistant.models', '模型')}
            bodyClassName="p-0"
            actions={
              <Button
                size="sm"
                disabled={providerList.length === 0}
                onClick={() => setModelDraft(emptyModelDraft(providerList[0]?.id ?? 0))}
              >
                <Plus className="size-4" />
                {t('assistant.addModel', '添加模型')}
              </Button>
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
                    return (
                      <TableRow key={m.id}>
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
                    )
                  })}
                </TableBody>
              </Table>
            )}
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
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.name', '名称')}</span>
                  <Input
                    value={providerDraft.name}
                    onChange={(e) => setProviderDraft({ ...providerDraft, name: e.target.value })}
                  />
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.protocolLabel', '协议')}</span>
                  <NativeSelect
                    aria-label={t('assistant.protocolLabel', '协议')}
                    value={providerDraft.protocol}
                    onChange={(e) => setProviderDraft({ ...providerDraft, protocol: e.target.value })}
                  >
                    {ASSISTANT_PROTOCOLS.map((p) => (
                      <option key={p.value} value={p.value}>
                        {t(p.labelKey, p.value)}
                      </option>
                    ))}
                  </NativeSelect>
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.baseUrl', '地址')}</span>
                  <Input
                    value={providerDraft.baseUrl}
                    placeholder="https://api.example.com/v1"
                    onChange={(e) => setProviderDraft({ ...providerDraft, baseUrl: e.target.value })}
                  />
                  <span className="block text-xs text-muted-foreground">
                    {t('assistant.baseUrlHint', '仅支持 https，不能带 query 或内嵌凭据。')}
                  </span>
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium">{t('assistant.credentialLabel', '凭据')}</span>
                  <NativeSelect
                    aria-label={t('assistant.credentialLabel', '凭据')}
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
                </label>
                {providerDraft.credentialMode === 'env' && (
                  <label className="block space-y-1">
                    <span className="text-sm font-medium">{t('assistant.credentialEnv', '环境变量名')}</span>
                    <Input
                      value={providerDraft.credentialEnv}
                      placeholder="JIANMANAGER_ASSISTANT_PROVIDER_KEY"
                      onChange={(e) => setProviderDraft({ ...providerDraft, credentialEnv: e.target.value })}
                    />
                  </label>
                )}
                {providerDraft.credentialMode === 'stored' && (
                  <label className="block space-y-1">
                    <span className="text-sm font-medium">{t('assistant.credentialSecret', 'API 密钥')}</span>
                    <Input
                      type="password"
                      value={providerDraft.credentialSecret}
                      placeholder={
                        providerDraft.hasCredential
                          ? t('assistant.credentialKeepHint', '留空表示保留已保存的密钥')
                          : ''
                      }
                      onChange={(e) =>
                        setProviderDraft({ ...providerDraft, credentialSecret: e.target.value })
                      }
                    />
                  </label>
                )}
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={providerDraft.enabled}
                    onChange={(e) => setProviderDraft({ ...providerDraft, enabled: e.target.checked })}
                  />
                  {t('assistant.enabled', '启用')}
                </label>
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
                  <label className="block space-y-1">
                    <span className="text-sm font-medium">{t('assistant.contextWindow', '上下文窗口')}</span>
                    <Input
                      value={modelDraft.contextWindow}
                      placeholder={t('assistant.optional', '可留空')}
                      onChange={(e) => setModelDraft({ ...modelDraft, contextWindow: e.target.value })}
                    />
                  </label>
                  <label className="block space-y-1">
                    <span className="text-sm font-medium">{t('assistant.maxOutput', '输出上限')}</span>
                    <Input
                      value={modelDraft.maxOutputTokens}
                      placeholder={t('assistant.optional', '可留空')}
                      onChange={(e) => setModelDraft({ ...modelDraft, maxOutputTokens: e.target.value })}
                    />
                  </label>
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
    </PageShell>
  )
}
