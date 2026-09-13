import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Download, Users } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@jianmanager/ui/components/sheet'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@jianmanager/ui/components/table'
import ClientDistExportButton from '@/components/ClientDistExportButton'
import {
  useClientDistMachines, useClientMachineEvents, type ClientMachineSummary, type MachineSortField,
} from '@/api/clientDistMachines'
import type { TFunction } from 'i18next'

type TFunc = TFunction

/**
 * 机器级更新清单与钻取（FR-426）：「用户」= 机器（machineId）。
 * 窗口内每台机器的更新次数 / 最近更新 / 版本滞后；点行看该机器更新事件时间线。
 * 明细保留窗 14 天：exact=false 时 UI 明示「近似」（ADR-049）。
 */

function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function resultBadge(t: TFunc, result: string) {
  switch (result) {
    case 'success': return <Badge className="bg-emerald-600">{t('clientDistObs.resultSuccess', '成功')}</Badge>
    case 'fail-static': return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('clientDistObs.resultFailStatic', '失败静态')}</Badge>
    case 'rollback': return <Badge variant="outline">{t('clientDistObs.resultRollback', '回滚')}</Badge>
    default: return <Badge variant="destructive">{t('clientDistObs.resultError', '错误')}</Badge>
  }
}

function lagBadge(t: TFunc, lag: number) {
  if (lag <= 0) return <Badge variant="outline" className="text-emerald-600">{t('clientDistObs.lagLatest', '已最新')}</Badge>
  if (lag >= 3) return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('clientDistObs.lagBehind', '落后 {{n}} 版', { n: lag })}</Badge>
  return <Badge variant="outline" className="text-muted-foreground">{t('clientDistObs.lagBehind', '落后 {{n}} 版', { n: lag })}</Badge>
}

