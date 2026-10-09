import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Folder,
  HardDrive,
  RefreshCw,
  Snowflake,
  Trash2,
} from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import UnifiedExplorerShell from '@/components/views/file-browser/UnifiedExplorerShell'
import { storageBrowseCapability } from '@/lib/file-browser/file-browser-capability'
import type { FileBrowserSource } from '@/lib/file-browser/file-browser-types'
import { formatBytes, deriveArchive, sortDirsByUsage } from '@/lib/file-browser/storage-view'
import type { DirUsage, StorageOverview } from '@/lib/file-browser/storage-types'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type StorageNotice = (kind: 'success' | 'error', message: string) => void

export interface StoragePageProps {
  /** 是否平台管理员（外壳从 auth store 读出；非管理员只渲染一行说明）。 */
  isPlatformAdmin: boolean
  /** 概览数据。 */
  overview?: StorageOverview
  isLoading: boolean
  isError: boolean
  /** 清空 cache/；返回删除条目数。 */
  onClearCache: () => Promise<number>
  /** 清理成功后通知外壳失效 `['storage','overview']` 与 `['storage','files']` 缓存。 */
  onCacheCleared?: () => void
  /** 平台存储只读数据源（外壳注入取数）。 */
  storageSource: FileBrowserSource
  /** 透传给内层统一浏览壳的注入项（主题 + 标签宿主依赖）。 */
  shellDeps: Omit<
    Parameters<typeof UnifiedExplorerShell>[0],
    'capability' | 'source' | 'refreshKey' | 'className'
  >
  /** 提示通道。 */
  notify: StorageNotice
}

/**
 * 平台存储资源管理器（FR-083）：对 CP 侧数据根（ADR-010 FHS 布局）只读浏览 + 占用统计
 * + 制品归档冷热可见 + cache 受控清理（二次确认）。
 *
 * 数据根是平台级资源（仅 CP 读写，见架构不变量），故整页仅平台管理员可见，后端 RBAC 同样收敛。
 * 浏览走平台存储端点（/storage/*），不复用实例级文件 API；Worker 侧数据根（var/servers、
 * opt/jdks 落各节点本机）按节点经既有实例文件管理浏览，不在此页范围。
 *
 * 受控视图（ADR-097）：概览取数、cache 清理、权限判定、数据源与浏览壳依赖全部由外壳注入。
 */
export default function StoragePage({
  isPlatformAdmin,
  overview: data,
  isLoading,
  isError,
  onClearCache,
  onCacheCleared,
  storageSource,
  shellDeps,
  notify,
}: StoragePageProps) {
  const { t } = useTranslation()
  // FR-378：FileBrowser 不走 react-query，清理 cache 后靠 refreshKey 重拉树。
  const [browseRefreshKey, setBrowseRefreshKey] = useState(0)
  const bumpBrowse = () => setBrowseRefreshKey((k) => k + 1)

  if (!isPlatformAdmin) {
    return <p className="text-muted-foreground">{t('storage.adminOnly')}</p>
  }
  if (isLoading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }
  if (isError || !data) {
    return <p className="text-destructive">{t('storage.loadFailed')}</p>
  }

  return (
    // 全量对齐：外壳与页头改用布局层原语。原先无 data-page，迁移时补上。
    // 图标随 title 进 h1（PageHeader 无独立 icon 槽，且该图标无 aria 语义）；
    // 存储基址由独立一行降为描述的一部分（等宽），仍保留 title 供悬停看全。
    <PageShell data-page="storage">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <HardDrive className="size-5 text-muted-foreground" />
            {t('storage.title')}
          </span>
        }
        description={
          <>
            {t('storage.subtitle')} · <span className="font-mono" title={data.base}>{data.base}</span>
          </>
        }
      />

      <OverviewSection data={data} />
      <DirUsageSection
        data={data}
        onClearCache={onClearCache}
        onCacheCleared={() => {
          bumpBrowse()
          onCacheCleared?.()
        }}
        notify={notify}
      />
      <ArchiveSection data={data} />
      <BrowserSection
        refreshKey={browseRefreshKey}
        onRefresh={bumpBrowse}
        storageSource={storageSource}
        shellDeps={shellDeps}
      />
    </PageShell>
  )
}

