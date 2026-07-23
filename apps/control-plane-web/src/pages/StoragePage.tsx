import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  Folder,
  HardDrive,
  RefreshCw,
  Snowflake,
  Trash2,
} from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'

import {
  useStorageOverview,
  clearStorageCache,
  type DirUsage,
  type StorageOverview,
} from '@/api/storage'
import { useAuthStore } from '@/stores/auth'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/DangerConfirm'
import UnifiedExplorerShell from '@/components/file-browser/UnifiedExplorerShell'
import { storageBrowseCapability } from '@/components/file-browser/capability'
import { storageFileSource } from '@/components/file-browser/sources/storageSource'
import {
  formatBytes,
  deriveArchive,
  sortDirsByUsage,
} from './storage-view'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 平台存储资源管理器（FR-083）：对 CP 侧数据根（ADR-010 FHS 布局）只读浏览 + 占用统计
 * + 制品归档冷热可见 + cache 受控清理（二次确认）。
 *
 * 数据根是平台级资源（仅 CP 读写，见架构不变量），故整页仅平台管理员可见，后端 RBAC 同样收敛。
 * 浏览走平台存储端点（/storage/*），不复用实例级文件 API；Worker 侧数据根（var/servers、
 * opt/jdks 落各节点本机）按节点经既有实例文件管理浏览，不在此页范围。
 */
export default function StoragePage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const { data, isLoading, isError } = useStorageOverview()
  // FR-378：FileBrowser 不走 react-query，清理 cache 后靠 refreshKey 重拉树。
  const [browseRefreshKey, setBrowseRefreshKey] = useState(0)
  const bumpBrowse = () => setBrowseRefreshKey((k) => k + 1)

  if (role !== ROLE_PLATFORM_ADMIN) {
    return <p className="text-muted-foreground">{t('storage.adminOnly')}</p>
  }
  if (isLoading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }
  if (isError || !data) {
    return <p className="text-destructive">{t('storage.loadFailed')}</p>
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-2">
        <HardDrive className="mt-0.5 size-5 text-muted-foreground" />
        <div className="min-w-0">
          <h1 className="text-xl font-bold">{t('storage.title')}</h1>
          <p className="text-xs text-muted-foreground">{t('storage.subtitle')}</p>
          <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground/70" title={data.base}>
            {data.base}
          </p>
        </div>
      </div>

      <OverviewSection data={data} />
      <DirUsageSection data={data} onCacheCleared={bumpBrowse} />
      <ArchiveSection data={data} />
      <BrowserSection refreshKey={browseRefreshKey} onRefresh={bumpBrowse} />
    </div>
  )
}

/** 一个汇总指标小卡（数字 + 标签）。 */
function StatCard({
  label,
  value,
  accent,
}: {
  label: string
  value: React.ReactNode
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
  onCacheCleared,
}: {
  data: StorageOverview
  onCacheCleared?: () => void
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
            <DirRow key={d.path} dir={d} onCacheCleared={onCacheCleared} />
          ))}
        </TableBody>
      </Table>
    </Panel>
  )
}

function DirRow({ dir, onCacheCleared }: { dir: DirUsage; onCacheCleared?: () => void }) {
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
          <ClearCacheButton size={dir.size} fileCount={dir.fileCount} onCleared={onCacheCleared} />
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
  onCleared,
}: {
  size: number
  fileCount: number
  onCleared?: () => void
}) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const empty = fileCount === 0

  const onClear = async () => {
    setConfirming(false)
    setBusy(true)
    try {
      const removed = await clearStorageCache()
      toast.success(t('storage.cacheCleared', { count: removed }))
      // 清理后占用变化，刷新概览；并通知浏览壳重拉（FR-378 FileBrowser）。
      qc.invalidateQueries({ queryKey: ['storage', 'overview'] })
      qc.invalidateQueries({ queryKey: ['storage', 'files'] })
      onCleared?.()
    } catch {
      toast.error(t('storage.clearFailed'))
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
        onConfirm={() => void onClear()}
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
}: {
  refreshKey: number
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  const source = useMemo(
    () => storageFileSource({ noPreview: t('storage.noContentPreview') }),
    [t],
  )
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
        source={source}
        refreshKey={refreshKey}
        className="min-h-[360px]"
      />
    </Panel>
  )
}