export function MachineListPanel({ channelId, from, to }: { channelId?: string; from: string; to: string }) {
  const { t } = useTranslation()
  const [sort, setSort] = useState<MachineSortField>('updates')
  const [order, setOrder] = useState<'asc' | 'desc'>('desc')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<ClientMachineSummary | null>(null)

  const query = useClientDistMachines({ channelId, from, to, sort, order, page, pageSize: 20 })
  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / 20))

  const toggleSort = (field: MachineSortField) => {
    if (sort === field) {
      setOrder((o) => (o === 'desc' ? 'asc' : 'desc'))
    } else {
      setSort(field)
      setOrder('desc')
    }
    setPage(1)
  }

  const sortIcon = (field: MachineSortField) => (sort === field ? (order === 'desc' ? ' ↓' : ' ↑') : '')

  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          <Users className="size-4" />
          {t('clientDistObs.machineTitle', '机器更新排行')}
        </span>
      }
      actions={
        <div className="flex items-center gap-2">
          {query.data && !query.data.exact && (
            <Badge variant="outline" className="border-amber-500/50 text-amber-600">
              {t('clientDistObs.approxWindow', '超出 14 天明细窗：近似统计')}
            </Badge>
          )}
          <ClientDistExportButton kind="machine-updates" filters={{ channelId: channelId ?? '', from, to }} />
        </div>
      }
    >
      <Table data-testid="machine-list">
        <TableHeader>
          <TableRow>
            <TableHead className="w-32">{t('clientDistObs.colMachine', '机器')}</TableHead>
            <TableHead>{t('clientDistObs.colPlayer', '玩家')}</TableHead>
            <TableHead className="cursor-pointer" onClick={() => toggleSort('updates')}>
              {t('clientDistObs.colUpdates', '更新次数')}{sortIcon('updates')}
            </TableHead>
            <TableHead className="cursor-pointer" onClick={() => toggleSort('lastUpdateAt')}>
              {t('clientDistObs.colLastUpdate', '最近更新')}{sortIcon('lastUpdateAt')}
            </TableHead>
            <TableHead>{t('clientDistObs.colVersion', '客户端版本')}</TableHead>
            <TableHead className="cursor-pointer" onClick={() => toggleSort('versionLag')}>
              {t('clientDistObs.colLag', '版本滞后')}{sortIcon('versionLag')}
            </TableHead>
            <TableHead>{t('clientDistObs.colBytes', '下载量')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((m) => (
            <TableRow key={m.machineId} className="cursor-pointer" onClick={() => setSelected(m)} data-testid="machine-row">
              <TableCell className="font-mono text-xs text-muted-foreground">{m.machineId}</TableCell>
              <TableCell data-testid="machine-player">
                {m.playerName
                  ? <span className="font-medium">{m.playerName}</span>
                  : <span className="text-xs text-muted-foreground">{t('clientDistObs.noPlayer', '未上报')}</span>}
              </TableCell>
              <TableCell className="font-medium tabular-nums">{m.updates.toLocaleString()}</TableCell>
              <TableCell className="tabular-nums">{fmtTime(m.lastUpdateAt)}</TableCell>
              <TableCell className="tabular-nums">v{m.currentVersion}</TableCell>
              <TableCell>{lagBadge(t, m.versionLag)}</TableCell>
              <TableCell className="tabular-nums">{fmtBytes(m.downloadBytes)}</TableCell>
            </TableRow>
          ))}
          {!query.isLoading && items.length === 0 && (
            <TableRow>
              <TableCell colSpan={7} className="py-8 text-center text-sm text-muted-foreground">
                {t('clientDistObs.machineEmpty', '所选时间段内没有机器更新记录')}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      {total > 20 && (
        <div className="mt-2 flex items-center justify-end gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            <ChevronLeft className="size-3.5" />
          </Button>
          <span className="text-xs tabular-nums text-muted-foreground">{page} / {totalPages}</span>
          <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>
            <ChevronRight className="size-3.5" />
          </Button>
        </div>
      )}

      <Sheet open={!!selected} onOpenChange={(open) => !open && setSelected(null)}>
        <SheetContent className="w-[480px] overflow-y-auto sm:max-w-[480px]">
          <SheetHeader>
            <SheetTitle>
              {t('clientDistObs.machineTimelineTitle', '机器更新时间线')}
              {selected?.playerName && (
                <Badge className="ml-2 bg-emerald-600">{selected.playerName}</Badge>
              )}
            </SheetTitle>
          </SheetHeader>
          {selected && (
            <div className="space-y-3">
              {/* 身份与来源（FR-426 增补）：机器哈希仅近似标识，玩家名/安装 ID 来自遥测与运行态画像。 */}
              <div className="grid grid-cols-2 gap-x-3 gap-y-1 rounded-lg border p-3 text-xs">
                <div className="min-w-0">
                  <span className="text-muted-foreground">{t('clientDistObs.colMachine', '机器')}: </span>
                  <code className="break-all">{selected.machineId}</code>
                </div>
                <div>
                  <span className="text-muted-foreground">{t('clientDistObs.colPlayer', '玩家')}: </span>
                  {selected.playerName || <span className="text-muted-foreground">{t('clientDistObs.noPlayer', '未上报')}</span>}
                </div>
                <div className="min-w-0">
                  <span className="text-muted-foreground">{t('clientDistObs.drawerInstallId', '安装 ID')}: </span>
                  {selected.installId
                    ? <code className="break-all">{selected.installId}</code>
                    : <span className="text-muted-foreground">{t('clientDistObs.noPlayer', '未上报')}</span>}
                </div>
                <div>
                  <span className="text-muted-foreground">{t('clientDistObs.drawerIp', '最近 IP')}: </span>
                  <code>{selected.ip || '—'}</code>
                </div>
                <div>
                  <span className="text-muted-foreground">{t('clientDistObs.drawerChannel', '频道')}: </span>
                  {selected.channelId}
                </div>
                <div>
                  <span className="text-muted-foreground">{t('clientDistObs.colVersion', '客户端版本')}: </span>
                  <span className="tabular-nums">
                    v{selected.currentVersion} → v{selected.latestVersion}
                  </span>
                </div>
              </div>
              <MachineTimeline machineId={selected.machineId} channelId={selected.channelId} from={from} to={to} />
            </div>
          )}
        </SheetContent>
      </Sheet>
    </Panel>
  )
}

function MachineTimeline({ machineId, channelId, from, to }: { machineId: string; channelId: string; from: string; to: string }) {
  const { t } = useTranslation()
  const query = useClientMachineEvents({ machineId, channelId, from, to })
  const items = query.data?.items ?? []

  if (query.isLoading) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>
  }
  if (items.length === 0) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistObs.timelineEmpty', '该时间段内无更新事件')}</p>
  }

  return (
    <div className="mt-2 space-y-0" data-testid="machine-timeline">
      {items.map((ev, i) => (
        <div key={`${ev.ts}-${i}`} className="relative flex gap-3 pb-4 pl-1">
          {i < items.length - 1 && <div className="absolute left-[7px] top-5 h-full w-px bg-border" />}
          <div className="z-10 mt-1 size-3.5 shrink-0 rounded-full border-2 border-background"
            style={{ backgroundColor: ev.result === 'success' ? '#059669' : ev.result === 'error' ? '#dc2626' : '#d97706' }}
          />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              {resultBadge(t, ev.result)}
              <span className="text-xs tabular-nums text-muted-foreground">{fmtTime(ev.ts)}</span>
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {t('clientDistObs.timelineMeta', '目标 v{{version}} · {{bytes}} · {{sec}}s', {
                version: ev.version,
                bytes: fmtBytes(ev.bytes),
                sec: (ev.durationMs / 1000).toFixed(1),
              })}
            </div>
          </div>
        </div>
      ))}
      <div className="flex items-center gap-1.5 pt-1 text-xs text-muted-foreground">
        <Download className="size-3" />
        {t('clientDistObs.timelineTotal', '共 {{n}} 次更新记录', { n: items.length })}
      </div>
    </div>
  )
}

export default MachineListPanel
