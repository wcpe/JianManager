import { useState, useMemo, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  Plus,
  Trash2,
  Zap,
  Copy,
  Check,
  Coffee,
  MemoryStick,
  Box,
  Route,
  Flame,
  Grid2x2,
  Package,
  Clock,
  Search,
  LayoutGrid,
  Download,
} from 'lucide-react'
import api from '@/api/client'
import {
  useTemplates,
  useCreateTemplate,
  useDeleteTemplate,
  type TemplateInfo,
} from '@/api/templates'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import {
  deriveMarketMeta,
  extractVariables,
  fillTemplate,
  validateVariableValues,
  type MarketIcon,
} from '@/lib/template-apply'
import type { Tone } from '@/lib/tone'
import { Panel } from '@/components/ui/panel'
import { StatCard } from '@/components/ui/stat-card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validateUrl, validateAbsPath, validateFields, hasErrors } from '@/lib/form-validation'
import { cn } from '@/lib/utils'
import DangerConfirm from '@/components/DangerConfirm'

/** 市场图标语义名 → lucide 组件（纯函数派生图标，组件侧落地）。 */
const MARKET_ICONS: Record<MarketIcon, typeof Box> = {
  cube: Box,
  route: Route,
  flame: Flame,
  grid: Grid2x2,
  package: Package,
}

/** 色调 → 封面块底色（与 toneChipClass 同语义：主色淡染、状态色 12% 底，双主题安全）。 */
function coverBgClass(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'bg-status-success/12'
    case 'warning':
      return 'bg-status-warning/12'
    case 'danger':
      return 'bg-status-danger/12'
    case 'info':
      return 'bg-status-info/12'
    default:
      return 'bg-accent'
  }
}

/** 色调 → 封面图标/标题前景色。 */
function coverFgClass(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'text-status-success'
    case 'warning':
      return 'text-status-warning'
    case 'danger':
      return 'text-status-danger'
    case 'info':
      return 'text-status-info'
    default:
      return 'text-primary'
  }
}

/**
 * 服务端模板页（FR-064 + FR-154）：升级为「应用市场」观感——封面 + 类型/Java·RAM 需求卡片，
 * 「用此模板创建实例」入口含变量填充预览 + startCommand 复制 + 更新时间。
 * 套 FR-163 靛蓝圆角双主题原语（Panel/StatCard），新建/删除沿用既有能力。
 */
