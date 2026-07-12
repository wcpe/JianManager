import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Cpu, Package, RefreshCw, Trash2, Upload } from 'lucide-react'

import {
  useRuntimeAssetsOverview,
  useRefreshRuntimeAssets,
  useDeleteRuntimeJDK,
  useDeleteAsset,
  useImportAsset,
  type AssetInfo,
  type AssetType,
  type JDKMatrixItem,
  type RuntimeMatrixEntry,
} from '@/api/runtimeAssets'
import { useSearchInstances } from '@/api/instances'
import { useBatchDeployPlugins, type PluginBatchDeployResult } from '@/api/plugins'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/DangerConfirm'
import { formatRelativeTime } from '@/lib/relative-time'
import {
  formatBytes,
  buildRuntimeGrid,
  filterAssetGroups,
  shortSha,
  DEFAULT_ASSET_FILTER,
  RUNTIME_TYPE_LABEL,
  type AssetFilter,
} from './runtime-assets-view'

/** API 错误形状（占用方提示从 message + instances 字段取）。 */
type ApiError = Error & {
  response?: {
    status?: number
    data?: { message?: string; instances?: Array<{ name: string }>; reason?: string; count?: number }
  }
}

/** 状态等级 → 色点类（实例状态前导点）。 */
const LEVEL_DOT: Record<StatusLevel, string> = {
  success: 'bg-status-success',
  warning: 'bg-status-warning',
  danger: 'bg-status-danger',
  info: 'bg-status-info',
  neutral: 'bg-muted-foreground',
}

/**
 * 运行时与制品全局页（FR-082）：把 JDK 托管（FR-033）+ 制品库（FR-045）拆为独立全局页，
 * 按实例区分引用关系并可视化。两区：JDK 跨节点矩阵 + 引用实例；制品按类型占用/去重/冷热。
 * 删除受引用项拒绝并指出占用方（复用 FR-033/045 引用保护）。
 */
export default function RuntimeAssetsPage() {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useRuntimeAssetsOverview()

  if (isLoading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }
  if (isError || !data) {
    return <p className="text-destructive">{t('runtimeAssets.loadFailed')}</p>
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold">{t('runtimeAssets.title')}</h1>
        <p className="text-xs text-muted-foreground">{t('runtimeAssets.subtitle')}</p>
      </div>

      <JDKSection jdks={data.jdks} summary={data.jdkSummary} runtimes={data.runtimes} syncedAt={data.syncedAt} />
      <AssetSection groups={data.assets} summary={data.assetSummary} />
    </div>
  )
}

/** 一个汇总指标小卡（数字 + 标签）。 */
function StatCard({ label, value, accent }: { label: string; value: React.ReactNode; accent?: boolean }) {
  return (
    <div className="rounded-md border bg-card px-3 py-2">
      <div className={cn('text-lg font-bold tabular-nums', accent && 'text-primary')}>{value}</div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
    </div>
  )
}

/* ============================ JDK 区 ============================ */