/** 一个汇总指标小卡（数字 + 标签）。 */
function StatCard({
  label,
  value,
  accent,
}: {
  label: string
  value: ReactNode
  accent?: boolean
}) {
  return (
    <div className="rounded-md border bg-card px-3 py-2">
      <div className={cn('text-lg font-bold tabular-nums', accent && 'text-primary')}>{value}</div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
    </div>
  )
}

/* ============================ 概览汇总 ============================ */

function OverviewSection({ data }: { data: StorageOverview }) {
  const { t } = useTranslation()
  const cold = deriveArchive(data.archive)

  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
      <StatCard label={t('storage.totalSize')} value={formatBytes(data.totalSize)} accent />
      <StatCard label={t('storage.totalFiles')} value={data.totalFiles} />
      <StatCard label={t('storage.dirCount')} value={data.dirs.length} />
      <StatCard
        label={t('storage.archiveCold')}
        value={
          <span className="text-sm">
            {cold.cold}
            <span className="text-muted-foreground"> · {formatBytes(cold.coldSize)}</span>
          </span>
        }
      />
    </div>
  )
}

/* ============================ FHS 子目录占用 ============================ */

function DirUsageSection({
  data,
  onClearCache,
  onCacheCleared,
  notify,
}: {
  data: StorageOverview
  onClearCache: () => Promise<number>
  onCacheCleared?: () => void
  notify: StorageNotice
}) {
  const { t } = useTranslation()
  const dirs = sortDirsByUsage(data.dirs)

  return (
    <Panel title={t('storage.dirsTitle')} bodyClassName="p-0">
      <Table className="text-xs">
        <TableHeader className="bg-muted/40">
          <TableRow>
            <TableHead>{t('storage.dirLabel')}</TableHead>
            <TableHead>{t('storage.dirPath')}</TableHead>
            <TableHead className="text-right">{t('storage.size')}</TableHead>
            <TableHead className="text-right">{t('storage.files')}</TableHead>
            <TableHead className="text-right">{t('common.actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {dirs.map((d) => (
            <DirRow
              key={d.path}
              dir={d}
              onClearCache={onClearCache}
              onCacheCleared={onCacheCleared}
              notify={notify}
            />
          ))}
        </TableBody>
      </Table>
    </Panel>
  )
}

function DirRow({
  dir,
  onClearCache,
  onCacheCleared,
  notify,
}: {
  dir: DirUsage
  onClearCache: () => Promise<number>
  onCacheCleared?: () => void
  notify: StorageNotice
}) {
  const { t } = useTranslation()
  // 用途标注键由后端给出（artifacts/jdks/...），i18n 缺键回退到原始键。
  const labelText = t(`storage.dirNames.${dir.label}`, { defaultValue: dir.label })

  return (
    <TableRow className={cn(!dir.exists && 'text-muted-foreground/60')}>
      <TableCell>
        <span className="inline-flex items-center gap-1.5">
          <Folder className="size-3.5 shrink-0 text-muted-foreground" />
          {labelText}
          {!dir.exists && (
            <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
              {t('storage.missing')}
            </span>
          )}
          {dir.clearable && (
            <span className="rounded bg-status-warning/15 px-1.5 py-0.5 text-[10px] text-status-warning">
              {t('storage.clearable')}
            </span>
          )}
        </span>
      </TableCell>
      <TableCell className="font-mono text-[11px] text-muted-foreground">{dir.path}</TableCell>
      <TableCell className="text-right tabular-nums">{formatBytes(dir.size)}</TableCell>
      <TableCell className="text-right tabular-nums">{dir.fileCount}</TableCell>
      <TableCell className="text-right">
        {dir.clearable ? (
          <ClearCacheButton
            size={dir.size}
            fileCount={dir.fileCount}
            onClear={onClearCache}
            onCleared={onCacheCleared}
            notify={notify}
          />
        ) : (
          <span className="text-muted-foreground/40">—</span>
        )}
      </TableCell>
    </TableRow>
  )
}

