import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Puzzle, Store, UploadCloud } from 'lucide-react'
import { toast } from 'sonner'
import {
  usePlugins,
  useUploadPlugin,
  useDeletePlugin,
  useTogglePlugin,
  usePluginBatchDeploy,
  type PluginInfo,
} from '@/api/plugins'
import { useRuntimeAssetsOverview, type AssetInfo } from '@/api/runtimeAssets'
import { useInstanceSearch, type InstanceInfo } from '@/api/instances'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { cn } from '@jianmanager/ui'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import DangerConfirm from '@/components/DangerConfirm'

const MANAGED_DIRS = ['plugins', 'mods', 'resourcepacks', 'datapacks'] as const

type ManagedDir = (typeof MANAGED_DIRS)[number]

const UPLOAD_ACCEPT: Record<ManagedDir, string> = {
  plugins: '.jar',
  mods: '.jar',
  resourcepacks: '.zip',
  datapacks: '.zip',
}

interface UploadCandidate {
  file: File
  dir: ManagedDir
}

interface UploadProgress {
  fileName: string
  loaded: number
  total: number
}

/** 插件/模组单服管理面板（FR-052）：列表 + 启用/禁用 + 上传 + 删除（二次确认）。 */
interface PluginManagerProps {
  /** 当前实例 id */
  instanceId: number
}

/**
 * 分栏（FR-423，spec §3.1 插件 62:38）：左 62% 是插件表（5 列 × 4 个目录分组，横向要空间、纵向要行数），
 * 右 38% 收「需关注」（重启提示）+ 上传投放 + 插件市场——这些都是内容驱动的窄块，
 * 原先横铺整宽压在表格上方，把表格挤到折叠线以下（重启横幅 + 投放区 ≈ 110px 纯装饰高度）。
 *
 * 高度策略统一为「内容驱动高度 + 栏高封顶（`max-h-full min-h-0 flex-none`）+ 卡内滚动」（spec §3.2）：
 * 插件多时撑到栏高上限后表体内部滚，少时贴合内容；右栏三块一律 `flex-none`，
 * 空白落在栏位空隙而非卡片内部。
 */