function JDKSection({
  jdks,
  summary,
  runtimes,
  syncedAt,
}: {
  jdks: JDKMatrixItem[]
  summary: { nodeCount: number; jdkCount: number; referencedJdk: number; instanceRefs: number }
  /** 多运行时矩阵项（FR-301）：jdk + nodejs 等多类型。 */
  runtimes: RuntimeMatrixEntry[]
  /** 整体上次库存同步时间（ISO）；null=从未同步。 */
  syncedAt: string | null
}) {
  const { t } = useTranslation()
  const grid = buildRuntimeGrid(runtimes)
  const refresh = useRefreshRuntimeAssets()

  // 手动刷新（FR-301）：强制全节点 syncFromWorker。失败容忍——部分节点失败时
  // 提示失败节点名单，页面继续显示 DB 旧数据（不清空、不报错态）。
  const onRefresh = () => {
    refresh.mutate(undefined, {
      onSuccess: (outcome) => {
        const failed = outcome.results.filter((r) => !r.ok)
        if (failed.length > 0) {
          toast.warning(
            t('runtimeAssets.refreshPartial', {
              names: failed.map((f) => f.nodeName || `#${f.nodeId}`).join('、'),
            }),
          )
        } else {
          toast.success(t('runtimeAssets.refreshDone'))
        }
      },
      onError: (err: ApiError) => toast.error(err.response?.data?.message || t('runtimeAssets.refreshFailed')),
    })
  }

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Cpu className="size-4 text-muted-foreground" />
          <h2 className="text-base font-semibold">{t('runtimeAssets.jdkRegion')}</h2>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {syncedAt
              ? t('runtimeAssets.lastSync', { time: formatRelativeTime(syncedAt) })
              : t('runtimeAssets.neverSynced')}
          </span>
          <Button variant="outline" size="sm" disabled={refresh.isPending} onClick={onRefresh}>
            <RefreshCw className={cn('size-4', refresh.isPending && 'animate-spin')} />
            {t('runtimeAssets.refresh')}
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label={t('runtimeAssets.nodeCount')} value={summary.nodeCount} />
        <StatCard label={t('runtimeAssets.jdkCount')} value={summary.jdkCount} />
        <StatCard label={t('runtimeAssets.referencedJdk')} value={summary.referencedJdk} />
        <StatCard label={t('runtimeAssets.instanceRefs')} value={summary.instanceRefs} accent />
      </div>

      {runtimes.length === 0 ? (
        <Panel>
          <p className="py-8 text-center text-sm text-muted-foreground">{t('runtimeAssets.jdkEmpty')}</p>
        </Panel>
      ) : (
        /* 可视化：节点×运行时引用矩阵（FR-301 多类型）——列=类型徽章+版本，格内数字=引用实例数（非 JDK 恒 0）。 */
        <Panel title={t('runtimeAssets.runtimeMatrixTitle')} bodyClassName="p-0">
          <Table className="text-xs">
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="sticky left-0 z-10 border-r bg-muted/50">{t('runtimeAssets.node')}</TableHead>
                {grid.columns.map((col) => (
                  <TableHead key={col.key} className="text-center">
                    <span
                      className={cn(
                        'rounded px-1.5 py-0.5 text-[9px] font-medium',
                        col.type === 'jdk'
                          ? 'bg-status-info/15 text-status-info'
                          : 'bg-status-success/15 text-status-success',
                      )}
                    >
                      {RUNTIME_TYPE_LABEL[col.type] ?? col.type}
                    </span>
                    <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                      {col.type === 'jdk' ? `${col.label} ${col.majorVersion}` : `v${col.majorVersion}`}
                    </div>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {grid.rows.map((row) => (
                <TableRow key={row.nodeId}>
                  <TableCell className="sticky left-0 z-10 border-r bg-card whitespace-nowrap">
                    <span className="inline-flex items-center gap-1.5">
                      <span
                        className={cn(
                          'size-1.5 rounded-full',
                          row.nodeOnline ? 'bg-status-success' : 'bg-muted-foreground',
                        )}
                      />
                      {row.nodeName || `#${row.nodeId}`}
                    </span>
                  </TableCell>
                  {grid.columns.map((col) => {
                    const cell = row.cells[col.key]
                    if (!cell) {
                      return (
                        <TableCell key={col.key} className="text-center text-muted-foreground/40">·</TableCell>
                      )
                    }
                    // 引用越多色越深（冷热可视）：0=灰，>0=主色调深浅。
                    const hot = cell.refCount > 0
                    return (
                      <TableCell key={col.key} className="text-center">
                        <span
                          className={cn(
                            'inline-flex min-w-7 items-center justify-center rounded px-1.5 py-0.5 font-mono',
                            hot ? 'bg-primary/15 font-semibold text-primary' : 'bg-muted text-muted-foreground',
                          )}
                          title={cell.items.map((it) => `${it.version || it.name} · ${it.refCount}`).join('\n')}
                        >
                          {cell.refCount}
                        </span>
                      </TableCell>
                    )
                  })}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {/* 明细：每个 JDK + 其引用实例（引用关系下钻 + 删除占用方提示）。 */}
      {jdks.length > 0 && (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          {jdks.map((j) => (
            <JDKCard key={j.id} jdk={j} />
          ))}
        </div>
      )}
    </section>
  )
}

function JDKCard({ jdk }: { jdk: JDKMatrixItem }) {
  const { t } = useTranslation()
  const del = useDeleteRuntimeJDK()
  const [confirming, setConfirming] = useState(false)

  const onDelete = () => {
    del.mutate(
      { nodeId: jdk.nodeId, jdkId: jdk.id },
      {
        onSuccess: () => toast.success(t('runtimeAssets.jdkDeleted')),
        onError: (err: ApiError) => {
          const occupants = err.response?.data?.instances?.map((i) => i.name).join('、')
          if (err.response?.status === 409 && occupants) {
            toast.error(t('runtimeAssets.jdkInUse', { names: occupants }))
          } else {
            toast.error(err.response?.data?.message || t('runtimeAssets.deleteFailed'))
          }
        },
      },
    )
    setConfirming(false)
  }

  return (
    <Panel
      title={
        <span className="flex items-center gap-2">
          <span className="text-foreground">
            {jdk.vendor} {jdk.majorVersion}
          </span>
          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] font-normal text-muted-foreground">
            {jdk.version || '—'}
          </span>
          {jdk.managed && (
            <span className="rounded bg-status-info/15 px-1.5 py-0.5 text-[10px] font-normal text-status-info">
              {t('runtimeAssets.managed')}
            </span>
          )}
        </span>
      }
      actions={
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground hover:text-destructive"
          disabled={del.isPending}
          onClick={() => setConfirming(true)}
          aria-label={t('common.delete')}
        >
          <Trash2 />
        </Button>
      }
      bodyClassName="space-y-2 p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <span
            className={cn('size-1.5 rounded-full', jdk.nodeOnline ? 'bg-status-success' : 'bg-muted-foreground')}
          />
          {jdk.nodeName || `#${jdk.nodeId}`}
        </span>
        <span>{jdk.arch || '—'}</span>
        <span className="font-mono">{jdk.refCount} {t('runtimeAssets.refs')}</span>
      </div>
      <div className="overflow-hidden text-ellipsis whitespace-nowrap rounded bg-muted p-1.5 font-mono text-[11px]">
        {jdk.path}
      </div>
      {jdk.instances.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {jdk.instances.map((inst) => (
            <span
              key={inst.id}
              className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]"
              title={`${inst.binding === 'direct' ? t('runtimeAssets.bindDirect') : t('runtimeAssets.bindMajor')} · ${inst.uuid}`}
            >
              <span className={cn('size-1.5 shrink-0 rounded-full', LEVEL_DOT[instanceStatusLevel(inst.status)])} />
              {inst.name}
              <span className="text-muted-foreground/70">
                {inst.binding === 'direct' ? t('runtimeAssets.bindDirectShort') : t('runtimeAssets.bindMajorShort')}
              </span>
            </span>
          ))}
        </div>
      ) : (
        <p className="text-[11px] text-muted-foreground">{t('runtimeAssets.noRefs')}</p>
      )}

      <DangerConfirm
        open={confirming}
        title={jdk.managed
          ? t('nodes.jdkDeleteFilesTitle', '删除 JDK（含文件）?')
          : t('nodes.jdkDeleteRecordTitle', '删除 JDK 登记记录?')}
        description={jdk.managed
          ? t('nodes.jdkDeleteFilesDesc', '该 JDK 由平台下载托管，删除将一并移除 Worker 上的文件，不可恢复。请输入「厂商 主版本」确认。')
          : t('nodes.jdkDeleteRecordDesc', '外部登记的 JDK 仅删除平台记录，不影响磁盘上的 JDK 文件。')}
        confirmLabel={t('common.delete')}
        confirmText={jdk.managed ? `${jdk.vendor} ${jdk.majorVersion}` : undefined}
        onConfirm={onDelete}
        onCancel={() => setConfirming(false)}
      />
    </Panel>
  )
}

/* ============================ 制品区 ============================ */

/** 制品类型筛选选项（含「全部」）。 */
const ASSET_TYPES: Array<AssetType | 'all'> = [
  'all',
  'core',
  'plugin',
  'image',
  'video',
  'archive',
  'blob',
  'client-file',
]

function AssetSection({
  groups,
  summary,
}: {
  groups: import('@/api/runtimeAssets').AssetTypeGroup[]
  summary: {
    assetCount: number
    totalSize: number
    referencedCount: number
    hotCount: number
    archivedCount: number
    externalCount: number
  }
}) {
  const { t } = useTranslation()
  const [filter, setFilter] = useState<AssetFilter>(DEFAULT_ASSET_FILTER)
  const [importOpen, setImportOpen] = useState(false)
  const filtered = filterAssetGroups(groups, filter)
  const pluginAssets = groups.find((g) => g.type === 'plugin')?.items ?? []

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Package className="size-4 text-muted-foreground" />
          <h2 className="text-base font-semibold">{t('runtimeAssets.assetRegion')}</h2>
        </div>
        <Button variant="outline" size="sm" onClick={() => setImportOpen(true)}>
          <Upload className="size-4" />
          {t('runtimeAssets.import')}
        </Button>
      </div>
      {importOpen && <ImportAssetDialog open={importOpen} onOpenChange={setImportOpen} />}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label={t('runtimeAssets.assetCount')} value={summary.assetCount} />
        <StatCard label={t('runtimeAssets.totalSize')} value={formatBytes(summary.totalSize)} accent />
        <StatCard label={t('runtimeAssets.referencedAssets')} value={summary.referencedCount} />
        <StatCard
          label={t('runtimeAssets.hotCold')}
          value={
            <span className="text-sm">
              {summary.hotCount}
              <span className="text-muted-foreground"> / {summary.archivedCount + summary.externalCount}</span>
            </span>
          }
        />
      </div>

      {/* 筛选：类型 + 仅被引用 + 搜索（按实例/类型筛选的「类型」维度 + 内容搜索）。 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-1">
          {ASSET_TYPES.map((ty) => (
            <button
              key={ty}
              type="button"
              onClick={() => setFilter((f) => ({ ...f, type: ty }))}
              className={cn(
                'rounded px-2 py-0.5 text-xs transition-colors',
                filter.type === ty
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted text-muted-foreground hover:bg-accent',
              )}
            >
              {ty === 'all' ? t('runtimeAssets.typeAll') : ty}
            </button>
          ))}
        </div>
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={filter.onlyReferenced}
            onChange={(e) => setFilter((f) => ({ ...f, onlyReferenced: e.target.checked }))}
          />
          {t('runtimeAssets.onlyReferenced')}
        </label>
        <Input
          value={filter.search}
          onChange={(e) => setFilter((f) => ({ ...f, search: e.target.value }))}
          placeholder={t('runtimeAssets.searchPlaceholder')}
          className="h-8 w-48 text-xs"
        />
      </div>

      {filtered.length === 0 ? (
        <Panel>
          <p className="py-8 text-center text-sm text-muted-foreground">
            {summary.assetCount === 0 ? t('runtimeAssets.assetEmpty') : t('runtimeAssets.noMatch')}
          </p>
        </Panel>
      ) : (
        filtered.map((g) => (
          <Panel
            key={g.type}
            title={
              <span className="flex items-center gap-2">
                <span className="font-mono text-foreground">{g.type}</span>
                <span className="font-normal text-muted-foreground">
                  {g.items.length} · {formatBytes(g.totalSize)}
                </span>
              </span>
            }
            bodyClassName="p-0"
          >
            <Table className="text-xs">
              <TableHeader className="bg-muted/40">
                <TableRow>
                  <TableHead>{t('runtimeAssets.name')}</TableHead>
                  <TableHead>{t('runtimeAssets.version')}</TableHead>
                  <TableHead>{t('runtimeAssets.sha256')}</TableHead>
                  <TableHead className="text-right">{t('runtimeAssets.size')}</TableHead>
                  <TableHead className="text-center">{t('runtimeAssets.storage')}</TableHead>
                  <TableHead className="text-center">{t('runtimeAssets.refs')}</TableHead>
                  <TableHead className="text-right">{t('common.actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {g.items.map((a) => (
                  <AssetRow key={a.id} asset={a} pluginAssets={pluginAssets} />
                ))}
              </TableBody>
            </Table>
          </Panel>
        ))
      )}
    </section>
  )
}

/** 可导入的制品类型（不含 client-file：客户端文件走分发页专属发布流程，FR-088/251）。 */
const IMPORTABLE_ASSET_TYPES: AssetType[] = ['core', 'plugin', 'image', 'video', 'archive', 'blob']

/** 已上传字节 / 总字节 → 百分比（0~100，钳制）。 */
function importPercent(loaded: number, total: number): number {
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((loaded / total) * 100)))
}

/**
 * 导入制品弹窗（FR-155：补齐制品导入下载进度）。
 * 选类型 + 选本地文件后走 multipart 入库（POST /assets），上传期间显示实时进度条，
 * 进度由 axios onUploadProgress 驱动（与插件上传同一机制）。成功后 overview 失效刷新，新制品即刻出现。
 * 遵循模态纪律（ui-modals）：内容自适应壳 + 内部滚动，头/脚固定。
 */
function ImportAssetDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const importAsset = useImportAsset()
  const [type, setType] = useState<AssetType>('core')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [file, setFile] = useState<File | null>(null)
  // 已上传/总字节；null=尚未开始上传（不渲染进度条）。
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null)

  const submit = () => {
    if (!file) return
    setProgress({ loaded: 0, total: file.size })
    importAsset.mutate(
      {
        type,
        file,
        name: name.trim() || undefined,
        version: version.trim() || undefined,
        onProgress: (loaded, total) => setProgress({ loaded, total: total || file.size }),
      },
      {
        onSuccess: () => {
          toast.success(t('runtimeAssets.importDone'))
          onOpenChange(false)
        },
        onError: (err: ApiError) =>
          toast.error(err.response?.data?.message || t('runtimeAssets.importFailed')),
        onSettled: () => setProgress(null),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('runtimeAssets.importTitle')}</DialogTitle>
          <DialogDescription>{t('runtimeAssets.importDescription')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-3">
          <label className="block space-y-1 text-sm">
            <span className="font-medium">{t('runtimeAssets.importType')}</span>
            <select
              value={type}
              onChange={(e) => setType(e.target.value as AssetType)}
              disabled={importAsset.isPending}
              className="h-9 w-full rounded-md border bg-background px-2 text-sm"
            >
              {IMPORTABLE_ASSET_TYPES.map((ty) => (
                <option key={ty} value={ty}>{ty}</option>
              ))}
            </select>
          </label>
          <label className="block space-y-1 text-sm">
            <span className="font-medium">{t('runtimeAssets.importFile')}</span>
            <input
              type="file"
              aria-label={t('runtimeAssets.importFile')}
              disabled={importAsset.isPending}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              className="block w-full text-sm text-muted-foreground file:mr-3 file:rounded-md file:border file:bg-muted file:px-3 file:py-1.5 file:text-sm"
            />
          </label>
          <div className="grid grid-cols-2 gap-2">
            <label className="block space-y-1 text-sm">
              <span className="font-medium">{t('runtimeAssets.importName')}</span>
              <Input value={name} onChange={(e) => setName(e.target.value)} disabled={importAsset.isPending} className="h-9" />
            </label>
            <label className="block space-y-1 text-sm">
              <span className="font-medium">{t('runtimeAssets.importVersion')}</span>
              <Input value={version} onChange={(e) => setVersion(e.target.value)} disabled={importAsset.isPending} className="h-9" />
            </label>
          </div>
          {progress && (
            <div role="status" aria-label={t('runtimeAssets.importProgressLabel')}>
              <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
                <span className="truncate">{file?.name}</span>
                <span className="font-mono tabular-nums">{importPercent(progress.loaded, progress.total)}%</span>
              </div>
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full bg-primary transition-[width]"
                  style={{ width: `${importPercent(progress.loaded, progress.total)}%` }}
                />
              </div>
            </div>
          )}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={importAsset.isPending}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} disabled={!file || importAsset.isPending}>
            {importAsset.isPending ? t('runtimeAssets.importing') : t('runtimeAssets.import')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function parseAssetMetadata(raw: string): { path?: string; codec?: string } {
  if (!raw) return {}
  try {
    const value = JSON.parse(raw) as { path?: string; codec?: string; targetPath?: string }
    return { path: value.path || value.targetPath, codec: value.codec }
  } catch {
    return {}
  }
}

function AssetRow({ asset, pluginAssets }: { asset: AssetInfo; pluginAssets: AssetInfo[] }) {
  const { t } = useTranslation()
  const del = useDeleteAsset()
  const [confirming, setConfirming] = useState(false)
  const [deployOpen, setDeployOpen] = useState(false)
  const referenced = asset.refCount > 0

  const onDelete = () => {
    del.mutate(asset.id, {
      onSuccess: () => toast.success(t('runtimeAssets.assetDeleted')),
      onError: (err: ApiError) => {
        if (err.response?.status === 409) {
          const reason = err.response.data?.reason
          const count = err.response.data?.count ?? asset.refCount
          const key = reason ? `runtimeAssets.assetInUseReasons.${reason}` : 'runtimeAssets.assetInUse'
          toast.error(t(key, { count, defaultValue: t('runtimeAssets.assetInUse', { count }) }))
        } else {
          toast.error(err.response?.data?.message || t('runtimeAssets.deleteFailed'))
        }
      },
    })
    setConfirming(false)
  }

  const storageLabel =
    asset.storageState === 'archived'
      ? t('runtimeAssets.storageArchived')
      : asset.storageState === 'external'
        ? t('runtimeAssets.storageExternal')
        : t('runtimeAssets.storageHot')
  const metadata = parseAssetMetadata(asset.metadata)
  const clientFileInfo = asset.type === 'client-file'
    ? [asset.filename, metadata.path, metadata.codec].filter(Boolean).join(' · ')
    : ''

  return (
    <TableRow>
      <TableCell>
        <div>{asset.name || asset.filename || '—'}</div>
        {clientFileInfo && <div className="mt-0.5 max-w-64 truncate text-[11px] text-muted-foreground" title={clientFileInfo}>{clientFileInfo}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground">{asset.version || '—'}</TableCell>
      <TableCell className="font-mono text-[11px] text-muted-foreground" title={asset.sha256}>
        {shortSha(asset.sha256)}
      </TableCell>
      <TableCell className="text-right tabular-nums">{formatBytes(asset.size)}</TableCell>
      <TableCell className="text-center">
        <span
          className={cn(
            'rounded px-1.5 py-0.5 text-[10px]',
            asset.storageState === 'hot'
              ? 'bg-status-success/15 text-status-success'
              : 'bg-muted text-muted-foreground',
          )}
        >
          {storageLabel}
        </span>
      </TableCell>
      <TableCell className="text-center">
        <span
          className={cn(
            'inline-flex min-w-6 justify-center rounded px-1.5 py-0.5 font-mono text-[11px]',
            referenced ? 'bg-primary/15 font-semibold text-primary' : 'bg-muted text-muted-foreground',
          )}
          title={referenced ? t('runtimeAssets.assetRefHint', { count: asset.refCount }) : t('runtimeAssets.noRefs')}
        >
          {asset.refCount}
        </span>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          {asset.type === 'plugin' && (
            <Button variant="outline" size="xs" onClick={() => setDeployOpen(true)}>
              {t('plugins.batchDeploy.action')}
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground hover:text-destructive"
            disabled={referenced || del.isPending}
            onClick={() => setConfirming(true)}
            aria-label={t('common.delete')}
            title={referenced ? t('runtimeAssets.assetDeleteReferenced', { count: asset.refCount }) : undefined}
          >
            <Trash2 />
          </Button>
        </div>
        {asset.type === 'plugin' && deployOpen && (
          <PluginBatchDeployDialog
            open={deployOpen}
            initialAsset={asset}
            pluginAssets={pluginAssets}
            onOpenChange={setDeployOpen}
          />
        )}
        <DangerConfirm
          open={confirming}
          title={t('runtimeAssets.assetDeleteConfirm', { name: asset.name || asset.filename })}
          description={
            referenced
              ? t('runtimeAssets.assetDeleteReferenced', { count: asset.refCount })
              : t('runtimeAssets.assetDeleteDescription')
          }
          confirmLabel={t('common.delete')}
          onConfirm={onDelete}
          onCancel={() => setConfirming(false)}
        />
      </TableCell>
    </TableRow>
  )
}

function PluginBatchDeployDialog({
  open,
  initialAsset,
  pluginAssets,
  onOpenChange,
}: {
  open: boolean
  initialAsset: AssetInfo
  pluginAssets: AssetInfo[]
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const deploy = useBatchDeployPlugins()
  const [targetQuery, setTargetQuery] = useState('')
  const { data: instanceSearch, isLoading } = useSearchInstances(
    { q: targetQuery || undefined, page: 1, pageSize: 200, sort: 'name', order: 'asc' },
    open,
  )
  const instances = instanceSearch?.items ?? []
  const [selectedAssetIds, setSelectedAssetIds] = useState<number[]>([initialAsset.id])
  const [selectedInstanceIds, setSelectedInstanceIds] = useState<number[]>([])
  const [confirmOpen, setConfirmOpen] = useState(false)

  const toggleAsset = (id: number, checked: boolean) => {
    setSelectedAssetIds((ids) => (checked ? [...new Set([...ids, id])] : ids.filter((item) => item !== id)))
  }
  const toggleInstance = (id: number, checked: boolean) => {
    setSelectedInstanceIds((ids) => (checked ? [...new Set([...ids, id])] : ids.filter((item) => item !== id)))
  }
  const submit = () => {
    if (selectedAssetIds.length === 0 || selectedInstanceIds.length === 0) return
    setConfirmOpen(true)
  }
  const confirmDeploy = () => {
    deploy.mutate({ assetIds: selectedAssetIds, target: { ids: selectedInstanceIds } })
    setConfirmOpen(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle>{t('plugins.batchDeploy.title')}</DialogTitle>
          <DialogDescription>{t('plugins.batchDeploy.description')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="grid gap-4 py-2 md:grid-cols-2">
          <section className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t('plugins.batchDeploy.assets')}</h3>
              <span className="text-xs text-muted-foreground">
                {t('plugins.batchDeploy.selectedAssets', { count: selectedAssetIds.length })}
              </span>
            </div>
            <div className="space-y-2 rounded border p-2">
              {pluginAssets.map((asset) => (
                <label key={asset.id} className="flex cursor-pointer items-start gap-2 rounded p-2 hover:bg-muted/60">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selectedAssetIds.includes(asset.id)}
                    onChange={(e) => toggleAsset(asset.id, e.target.checked)}
                  />
                  <span className="min-w-0 text-sm">
                    <span className="block font-medium">{asset.name || asset.filename}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {asset.filename} · {formatBytes(asset.size)}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </section>

          <section className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t('plugins.batchDeploy.targets')}</h3>
              <span className="text-xs text-muted-foreground">
                {t('plugins.batchDeploy.selectedTargets', { count: selectedInstanceIds.length })}
              </span>
            </div>
            <Input
              value={targetQuery}
              onChange={(e) => setTargetQuery(e.target.value)}
              placeholder={t('plugins.batchDeploy.targetSearch')}
              className="h-8 text-xs"
            />
            <div className="max-h-72 space-y-2 overflow-auto rounded border p-2">
              {isLoading && (
                <p className="p-2 text-sm text-muted-foreground">{t('plugins.batchDeploy.loadingTargets')}</p>
              )}
              {!isLoading && instances.length === 0 && (
                <p className="p-2 text-sm text-muted-foreground">{t('plugins.batchDeploy.noTargets')}</p>
              )}
              {instances.map((inst) => (
                <label key={inst.id} className="flex cursor-pointer items-start gap-2 rounded p-2 hover:bg-muted/60">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selectedInstanceIds.includes(inst.id)}
                    onChange={(e) => toggleInstance(inst.id, e.target.checked)}
                  />
                  <span className="min-w-0 text-sm">
                    <span className="block font-medium">{inst.name}</span>
                    <span className="block text-xs text-muted-foreground">
                      #{inst.id} · {inst.status} · {t('runtimeAssets.node')} {inst.nodeId}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </section>
          {confirmOpen && (
            <div
              role="alert"
              className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive md:col-span-2"
            >
              <div className="font-medium">{t('plugins.batchDeploy.confirmTitle')}</div>
              <p className="mt-1 text-xs">
                {t('plugins.batchDeploy.confirmDescription', {
                  assetCount: selectedAssetIds.length,
                  instanceCount: selectedInstanceIds.length,
                })}
              </p>
            </div>
          )}
          {deploy.data && <PluginBatchDeployResultPanel result={deploy.data} />}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant={confirmOpen ? 'destructive' : 'default'}
            onClick={confirmOpen ? confirmDeploy : submit}
            disabled={deploy.isPending || selectedAssetIds.length === 0 || selectedInstanceIds.length === 0}
          >
            {deploy.isPending
              ? t('plugins.batchDeploy.submitting')
              : confirmOpen
                ? t('plugins.batchDeploy.confirmLabel')
                : t('plugins.batchDeploy.submit', { count: selectedInstanceIds.length })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PluginBatchDeployResultPanel({ result }: { result: PluginBatchDeployResult }) {
  const { t } = useTranslation()
  return (
    <div role="status" className="rounded border border-status-success/30 bg-status-success/10 p-3 text-xs md:col-span-2">
      <div className="font-medium text-status-success">
        {t('plugins.batchDeploy.result', { success: result.success, skipped: result.skipped, failed: result.failed })}
      </div>
      <div className="mt-1 text-muted-foreground">
        {result.results.map((item) => (
          <span key={item.id} className="mr-3 inline-block">
            {item.name}: {item.error || item.reason || t('plugins.batchDeploy.ok')}
          </span>
        ))}
      </div>
    </div>
  )
}