export default function TemplatesPage() {
  const { t } = useTranslation()
  const { data: templates, isLoading } = useTemplates()
  const [createOpen, setCreateOpen] = useState(false)
  const [applyTarget, setApplyTarget] = useState<TemplateInfo | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<TemplateInfo | null>(null)
  const [query, setQuery] = useState('')
  const del = useDeleteTemplate()

  // 按名称/类型/描述过滤（市场搜索）。
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return templates ?? []
    return (templates ?? []).filter(
      (tpl) =>
        tpl.name.toLowerCase().includes(q) ||
        tpl.type.toLowerCase().includes(q) ||
        (tpl.description ?? '').toLowerCase().includes(q),
    )
  }, [templates, query])

  const typeCount = useMemo(
    () => new Set((templates ?? []).map((tpl) => tpl.type).filter(Boolean)).size,
    [templates],
  )

  const confirmDelete = () => {
    if (!deleteTarget) return
    del.mutate(deleteTarget.id, {
      onSuccess: () => toast.success(t('templates.deleted')),
      onError: () => toast.error(t('templates.deleteFailed')),
    })
    setDeleteTarget(null)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-bold">{t('templates.title')}</h1>
          <p className="text-xs text-muted-foreground">{t('templates.marketSubtitle')}</p>
        </div>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus />
          {t('templates.create')}
        </Button>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : !templates || templates.length === 0 ? (
        <Panel>
          <p className="py-8 text-center text-sm text-muted-foreground">{t('templates.empty')}</p>
        </Panel>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            <StatCard
              label={t('templates.market.totalApps')}
              value={templates.length}
              icon={<LayoutGrid className="size-3.5" />}
            />
            <StatCard
              label={t('templates.market.typeCount')}
              value={typeCount}
              icon={<Package className="size-3.5" />}
              tone="info"
            />
            <StatCard
              label={t('templates.market.oneClickDeploy')}
              value={<Zap className="size-5" />}
              sub={t('templates.market.oneClickDeployHint')}
              icon={<Zap className="size-3.5" />}
              tone="success"
              className="hidden sm:flex"
            />
          </div>

          <div className="relative max-w-xs">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('templates.market.search')}
              className="pl-8"
            />
          </div>

          {filtered.length === 0 ? (
            <Panel>
              <p className="py-8 text-center text-sm text-muted-foreground">{t('templates.market.noMatch')}</p>
            </Panel>
          ) : (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
              {filtered.map((tpl) => (
                <MarketCard
                  key={tpl.id}
                  template={tpl}
                  onApply={() => setApplyTarget(tpl)}
                  onDelete={() => setDeleteTarget(tpl)}
                />
              ))}
            </div>
          )}
        </>
      )}

      {createOpen && <CreateTemplateDialog onClose={() => setCreateOpen(false)} />}
      {applyTarget && <ApplyTemplateDialog template={applyTarget} onClose={() => setApplyTarget(null)} />}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('templates.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        description={t('templates.deleteDescription')}
        confirmLabel={t('common.delete')}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/** 应用市场卡片：封面块（图标 + 名称）+ 类型/Java·RAM 需求 + 更新时间 + 一键部署/删除。 */
function MarketCard({
  template,
  onApply,
  onDelete,
}: {
  template: TemplateInfo
  onApply: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const meta = deriveMarketMeta(template)
  const Icon = MARKET_ICONS[meta.icon]
  const updatedAt = template.updatedAt || template.createdAt

  return (
    <Panel hoverable bodyClassName="p-0" className="overflow-hidden">
      {/* 封面块：语义底色 + 大图标 + 左下角名称。 */}
      <div className={cn('relative flex h-20 items-center justify-center', coverBgClass(meta.tone))}>
        <Icon className={cn('size-8', coverFgClass(meta.tone))} />
        <span className={cn('absolute bottom-2 left-3 truncate pr-3 text-sm font-semibold', coverFgClass(meta.tone))}>
          {template.name}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          className="absolute right-1.5 top-1.5 bg-card/60 text-muted-foreground hover:text-destructive"
          onClick={onDelete}
          aria-label={t('common.delete')}
        >
          <Trash2 />
        </Button>
      </div>

      <div className="space-y-2.5 p-3">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className={cn('rounded-full px-2 py-0.5 text-[10px] font-medium', coverBgClass(meta.tone), coverFgClass(meta.tone))}>
            {meta.typeLabel}
          </span>
        </div>

        {template.description ? (
          <p className="line-clamp-2 text-xs text-muted-foreground">{template.description}</p>
        ) : null}

        {/* 运行时需求：Java + RAM（推断得到才显示）。 */}
        {(meta.java || meta.ram) && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
            {meta.java && (
              <span className="inline-flex items-center gap-1">
                <Coffee className="size-3.5" />
                {meta.java}
              </span>
            )}
            {meta.ram && (
              <span className="inline-flex items-center gap-1">
                <MemoryStick className="size-3.5" />
                {t('templates.market.ramShort', { value: meta.ram })}
              </span>
            )}
          </div>
        )}

        <div className="flex items-center justify-between gap-2 text-[10px] text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <Clock className="size-3" />
            {new Date(updatedAt).toLocaleDateString()}
          </span>
          {template.downloadUrl && (
            <a
              href={template.downloadUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-primary hover:underline"
            >
              <Download className="size-3" />
              {t('templates.downloadLink')}
            </a>
          )}
        </div>

        <Button size="sm" className="w-full" onClick={onApply}>
          <Zap />
          {t('templates.market.deploy')}
        </Button>
      </div>
    </Panel>
  )
}

/**
 * 应用模板对话框（FR-154）：轻量预览 + 一键创建，不改共享 ProvisionServerDialog。
 * 含占位变量（`{{var}}`）时提供填充表单 + 实时 startCommand 派生预览（可复制/展开）；
 * 选节点 + 实例名后，直接调既有 `POST /instances`（与 CreateInstanceDialog 同流）。
 */
function ApplyTemplateDialog({ template, onClose }: { template: TemplateInfo; onClose: () => void }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()

  const meta = deriveMarketMeta(template)
  const vars = useMemo(() => extractVariables(template.startCommand), [template.startCommand])
  const isMc = template.type.trim().toLowerCase().replace(/[-\s]+/g, '_') === 'minecraft_java'

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [groupId, setGroupId] = useState('')
  const [values, setValues] = useState<Record<string, string>>({})
  const [copied, setCopied] = useState(false)
  const [showFullCmd, setShowFullCmd] = useState(false)

  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  // 派生预览：把已填变量替换进 startCommand（未填占位保留，缺口可见）。
  const derivedCommand = useMemo(() => fillTemplate(template.startCommand, values), [template.startCommand, values])

  const varErrors = validateVariableValues(vars, values)
  const baseErrors = validateFields(
    { name, nodeId },
    { name: [validateRequired], nodeId: [validateRequired] },
  )
  const blocked = hasErrors(baseErrors) || hasErrors(varErrors)

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      toast.success(t('templates.market.created', { name }))
      onClose()
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || t('instances.createFailed'))
    },
  })

  const copyCommand = async () => {
    try {
      await navigator.clipboard.writeText(derivedCommand)
      setCopied(true)
      toast.success(t('templates.market.copied'))
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error(t('templates.market.copyFailed'))
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (blocked) return
    // MC 类型工作目录由系统分配；其它类型回填模板默认工作目录（可为空则交后端处理）。
    create.mutate({
      nodeId: Number(nodeId),
      name,
      type: template.type,
      processType: 'daemon',
      startCommand: derivedCommand,
      workDir: isMc ? '' : template.defaultWorkDir || '',
      autoRestart: true,
      groupId: groupId ? Number(groupId) : undefined,
    })
  }

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span className={cn('flex size-7 items-center justify-center rounded-md', coverBgClass(meta.tone))}>
              {(() => {
                const Icon = MARKET_ICONS[meta.icon]
                return <Icon className={cn('size-4', coverFgClass(meta.tone))} />
              })()}
            </span>
            {t('templates.market.deployTitle', { name: template.name })}
          </DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            {/* 需求摘要 */}
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
              <span className={cn('rounded-full px-2 py-0.5 text-[10px] font-medium', coverBgClass(meta.tone), coverFgClass(meta.tone))}>
                {meta.typeLabel}
              </span>
              {meta.java && (
                <span className="inline-flex items-center gap-1">
                  <Coffee className="size-3.5" />
                  {meta.java}
                </span>
              )}
              {meta.ram && (
                <span className="inline-flex items-center gap-1">
                  <MemoryStick className="size-3.5" />
                  {t('templates.market.ramShort', { value: meta.ram })}
                </span>
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel required htmlFor="apply-name">{t('instances.instanceName')}</FieldLabel>
              <Input
                id="apply-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="survival"
                aria-invalid={!!baseErrors.name}
              />
              <FieldError error={baseErrors.name} />
            </div>

            <div className="space-y-1.5">
              <FieldLabel required htmlFor="apply-node">{t('instances.node')}</FieldLabel>
              <Combobox
                id="apply-node"
                options={nodeOptions}
                value={nodeId}
                onChange={setNodeId}
                allowCustom={false}
                placeholder={t('instances.selectNode')}
                invalid={!!baseErrors.nodeId}
              />
              <FieldError error={baseErrors.nodeId} />
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="apply-group">{t('instances.group')}</FieldLabel>
              <Combobox
                id="apply-group"
                options={groupOptions}
                value={groupId}
                onChange={setGroupId}
                allowCustom={false}
                placeholder={t('instances.noGroup')}
              />
            </div>

            {/* 变量填充（仅当 startCommand 含占位时） */}
            {vars.length > 0 && (
              <div className="space-y-2 rounded-lg border border-dashed p-3">
                <p className="text-xs font-medium text-foreground">{t('templates.market.variablesTitle')}</p>
                <p className="text-[11px] text-muted-foreground">{t('templates.market.variablesHint')}</p>
                {vars.map((v) => (
                  <div key={v} className="space-y-1">
                    <FieldLabel required htmlFor={`apply-var-${v}`}>
                      <span className="font-mono">{`{{${v}}}`}</span>
                    </FieldLabel>
                    <Input
                      id={`apply-var-${v}`}
                      value={values[v] ?? ''}
                      onChange={(e) => setValues((prev) => ({ ...prev, [v]: e.target.value }))}
                      placeholder={v}
                      aria-invalid={!!varErrors[v]}
                    />
                  </div>
                ))}
              </div>
            )}

            {/* startCommand 派生预览：可复制 + 展开查看完整命令 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <FieldLabel>{t('templates.startCommand')}</FieldLabel>
                <div className="flex items-center gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    className="h-6 px-1.5 text-muted-foreground"
                    onClick={() => setShowFullCmd((s) => !s)}
                  >
                    {showFullCmd ? t('templates.market.collapse') : t('templates.market.expand')}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    className="text-muted-foreground"
                    onClick={copyCommand}
                    aria-label={t('templates.market.copy')}
                  >
                    {copied ? <Check className="text-status-success" /> : <Copy />}
                  </Button>
                </div>
              </div>
              <pre
                className={cn(
                  'rounded-lg bg-muted p-2 font-mono text-xs text-foreground',
                  showFullCmd ? 'whitespace-pre-wrap break-all' : 'overflow-hidden text-ellipsis whitespace-nowrap',
                )}
              >
                {derivedCommand}
              </pre>
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || blocked}>
              <Zap />
              {create.isPending ? t('common.creating') : t('templates.market.deploy')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** 新建模板对话框：名称/类型/描述/启动命令/下载URL/默认工作目录。 */
function CreateTemplateDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const { data: templates } = useTemplates()
  const create = useCreateTemplate()
  const [name, setName] = useState('')
  const [type, setType] = useState('minecraft_java')
  const [description, setDescription] = useState('')
  const [startCommand, setStartCommand] = useState('')
  const [downloadUrl, setDownloadUrl] = useState('')
  const [defaultWorkDir, setDefaultWorkDir] = useState('')

  // 类型可编辑下拉：内置常用类型 + 已有模板出现过的类型去重（FR-072）。
  const typeOptions: ComboboxOption[] = Array.from(
    new Set(['minecraft_java', 'generic', ...((templates ?? []).map((tpl) => tpl.type).filter(Boolean))]),
  ).map((v) => ({ value: v }))

  const errors = validateFields(
    { name, type, startCommand, downloadUrl, defaultWorkDir },
    {
      name: [validateRequired],
      type: [validateRequired],
      startCommand: [validateRequired],
      downloadUrl: [validateUrl],
      defaultWorkDir: [validateAbsPath],
    },
  )

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    create.mutate(
      {
        name,
        type,
        description: description || undefined,
        startCommand,
        downloadUrl: downloadUrl || undefined,
        defaultWorkDir: defaultWorkDir || undefined,
      },
      {
        onSuccess: () => {
          toast.success(t('templates.created'))
          onClose()
        },
        onError: () => toast.error(t('templates.createFailed')),
      },
    )
  }

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('templates.createTitle')}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-name">{t('templates.name')}</FieldLabel>
              <Input
                id="tpl-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('templates.namePlaceholder')}
                aria-invalid={!!errors.name}
              />
              <FieldError error={errors.name} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-type">{t('templates.type')}</FieldLabel>
              <Combobox
                id="tpl-type"
                options={typeOptions}
                value={type}
                onChange={setType}
                placeholder={t('templates.typePlaceholder')}
                invalid={!!errors.type}
              />
              <FieldError error={errors.type} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-desc">{t('templates.description')}</FieldLabel>
              <Input
                id="tpl-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t('templates.descriptionPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-cmd">{t('templates.startCommand')}</FieldLabel>
              <Textarea
                id="tpl-cmd"
                value={startCommand}
                onChange={(e) => setStartCommand(e.target.value)}
                placeholder={t('templates.startCommandPlaceholder')}
                className="font-mono text-xs"
                rows={2}
                aria-invalid={!!errors.startCommand}
              />
              <FieldError error={errors.startCommand} />
              <p className="text-[11px] text-muted-foreground">{t('templates.market.variablesSyntaxHint')}</p>
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-url">{t('templates.downloadUrl')}</FieldLabel>
              <Input
                id="tpl-url"
                value={downloadUrl}
                onChange={(e) => setDownloadUrl(e.target.value)}
                placeholder={t('templates.downloadUrlPlaceholder')}
                aria-invalid={!!errors.downloadUrl}
              />
              <FieldError error={errors.downloadUrl} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-workdir">{t('templates.defaultWorkDir')}</FieldLabel>
              <Input
                id="tpl-workdir"
                value={defaultWorkDir}
                onChange={(e) => setDefaultWorkDir(e.target.value)}
                placeholder={t('templates.defaultWorkDirPlaceholder')}
                aria-invalid={!!errors.defaultWorkDir}
              />
              <FieldError error={errors.defaultWorkDir} />
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || hasErrors(errors)}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