export default function PluginManager({ instanceId }: PluginManagerProps) {
  const { t } = useTranslation()
  const { data: plugins, isLoading, error } = usePlugins(instanceId)
  const upload = useUploadPlugin(instanceId)
  const toggle = useTogglePlugin(instanceId)
  const remove = useDeletePlugin(instanceId)

  const fileInputRef = useRef<HTMLInputElement>(null)
  // 上传目标目录：插件 / 模组 / 资源包 / 数据包。
  const [uploadDir, setUploadDir] = useState<ManagedDir>('plugins')
  // 待删除插件（用于二次确认对话框）。
  const [deleteTarget, setDeleteTarget] = useState<PluginInfo | null>(null)
  const [overwriteTarget, setOverwriteTarget] = useState<UploadCandidate | null>(null)
  const [dragActive, setDragActive] = useState(false)
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(null)
  const [batchDeployOpen, setBatchDeployOpen] = useState(false)
  const grouped = MANAGED_DIRS.map((dir) => ({ dir, items: (plugins || []).filter((p) => p.dir === dir) }))

  const onPickFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (file) handleUploadFile(file, uploadDir)
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const handleUploadFile = (file: File, dir: ManagedDir, overwrite = false) => {
    if (!isAcceptedUpload(file, dir)) {
      toast.error(t('plugins.invalidUploadType', { extensions: UPLOAD_ACCEPT[dir] }))
      return
    }
    if (!overwrite && plugins?.some((p) => p.dir === dir && p.name === file.name)) {
      setOverwriteTarget({ file, dir })
      return
    }
    setUploadProgress({ fileName: file.name, loaded: 0, total: file.size })
    upload.mutate(
      {
        file,
        dir,
        overwrite,
        onProgress: (loaded, total) => {
          setUploadProgress({ fileName: file.name, loaded, total: total || file.size })
        },
      },
      {
        onSettled: () => setUploadProgress(null),
      },
    )
  }

  const onDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    setDragActive(false)
    const file = e.dataTransfer.files?.[0]
    if (file) handleUploadFile(file, uploadDir)
  }

  const confirmDelete = () => {
    if (deleteTarget) remove.mutate({ name: deleteTarget.name, dir: deleteTarget.dir })
    setDeleteTarget(null)
  }

  const confirmOverwrite = () => {
    if (overwriteTarget) handleUploadFile(overwriteTarget.file, overwriteTarget.dir, true)
    setOverwriteTarget(null)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 xl:flex-row">
      {/* 左栏 62%：插件表。批量部署留在卡头 actions——它作用于表内容，滚动时也需常驻可点。 */}
      <div className="flex flex-none flex-col xl:min-h-0 xl:flex-[1.6]">
        <Panel
          className="max-h-full min-h-0 flex-none"
          bodyClassName="flex min-h-0 flex-col overflow-hidden p-0"
          icon={<Puzzle className="size-3.5" />}
          title={t('plugins.title')}
          actions={
            <Button size="sm" variant="outline" onClick={() => setBatchDeployOpen(true)}>
              {t('plugins.batchDeploy.action')}
            </Button>
          }
        >
          {isLoading ? (
            <p className="p-3 text-sm text-muted-foreground">{t('plugins.loading')}</p>
          ) : error ? (
            <p className="p-3 text-sm text-destructive">
              {(error as Error & { response?: { data?: { message?: string } } }).response?.data?.message ||
                t('plugins.loadFailed')}
            </p>
          ) : !plugins || plugins.length === 0 ? (
            /* 空态高度按内容走（spec §3.2）：去掉原 p-8 虚线大方块，不再为一句话占固定高度。 */
            <EmptyState icon={<Puzzle />} title={t('plugins.empty')} />
          ) : (
            <div className="min-h-0 space-y-3 overflow-auto p-3">
              {grouped.map(({ dir, items }) => (
                <section key={dir} aria-label={dirLabel(t, dir)} className="rounded-lg border">
                  <div className="flex items-center gap-2 border-b bg-muted/50 px-3 py-2">
                    <h3 className="text-sm font-semibold">{dirLabel(t, dir)}</h3>
                    <Badge variant="outline">{items.length}</Badge>
                  </div>
                  {items.length === 0 ? (
                    <p className="px-3 py-3 text-center text-sm text-muted-foreground">{t('plugins.sectionEmpty')}</p>
                  ) : (
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>{t('plugins.name')}</TableHead>
                          <TableHead>{t('plugins.metadata')}</TableHead>
                          <TableHead className="w-24">{t('plugins.status')}</TableHead>
                          <TableHead className="w-24 text-right">{t('plugins.size')}</TableHead>
                          <TableHead className="w-44 text-right">{t('plugins.actions')}</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {items.map((p) => (
                          <TableRow key={`${p.dir}/${p.name}`}>
                            <TableCell>
                              <div className="font-mono text-xs">{p.name}</div>
                              <div className="mt-1 text-xs text-muted-foreground">{p.dir}</div>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {pluginMetadata(p)}
                            </TableCell>
                            <TableCell>
                              {p.enabled ? (
                                <Badge className="bg-emerald-500/15 text-emerald-600 dark:text-emerald-400">
                                  {t('plugins.statusEnabled')}
                                </Badge>
                              ) : (
                                <Badge variant="secondary">{t('plugins.statusDisabled')}</Badge>
                              )}
                            </TableCell>
                            <TableCell className="text-right tabular-nums text-xs text-muted-foreground">
                              {formatSize(p.size)}
                            </TableCell>
                            <TableCell className="text-right">
                              <div className="flex justify-end gap-1.5">
                                <Button
                                  size="xs"
                                  variant="outline"
                                  disabled={toggle.isPending}
                                  onClick={() => toggle.mutate({ name: p.name, dir: p.dir })}
                                >
                                  {p.enabled ? t('plugins.disable') : t('plugins.enable')}
                                </Button>
                                <Button
                                  size="xs"
                                  variant="ghost"
                                  className="text-destructive hover:text-destructive"
                                  disabled={remove.isPending}
                                  onClick={() => setDeleteTarget(p)}
                                >
                                  {t('plugins.delete')}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  )}
                </section>
              ))}
            </div>
          )}
        </Panel>
      </div>

      {/* 右栏 38%：需关注 + 上传投放 + 市场。xl 以下退成整页纵向堆叠，权重只在 xl 生效——
          窄屏若仍按权重分高度，两栏会互相挤压，页面该滚的时候滚不动。 */}
      <div className="flex flex-none flex-col gap-2 xl:min-h-0 xl:flex-1">
        {/* 需关注：重启才生效是本页最容易踩的坑，原先是横铺整宽的琥珀条，现在收成一张窄卡。 */}
        <Panel
          className="flex-none"
          tone="warning"
          icon={<AlertTriangle className="size-3.5" />}
          title={t('plugins.noticeTitle')}
        >
          <p className="text-xs text-status-warning">{t('plugins.restartHint')}</p>
        </Panel>

        {/* 上传投放：目录选择 / 上传按钮 / 拖拽区 / 进度条同属一件事，合到一张卡里，
            不再让「选目录」在页头、「拖到这里」在页中隔着一条横幅。 */}
        <Panel
          className="flex-none"
          bodyClassName="flex flex-col gap-2 p-3"
          icon={<UploadCloud className="size-3.5" />}
          title={t('plugins.upload')}
        >
          <div className="flex flex-wrap items-center gap-2">
            <Select value={uploadDir} onValueChange={(v) => setUploadDir(v as ManagedDir)}>
              <SelectTrigger size="sm" className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MANAGED_DIRS.map((dir) => (
                  <SelectItem key={dir} value={dir}>
                    {dirLabel(t, dir)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button size="sm" disabled={upload.isPending} onClick={() => fileInputRef.current?.click()}>
              {t('plugins.upload')}
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept={UPLOAD_ACCEPT[uploadDir]}
              className="hidden"
              onChange={onPickFile}
            />
          </div>
          <div
            data-testid="plugin-dropzone"
            className={cn(
              'rounded-lg border border-dashed px-3 py-2.5 text-xs transition-colors',
              dragActive ? 'border-primary bg-primary/5 text-primary' : 'border-muted-foreground/30 text-muted-foreground',
            )}
            onDragEnter={(e) => {
              e.preventDefault()
              setDragActive(true)
            }}
            onDragOver={(e) => {
              e.preventDefault()
              e.dataTransfer.dropEffect = 'copy'
            }}
            onDragLeave={() => setDragActive(false)}
            onDrop={onDrop}
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>{t('plugins.dropUploadHint', { dir: dirLabel(t, uploadDir), extensions: UPLOAD_ACCEPT[uploadDir] })}</span>
              {uploadProgress && (
                <span className="font-mono text-xs" role="status">
                  {t('plugins.uploadProgress', {
                    name: uploadProgress.fileName,
                    percent: uploadPercent(uploadProgress),
                  })}
                </span>
              )}
            </div>
            {uploadProgress && (
              <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary transition-[width]" style={{ width: `${uploadPercent(uploadProgress)}%` }} />
              </div>
            )}
          </div>
        </Panel>

        {/* 市场：入口尚未接通（按钮 disabled），单独一张窄卡说明缘由，不再占页头一格。 */}
        <Panel className="flex-none" bodyClassName="flex flex-col items-start gap-2 p-3" icon={<Store className="size-3.5" />} title={t('plugins.market')}>
          <p className="text-xs text-muted-foreground">{t('plugins.marketSoon')}</p>
          <Button size="sm" variant="outline" disabled title={t('plugins.marketSoon')}>
            <Store className="mr-1 size-3.5" aria-hidden="true" />
            {t('plugins.market')}
          </Button>
        </Panel>
      </div>

      <DangerConfirm
        open={!!deleteTarget}
        title={t('plugins.deleteTitle')}
        description={t('plugins.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        confirmLabel={t('plugins.delete')}
        confirmText={deleteTarget?.name ?? ''}
        scope="group"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
      <DangerConfirm
        open={!!overwriteTarget}
        title={t('plugins.overwriteTitle')}
        description={t('plugins.overwriteConfirm', { name: overwriteTarget?.file.name ?? '' })}
        confirmLabel={t('plugins.overwrite')}
        onConfirm={confirmOverwrite}
        onCancel={() => setOverwriteTarget(null)}
      />
      {batchDeployOpen && (
        <PluginBatchDeployDialog
          defaultTargetId={instanceId}
          onOpenChange={setBatchDeployOpen}
        />
      )}
    </div>
  )
}

function PluginBatchDeployDialog({
  defaultTargetId,
  onOpenChange,
}: {
  defaultTargetId: number
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const assetsQuery = useRuntimeAssetsOverview()
  const [query, setQuery] = useState('')
  const instancesQuery = useInstanceSearch({ q: query, pageSize: 20, sort: 'name', order: 'asc' }, true)
  const batchDeploy = usePluginBatchDeploy()
  const [selectedAssets, setSelectedAssets] = useState<Set<number>>(new Set())
  const [selectedInstances, setSelectedInstances] = useState<Set<number>>(new Set([defaultTargetId]))
  const [destination, setDestination] = useState<'plugins' | 'mods'>('plugins')
  const [overwrite, setOverwrite] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const suppressParentCloseRef = useRef(false)

  const pluginAssets = useMemo(
    () => assetsQuery.data?.assets.find((g) => g.type === 'plugin')?.items ?? [],
    [assetsQuery.data],
  )
  const instances = instancesQuery.data?.items ?? []
  const result = batchDeploy.data
  const canSubmit = selectedAssets.size > 0 && selectedInstances.size > 0 && !batchDeploy.isPending

  const toggleAsset = (id: number) => {
    setSelectedAssets((current) => toggleSetValue(current, id))
  }
  const toggleInstance = (id: number) => {
    setSelectedInstances((current) => toggleSetValue(current, id))
  }

  const handleDialogOpenChange = (open: boolean) => {
    if (!open && (confirming || suppressParentCloseRef.current)) return
    onOpenChange(open)
  }
  const suppressParentClose = () => {
    suppressParentCloseRef.current = true
    setTimeout(() => {
      suppressParentCloseRef.current = false
    }, 200)
  }
  const submit = () => {
    if (!canSubmit) return
    suppressParentClose()
    setConfirming(true)
  }
  const cancelConfirm = () => {
    suppressParentClose()
    setConfirming(false)
  }
  const confirmDeploy = () => {
    if (!canSubmit) return
    suppressParentClose()
    setConfirming(false)
    batchDeploy.mutate({
      assetIds: [...selectedAssets],
      target: { ids: [...selectedInstances] },
      destination,
      overwrite,
    })
  }

  return (
    <>
      <Dialog open onOpenChange={handleDialogOpenChange}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
          <DialogHeader>
            <DialogTitle>{t('plugins.batchDeployTitle')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-4">
          <section className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">{t('plugins.batchAssets')}</h3>
              <Badge variant="outline">{selectedAssets.size}</Badge>
            </div>
            {assetsQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : pluginAssets.length === 0 ? (
              <p className="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground">
                {t('plugins.batchNoAssets')}
              </p>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {pluginAssets.map((asset) => (
                  <PluginBatchAssetOption
                    key={asset.id}
                    asset={asset}
                    checked={selectedAssets.has(asset.id)}
                    onCheckedChange={() => toggleAsset(asset.id)}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-semibold">{t('plugins.batchTargets')}</h3>
              <Badge variant="outline">{selectedInstances.size}</Badge>
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('plugins.batchSearchTargets')}
                className="ml-auto h-8 w-56 text-xs"
              />
            </div>
            {instancesQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : (
              <div className="grid max-h-56 gap-2 overflow-auto pr-1 sm:grid-cols-2">
                {instances.map((instance) => (
                  <PluginBatchInstanceOption
                    key={instance.id}
                    instance={instance}
                    checked={selectedInstances.has(instance.id)}
                    onCheckedChange={() => toggleInstance(instance.id)}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">{t('plugins.batchDestination')}</span>
              <Select value={destination} onValueChange={(v) => setDestination(v as 'plugins' | 'mods')}>
                <SelectTrigger size="sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="plugins">{t('plugins.dirPlugins')}</SelectItem>
                  <SelectItem value="mods">{t('plugins.dirMods')}</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className="flex items-center gap-2 self-end rounded-md border p-2 text-xs">
              <Checkbox checked={overwrite} onCheckedChange={(v) => setOverwrite(v === true)} />
              <span>{t('plugins.batchOverwrite')}</span>
            </label>
          </section>

          {result && (
            <div className="rounded-md border bg-muted/30 p-3 text-xs">
              <div className="font-semibold">
                {t('plugins.batchResult', {
                  succeeded: result.succeeded,
                  failed: result.failed,
                  skipped: result.skipped,
                })}
              </div>
              <div className="mt-1 text-muted-foreground">
                {t('plugins.batchRequested', {
                  instances: result.requestedInstances,
                  assets: result.requestedAssets,
                })}
              </div>
            </div>
          )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button disabled={!canSubmit} onClick={submit}>
              {batchDeploy.isPending ? t('plugins.batchDeploying') : t('plugins.batchSubmit')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <DangerConfirm
        open={confirming}
        title={t('plugins.batchConfirmTitle')}
        description={t('plugins.batchConfirmDesc', {
          instances: selectedInstances.size,
          assets: selectedAssets.size,
        })}
        confirmLabel={t('plugins.batchConfirm')}
        onConfirm={confirmDeploy}
        onCancel={cancelConfirm}
      />
    </>
  )
}

function PluginBatchAssetOption({
  asset,
  checked,
  onCheckedChange,
}: {
  asset: AssetInfo
  checked: boolean
  onCheckedChange: () => void
}) {
  const { t } = useTranslation()
  return (
    <label className="flex cursor-pointer items-start gap-2 rounded-md border p-2 text-xs hover:bg-accent/40">
      <Checkbox
        checked={checked}
        onCheckedChange={onCheckedChange}
        aria-label={t('plugins.batchSelectAsset', { name: asset.filename })}
      />
      <span className="min-w-0">
        <span className="block truncate font-medium">{asset.filename}</span>
        <span className="block text-muted-foreground">
          {asset.version || '—'} · {formatSize(asset.size)}
        </span>
      </span>
    </label>
  )
}

function PluginBatchInstanceOption({
  instance,
  checked,
  onCheckedChange,
}: {
  instance: InstanceInfo
  checked: boolean
  onCheckedChange: () => void
}) {
  const { t } = useTranslation()
  return (
    <label className="flex cursor-pointer items-center gap-2 rounded-md border p-2 text-xs hover:bg-accent/40">
      <Checkbox
        checked={checked}
        onCheckedChange={onCheckedChange}
        aria-label={t('plugins.batchSelectInstance', { name: instance.name })}
      />
      <span className="min-w-0">
        <span className="block truncate font-medium">{instance.name}</span>
        <span className="block text-muted-foreground">
          {instance.status} · {instance.role}
        </span>
      </span>
    </label>
  )
}

function toggleSetValue(values: Set<number>, value: number): Set<number> {
  const next = new Set(values)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  return next
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function uploadPercent(progress: UploadProgress): number {
  if (progress.total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((progress.loaded / progress.total) * 100)))
}

function isAcceptedUpload(file: File, dir: ManagedDir): boolean {
  return UPLOAD_ACCEPT[dir].split(',').some((ext) => file.name.toLowerCase().endsWith(ext.trim().toLowerCase()))
}

function dirLabel(t: ReturnType<typeof useTranslation>['t'], dir: ManagedDir | string): string {
  const key: Record<string, string> = {
    plugins: 'plugins.dirPlugins',
    mods: 'plugins.dirMods',
    resourcepacks: 'plugins.dirResourcepacks',
    datapacks: 'plugins.dirDatapacks',
  }
  return t(key[dir] || 'plugins.dirPlugins')
}

function pluginMetadata(p: PluginInfo): string {
  const parts = [p.version, p.author, ...(p.dependencies || [])].filter(Boolean)
  return parts.length > 0 ? parts.join(' · ') : '—'
}
