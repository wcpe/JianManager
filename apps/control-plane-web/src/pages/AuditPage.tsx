import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, Download } from 'lucide-react'
import { exportAuditLogs, useAuditLogs, type AuditLogInfo } from '@/api/audit'
import { useUsers } from '@/api/users'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { cn } from '@jianmanager/ui'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { DEFAULT_AUDIT_FILTER, toAuditParams, formatAuditDetail, type AuditFilterState } from './audit-filters'

// Radix Select 不允许空字符串值，用哨兵代表「全部用户」。
const SENTINEL_ALL = '__all__'

/**
 * 审计日志查询页（FR-015 + FR-158）。
 * 套「流水检索」范式：强筛选（用户/操作/目标类型/时间范围）→ 时间线行；行可展开看变更详情（detail）。
 * 「导出」走后端 NDJSON 白名单导出；「加载更多」逐页追加；「清空」恢复默认。
 * 后端分页 envelope 返回真实 total，页面展示已加载数与命中总数。
 */
export default function AuditPage() {
  const { t } = useTranslation()
  const { data: users } = useUsers()

  const [filter, setFilter] = useState<AuditFilterState>(DEFAULT_AUDIT_FILTER)
  const [expanded, setExpanded] = useState<number | null>(null)

  const patch = (next: Partial<AuditFilterState>) => setFilter((prev) => ({ ...prev, ...next }))

  const params = toAuditParams(filter)
  const { data, isLoading, isError, fetchNextPage, hasNextPage, isFetchingNextPage } = useAuditLogs(params)
  const logs = data?.pages.flatMap((page) => page.items) ?? []
  const total = data?.pages[0]?.total ?? logs.length

  const handleExport = async () => {
    if (logs.length === 0) return
    const blob = await exportAuditLogs(params)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `audit-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">{t('audit.title')}</h1>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={handleExport}
            disabled={logs.length === 0}
          >
            <Download className="size-3.5" />
            {t('audit.export')}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setFilter(DEFAULT_AUDIT_FILTER)}>
            {t('audit.clear')}
          </Button>
        </div>
      </div>

      {/* 强筛选器 */}
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={filter.userId === '' ? SENTINEL_ALL : filter.userId}
          onValueChange={(v: string) => patch({ userId: v === SENTINEL_ALL ? '' : v })}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('audit.allUsers')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('audit.allUsers')}</SelectItem>
            {users?.map((u) => (
              <SelectItem key={u.id} value={String(u.id)}>
                {u.username}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={filter.action}
          onChange={(e) => patch({ action: e.target.value })}
          placeholder={t('audit.actionPlaceholder')}
          className="h-9 w-44"
        />
        <Input
          value={filter.targetType}
          onChange={(e) => patch({ targetType: e.target.value })}
          placeholder={t('audit.targetTypePlaceholder')}
          className="h-9 w-44"
        />
        <Input
          type="datetime-local"
          value={filter.from}
          onChange={(e) => patch({ from: e.target.value })}
          aria-label={t('audit.from')}
          className="h-9 w-52"
        />
        <Input
          type="datetime-local"
          value={filter.to}
          onChange={(e) => patch({ to: e.target.value })}
          aria-label={t('audit.to')}
          className="h-9 w-52"
        />
      </div>

      {isLoading && !data ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('audit.loadError')}</p>
      ) : (
        <>
          <Panel bodyClassName="p-0">
            {/* 列头 */}
            <div className="flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium text-muted-foreground">
              <span className="w-4 shrink-0" />
              <span className="w-40 shrink-0">{t('audit.time')}</span>
              <span className="w-28 shrink-0">{t('audit.user')}</span>
              <span className="w-44 shrink-0">{t('audit.action')}</span>
              <span className="min-w-0 flex-1">{t('audit.target')}</span>
              <span className="w-28 shrink-0">{t('audit.ip')}</span>
            </div>
            {logs.length === 0 ? (
              <p className="px-3 py-10 text-center text-sm text-muted-foreground">{t('audit.empty')}</p>
            ) : (
              logs.map((log) => (
                <AuditRow
                  key={log.id}
                  log={log}
                  open={expanded === log.id}
                  onToggle={() => setExpanded((id) => (id === log.id ? null : log.id))}
                />
              ))
            )}
          </Panel>

          {/* 加载更多 */}
          <div className="flex items-center justify-between text-sm text-muted-foreground">
            <span>{t('audit.loadedCount', { loaded: logs.length, total })}</span>
            <Button
              variant="outline"
              size="sm"
              disabled={!hasNextPage || isFetchingNextPage}
              onClick={() => fetchNextPage()}
            >
              {t('audit.loadMore')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}

/** 单条审计行：可点展开查看变更详情（detail）。 */
function AuditRow({
  log,
  open,
  onToggle,
}: {
  log: AuditLogInfo
  open: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const detail = formatAuditDetail(log.detail)
  const hasDetail = detail !== '' || (log.failed && !!log.error)
  // FR-303：action 键翻译。audit.actions 下是含点的扁平键（如 instance.start），
  // 依赖 i18next ignoreJSONStructure（默认开）解析；未知键回退原键（defaultValue），保证不崩。
  const actionLabel = t(`audit.actions.${log.action}`, { defaultValue: log.action })
  return (
    <div className="border-b border-border/60 last:border-b-0">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        disabled={!hasDetail}
        className={cn(
          'flex w-full items-center gap-3 px-3 py-2 text-left text-xs transition-colors',
          hasDetail ? 'hover:bg-accent/50' : 'cursor-default',
        )}
      >
        <ChevronRight
          className={cn(
            'size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-ios',
            open && 'rotate-90',
            !hasDetail && 'opacity-0',
          )}
        />
        <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
          {new Date(log.createdAt).toLocaleString()}
        </span>
        <span className="w-28 shrink-0 truncate">{log.user?.username ?? `#${log.userId}`}</span>
        {/* FR-303：有翻译时显翻译 + 小号 mono 原键角标（筛选仍按原键，心智不断）；无翻译时保持原 mono 徽章。 */}
        <span className="w-44 min-w-0 shrink-0" title={log.action}>
          {actionLabel === log.action ? (
            <span className="rounded bg-muted px-2 py-0.5 font-mono text-[11px]">{log.action}</span>
          ) : (
            <span className="flex flex-col">
              <span className="truncate">{actionLabel}</span>
              <span className="truncate font-mono text-[10px] text-muted-foreground">{log.action}</span>
            </span>
          )}
        </span>
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          {log.targetType && `${log.targetType}#${log.targetId}`}
        </span>
        {/* FR-321：失败操作红色徽章，一眼定位报错操作。 */}
        {log.failed && (
          <span className="shrink-0 rounded-full bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive">
            {t('audit.failed')}
          </span>
        )}
        <span className="w-28 shrink-0 font-mono text-[11px] text-muted-foreground">{log.ip}</span>
      </button>
      {open && hasDetail && (
        <div className="bg-muted/30 px-3 pb-3 pl-10">
          {log.failed && log.error && (
            <>
              <p className="mb-1 text-[11px] font-medium text-destructive">{t('audit.errorContent')}</p>
              <pre className="mb-2 overflow-x-auto rounded-md border border-destructive/30 bg-destructive/5 p-2 font-mono text-[11px] whitespace-pre-wrap break-all text-destructive">
                {log.error}
              </pre>
            </>
          )}
          <p className="mb-1 text-[11px] font-medium text-muted-foreground">{t('audit.detail')}</p>
          <pre className="overflow-x-auto rounded-md border bg-card p-2 font-mono text-[11px] whitespace-pre-wrap break-all">
            {detail}
          </pre>
        </div>
      )}
    </div>
  )
}
