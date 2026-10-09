import { useState } from 'react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Users } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@jianmanager/ui/components/sheet'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@jianmanager/ui/components/table'
import type { ClientMachineSummary, MachineSortField } from '@/lib/client-dist/client-dist-machines-contracts'
import { fmtBytes, fmtTime, lagBadge } from '@/components/views/client-dist/machine-format'

/**
 * 机器级更新清单与钻取（FR-426）：「用户」= 机器（machineId）。
 * 窗口内每台机器的更新次数 / 最近更新 / 版本滞后；点行看该机器更新事件时间线。
 * 明细保留窗 14 天：exact=false 时 UI 明示「近似」（ADR-049）。
 *
 * 展示层：取数、查询状态与时间线容器均由应用侧提供（受控）。
 */
export function MachineListPanelView({
  items,
  total,
  exact,
  isLoading,
  sort,
  order,
  page,
  onToggleSort,
  onPageChange,
  exportSlot,
  renderTimeline,
}: {
  items: ClientMachineSummary[]
  total: number
  /** 窗口是否落在明细保留窗内（false 时明示近似统计）。 */
  exact?: boolean
  isLoading: boolean
  sort: MachineSortField
  order: 'asc' | 'desc'
  page: number
  onToggleSort: (field: MachineSortField) => void
  onPageChange: (page: number) => void
  /** 导出按钮（应用侧组件）。 */
  exportSlot?: ReactNode
  /** 机器事件时间线（应用侧取数容器），按选中机器渲染。 */
  renderTimeline: (machine: ClientMachineSummary) => ReactNode
}) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<ClientMachineSummary | null>(null)
  const totalPages = Math.max(1, Math.ceil(total / 20))

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
          {exact === false && (
            <Badge variant="outline" className="border-amber-500/50 text-amber-600">
              {t('clientDistObs.approxWindow', '超出 14 天明细窗：近似统计')}
            </Badge>
          )}
          {exportSlot}
        </div>
      }
    >
      <Table data-testid="machine-list">
        <TableHeader>
          <TableRow>
            <TableHead className="w-32">{t('clientDistObs.colMachine', '机器')}</TableHead>
            <TableHead>{t('clientDistObs.colPlayer', '玩家')}</TableHead>
            <TableHead className="cursor-pointer hover:text-foreground" onClick={() => onToggleSort('updates')}>
              {t('clientDistObs.colUpdates', '更新次数')}{sortIcon('updates')}
            </TableHead>
            <TableHead className="cursor-pointer hover:text-foreground" onClick={() => onToggleSort('lastUpdateAt')}>
              {t('clientDistObs.colLastUpdate', '最近更新')}{sortIcon('lastUpdateAt')}
            </TableHead>
            <TableHead>{t('clientDistObs.colVersion', '客户端版本')}</TableHead>
            <TableHead className="cursor-pointer hover:text-foreground" onClick={() => onToggleSort('versionLag')}>
              {t('clientDistObs.colLag', '版本滞后')}{sortIcon('versionLag')}
            </TableHead>
            <TableHead>{t('clientDistObs.colBytes', '下载量')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((m) => (
            <TableRow key={m.machineId} className="cursor-pointer transition-colors hover:bg-accent/50" onClick={() => setSelected(m)} data-testid="machine-row">
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
          {!isLoading && items.length === 0 && (
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
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            <ChevronLeft className="size-3.5" />
          </Button>
          <span className="text-xs tabular-nums text-muted-foreground">{page} / {totalPages}</span>
          <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => onPageChange(page + 1)}>
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
              {renderTimeline(selected)}
            </div>
          )}
        </SheetContent>
      </Sheet>
    </Panel>
  )
}