/** cache 受控清理按钮 + 二次确认（FR-059，平台范围）。 */
function ClearCacheButton({
  size,
  fileCount,
  onClear,
  onCleared,
  notify,
}: {
  size: number
  fileCount: number
  onClear: () => Promise<number>
  onCleared?: () => void
  notify: StorageNotice
}) {
  const { t } = useTranslation()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const empty = fileCount === 0

  const runClear = async () => {
    setConfirming(false)
    setBusy(true)
    try {
      const removed = await onClear()
      notify('success', t('storage.cacheCleared', { count: removed }))
      // 清理后占用变化：刷新概览由外壳负责，这里通知浏览壳重拉（FR-378 FileBrowser）。
      onCleared?.()
    } catch {
      notify('error', t('storage.clearFailed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Button
        variant="ghost"
        size="xs"
        className="gap-1 text-muted-foreground hover:text-destructive"
        disabled={busy || empty}
        onClick={() => setConfirming(true)}
      >
        <Trash2 className="size-3.5" />
        {t('storage.clearCache')}
      </Button>
      <DangerConfirm
        open={confirming}
        title={t('storage.clearCacheTitle')}
        description={t('storage.clearCacheDescription', { size: formatBytes(size), count: fileCount })}
        confirmLabel={t('storage.clearCache')}
        scope="platform"
        onConfirm={() => void runClear()}
        onCancel={() => setConfirming(false)}
      />
    </>
  )
}

/* ============================ 制品归档冷热 ============================ */

function ArchiveSection({ data }: { data: StorageOverview }) {
  const { t } = useTranslation()
  const a = data.archive

  const rows: Array<{ key: string; label: string; count: number; size: number; cold?: boolean }> = [
    { key: 'hot', label: t('storage.stateHot'), count: a.hotCount, size: a.hotSize },
    { key: 'archived', label: t('storage.stateArchived'), count: a.archivedCount, size: a.archivedSize, cold: true },
    { key: 'external', label: t('storage.stateExternal'), count: a.externalCount, size: a.externalSize, cold: true },
  ]

  return (
    <Panel
      title={
        <span className="inline-flex items-center gap-1.5">
          <Snowflake className="size-3.5 text-muted-foreground" />
          {t('storage.archiveTitle')}
        </span>
      }
      bodyClassName="p-0"
    >
      <Table className="text-xs">
        <TableHeader className="bg-muted/40">
          <TableRow>
            <TableHead>{t('storage.storageState')}</TableHead>
            <TableHead className="text-right">{t('storage.assetCount')}</TableHead>
            <TableHead className="text-right">{t('storage.size')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={r.key}>
              <TableCell>
                <span
                  className={cn(
                    'rounded px-1.5 py-0.5 text-[10px]',
                    r.cold ? 'bg-muted text-muted-foreground' : 'bg-status-success/15 text-status-success',
                  )}
                >
                  {r.label}
                </span>
              </TableCell>
              <TableCell className="text-right tabular-nums">{r.count}</TableCell>
              <TableCell className="text-right tabular-nums">{formatBytes(r.size)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Panel>
  )
}

/* ============================ 只读文件浏览 ============================ */

/**
 * 数据根浏览（FR-378）：经 UnifiedExplorerShell + storage FileBrowserSource，
 * 与实例/分发共享只读浏览器壳；cache 清理仍在页级 DangerConfirm。
 */
function BrowserSection({
  refreshKey,
  onRefresh,
  storageSource,
  shellDeps,
}: {
  refreshKey: number
  onRefresh: () => void
  storageSource: FileBrowserSource
  shellDeps: StoragePageProps['shellDeps']
}) {
  const { t } = useTranslation()
  const cap = useMemo(() => storageBrowseCapability(), [])

  return (
    <Panel
      title={t('storage.browserTitle')}
      actions={
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground"
          onClick={onRefresh}
          aria-label={t('storage.refresh')}
        >
          <RefreshCw className="size-3.5" />
        </Button>
      }
      bodyClassName="p-0"
    >
      <UnifiedExplorerShell
        capability={cap}
        source={storageSource}
        refreshKey={refreshKey}
        className="min-h-[360px]"
        {...shellDeps}
      />
    </Panel>
  )
}
