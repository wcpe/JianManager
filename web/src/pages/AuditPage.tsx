import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuditLogs } from '@/api/audit'
import { useUsers } from '@/api/users'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  AUDIT_PAGE_STEP,
  DEFAULT_AUDIT_FILTER,
  toAuditParams,
  type AuditFilterState,
} from './audit-filters'

// Radix Select 不允许空字符串值，用哨兵代表「全部用户」。
const SENTINEL_ALL = '__all__'

/**
 * 审计日志查询页（FR-015）。
 * 顶部筛选栏（用户/操作/目标类型/时间范围）下沉到后端 DB 过滤，变更即重查；
 * 「加载更多」递增 limit，「清空」恢复默认。套 FR-061 高密度风格。
 */
export default function AuditPage() {
  const { t } = useTranslation()
  const { data: users } = useUsers()

  const [filter, setFilter] = useState<AuditFilterState>(DEFAULT_AUDIT_FILTER)

  // 改任一筛选都把 limit 收回默认，避免停留在放大的页。
  const patch = (next: Partial<AuditFilterState>) =>
    setFilter((prev) => ({ ...prev, limit: DEFAULT_AUDIT_FILTER.limit, ...next }))

  const params = toAuditParams(filter)
  const { data: logs, isLoading, isError } = useAuditLogs(params)

  // 命中数等于当前 limit 时，可能还有更多，允许继续加载。
  const canLoadMore = !!logs && logs.length >= filter.limit

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">{t('audit.title')}</h1>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setFilter(DEFAULT_AUDIT_FILTER)}
        >
          {t('audit.clear')}
        </Button>
      </div>

      {/* 筛选器 */}
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

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('audit.loadError')}</p>
      ) : (
        <>
          <div className="border rounded-lg overflow-hidden">
            <Table>
              <TableHeader className="bg-muted/50">
                <TableRow>
                  <TableHead className="w-44">{t('audit.time')}</TableHead>
                  <TableHead className="w-32">{t('audit.user')}</TableHead>
                  <TableHead className="w-40">{t('audit.action')}</TableHead>
                  <TableHead>{t('audit.target')}</TableHead>
                  <TableHead className="w-32">{t('audit.ip')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {logs?.map((log) => (
                  <TableRow key={log.id}>
                    <TableCell className="text-muted-foreground whitespace-nowrap font-mono text-xs">
                      {new Date(log.createdAt).toLocaleString()}
                    </TableCell>
                    <TableCell className="text-xs">
                      {log.user?.username ?? `#${log.userId}`}
                    </TableCell>
                    <TableCell>
                      <span className="px-2 py-0.5 text-xs bg-muted rounded font-mono">
                        {log.action}
                      </span>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs break-all">
                      {log.targetType && `${log.targetType}#${log.targetId}`}
                    </TableCell>
                    <TableCell className="text-muted-foreground font-mono text-xs">
                      {log.ip}
                    </TableCell>
                  </TableRow>
                ))}
                {(!logs || logs.length === 0) && (
                  <TableRow>
                    <TableCell colSpan={5} className="text-center text-muted-foreground">
                      {t('audit.empty')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>

          {/* 加载更多 */}
          <div className="flex items-center justify-between text-sm text-muted-foreground">
            <span>{t('audit.totalCount', { count: logs?.length ?? 0 })}</span>
            <Button
              variant="outline"
              size="sm"
              disabled={!canLoadMore}
              onClick={() =>
                setFilter((prev) => ({ ...prev, limit: prev.limit + AUDIT_PAGE_STEP }))
              }
            >
              {t('audit.loadMore')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
