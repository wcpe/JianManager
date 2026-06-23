import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Cpu, Package, Trash2 } from 'lucide-react'

import {
  useRuntimeAssetsOverview,
  useDeleteRuntimeJDK,
  useDeleteAsset,
  type AssetInfo,
  type AssetType,
  type JDKMatrixItem,
} from '@/api/runtimeAssets'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { instanceStatusLevel, type StatusLevel } from '@/lib/threshold'
import { cn } from '@/lib/utils'
import DangerConfirm from '@/components/DangerConfirm'
import {
  formatBytes,
  buildJDKMatrix,
  filterAssetGroups,
  shortSha,
  DEFAULT_ASSET_FILTER,
  type AssetFilter,
} from './runtime-assets-view'

/** API 错误形状（占用方提示从 message + instances 字段取）。 */
type ApiError = Error & {
  response?: { status?: number; data?: { message?: string; instances?: Array<{ name: string }> } }
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

      <JDKSection jdks={data.jdks} summary={data.jdkSummary} />
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
}: {
  jdks: JDKMatrixItem[]
  summary: { nodeCount: number; jdkCount: number; referencedJdk: number; instanceRefs: number }
}) {
  const { t } = useTranslation()
  const matrix = buildJDKMatrix(jdks)

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-2">
        <Cpu className="size-4 text-muted-foreground" />
        <h2 className="text-base font-semibold">{t('runtimeAssets.jdkRegion')}</h2>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label={t('runtimeAssets.nodeCount')} value={summary.nodeCount} />
        <StatCard label={t('runtimeAssets.jdkCount')} value={summary.jdkCount} />
        <StatCard label={t('runtimeAssets.referencedJdk')} value={summary.referencedJdk} />
        <StatCard label={t('runtimeAssets.instanceRefs')} value={summary.instanceRefs} accent />
      </div>

      {jdks.length === 0 ? (
        <Panel>
          <p className="py-8 text-center text-sm text-muted-foreground">{t('runtimeAssets.jdkEmpty')}</p>
        </Panel>
      ) : (
        <>
          {/* 可视化：节点×版本引用矩阵——格内数字=该 vendor-major 在该节点上的引用实例数。 */}
          <Panel title={t('runtimeAssets.jdkMatrixTitle')} bodyClassName="overflow-x-auto p-0">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="bg-muted/50">
                  <th className="sticky left-0 z-10 border-b border-r bg-muted/50 px-3 py-2 text-left font-medium">
                    {t('runtimeAssets.node')}
                  </th>
                  {matrix.columns.map((col) => (
                    <th key={col.key} className="border-b px-3 py-2 text-center font-medium whitespace-nowrap">
                      <div>{col.vendor}</div>
                      <div className="font-mono text-[10px] text-muted-foreground">Java {col.majorVersion}</div>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {matrix.rows.map((row) => (
                  <tr key={row.nodeId} className="border-b last:border-b-0">
                    <td className="sticky left-0 z-10 border-r bg-card px-3 py-2 whitespace-nowrap">
                      <span className="inline-flex items-center gap-1.5">
                        <span
                          className={cn(
                            'size-1.5 rounded-full',
                            row.nodeOnline ? 'bg-status-success' : 'bg-muted-foreground',
                          )}
                        />
                        {row.nodeName || `#${row.nodeId}`}
                      </span>
                    </td>
                    {matrix.columns.map((col) => {
                      const cell = row.cells[col.key]
                      if (!cell) {
                        return (
                          <td key={col.key} className="px-3 py-2 text-center text-muted-foreground/40">
                            ·
                          </td>
                        )
                      }
                      // 引用越多色越深（冷热可视）：0=灰，>0=主色调深浅。
                      const hot = cell.refCount > 0
                      return (
                        <td key={col.key} className="px-3 py-2 text-center">
                          <span
                            className={cn(
                              'inline-flex min-w-7 items-center justify-center rounded px-1.5 py-0.5 font-mono',
                              hot
                                ? 'bg-primary/15 font-semibold text-primary'
                                : 'bg-muted text-muted-foreground',
                            )}
                            title={cell.items
                              .map((it) => `${it.version || it.vendor} · ${it.refCount}`)
                              .join('\n')}
                          >
                            {cell.refCount}
                          </span>
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>

          {/* 明细：每个 JDK + 其引用实例（引用关系下钻 + 删除占用方提示）。 */}
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            {jdks.map((j) => (
              <JDKCard key={j.id} jdk={j} />
            ))}
          </div>
        </>
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
        title={t('runtimeAssets.jdkDeleteConfirm', { vendor: jdk.vendor, major: jdk.majorVersion })}
        description={t('runtimeAssets.jdkDeleteDescription')}
        confirmLabel={t('common.delete')}
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
  const filtered = filterAssetGroups(groups, filter)

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-2">
        <Package className="size-4 text-muted-foreground" />
        <h2 className="text-base font-semibold">{t('runtimeAssets.assetRegion')}</h2>
      </div>

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
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead className="bg-muted/40">
                  <tr>
                    <th className="px-3 py-1.5 text-left font-medium">{t('runtimeAssets.name')}</th>
                    <th className="px-3 py-1.5 text-left font-medium">{t('runtimeAssets.version')}</th>
                    <th className="px-3 py-1.5 text-left font-medium">{t('runtimeAssets.sha256')}</th>
                    <th className="px-3 py-1.5 text-right font-medium">{t('runtimeAssets.size')}</th>
                    <th className="px-3 py-1.5 text-center font-medium">{t('runtimeAssets.storage')}</th>
                    <th className="px-3 py-1.5 text-center font-medium">{t('runtimeAssets.refs')}</th>
                    <th className="px-3 py-1.5 text-right font-medium">{t('common.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {g.items.map((a) => (
                    <AssetRow key={a.id} asset={a} />
                  ))}
                </tbody>
              </table>
            </div>
          </Panel>
        ))
      )}
    </section>
  )
}

function AssetRow({ asset }: { asset: AssetInfo }) {
  const { t } = useTranslation()
  const del = useDeleteAsset()
  const [confirming, setConfirming] = useState(false)
  const referenced = asset.refCount > 0

  const onDelete = () => {
    del.mutate(asset.id, {
      onSuccess: () => toast.success(t('runtimeAssets.assetDeleted')),
      onError: (err: ApiError) => {
        if (err.response?.status === 409) {
          toast.error(t('runtimeAssets.assetInUse', { count: asset.refCount }))
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

  return (
    <tr className="border-t">
      <td className="px-3 py-1.5">{asset.name || '—'}</td>
      <td className="px-3 py-1.5 text-muted-foreground">{asset.version || '—'}</td>
      <td className="px-3 py-1.5 font-mono text-[11px] text-muted-foreground" title={asset.sha256}>
        {shortSha(asset.sha256)}
      </td>
      <td className="px-3 py-1.5 text-right tabular-nums">{formatBytes(asset.size)}</td>
      <td className="px-3 py-1.5 text-center">
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
      </td>
      <td className="px-3 py-1.5 text-center">
        <span
          className={cn(
            'inline-flex min-w-6 justify-center rounded px-1.5 py-0.5 font-mono text-[11px]',
            referenced ? 'bg-primary/15 font-semibold text-primary' : 'bg-muted text-muted-foreground',
          )}
          title={referenced ? t('runtimeAssets.assetRefHint', { count: asset.refCount }) : t('runtimeAssets.noRefs')}
        >
          {asset.refCount}
        </span>
      </td>
      <td className="px-3 py-1.5 text-right">
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground hover:text-destructive"
          onClick={() => setConfirming(true)}
          aria-label={t('common.delete')}
        >
          <Trash2 />
        </Button>
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
      </td>
    </tr>
  )
}
