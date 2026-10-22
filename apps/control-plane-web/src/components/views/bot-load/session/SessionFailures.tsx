import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cardSurface, cardSurfaceTone, cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { FAILURE_CATEGORIES } from '@/lib/bot-load/bot-load-filters'
import type { BotLoadFailure } from '@/lib/bot-load/bot-load-types'
import { FailureTraceDrawer } from '@/components/views/bot-load/session/FailureTraceDrawer'

export const SESSION_FAILURES_PAGE_SIZE = 50

/** 失败明细的过滤条件（容器从 URL 解析，此处只消费结构）。 */
export interface SessionFailuresFilter {
  page: number
  category?: string
  errorCode?: string
  botUuid?: string
  node?: string
  step?: string
}

export interface SessionFailuresProps {
  filter: SessionFailuresFilter
  onFilterChange: (partial: Partial<SessionFailuresFilter>) => void
  /** 当前页数据（容器经 useBotLoadFailures 取数）。 */
  data?: { items: BotLoadFailure[]; total: number }
  isLoading?: boolean
  /**
   * 取数失败：必须显式报错。压测判定正是靠失败明细下结论，把它渲染成「无失败记录」
   * 是最危险的假信号——「没有失败」与「失败列表没取到」会被读成同一件事。
   */
  isError?: boolean
  /** 各类别失败计数（容器取自 run.failureSummary）。 */
  failureSummary: Record<string, number>
  /** 触发重试：传 botUuid 列表则重试选中，不传则重试当前过滤全集。 */
  onRetry: (botUuids?: string[]) => void
  retryPending?: boolean
  /** 重试结果文案（受控：文案由容器的 mutation 回调产出）。 */
  retryResult?: string | null
}

/**
 * 会话失败明细（FR-371）：顶部五类计数卡可按类别下钻，支持勾选 Bot 重试或按当前过滤
 * 全集重试；每行可展开失败追踪抽屉。勾选与抽屉开合是纯 UI 状态。
 */
export function SessionFailures({
  filter,
  onFilterChange,
  data,
  isLoading,
  isError,
  failureSummary,
  onRetry,
  retryPending,
  retryResult,
}: SessionFailuresProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [trace, setTrace] = useState<BotLoadFailure | null>(null)

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / SESSION_FAILURES_PAGE_SIZE))

  return (
    <div className="space-y-4" data-testid="session-failures">
      <div className="grid gap-2 sm:grid-cols-5">
        {FAILURE_CATEGORIES.map((cat) => (
          <Button
            key={cat}
            type="button"
            // 失败分类筛选片是整块可点击的卡片（自带 button 语义与键盘行为），换成 Card 的 div
            // 会退回 div 语义；故用 Button 的 invisible + auto 承接语义，外观仍取 Card 同源的
            // 表面常量（未选中态为 solid 档）。justify-start + 竖排：内容按「标题 / 数值」两行堆叠。
            variant="invisible"
            size="auto"
            className={cn(
              cardSurface,
              'flex w-full flex-col items-stretch justify-start gap-0 p-3 text-left transition-colors',
              filter.category === cat
                ? 'border-primary bg-primary/5'
                : cn(cardSurfaceTone.solid, 'hover:bg-accent/50'),
            )}
            onClick={() => onFilterChange({ category: filter.category === cat ? undefined : cat, page: 1 })}
          >
            <div className="text-xs text-muted-foreground">{t(`botLoad.failureCategory.${cat}`)}</div>
            <div className="text-lg font-semibold tabular-nums">{failureSummary[cat] ?? 0}</div>
          </Button>
        ))}
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={selected.size === 0 || retryPending}
          onClick={() => onRetry([...selected])}
        >
          {t('botLoad.retrySelected')}
        </Button>
        <Button size="sm" variant="outline" disabled={retryPending} onClick={() => onRetry()}>
          {t('botLoad.retryFiltered')}
        </Button>
      </div>
      {retryResult && (
        <p className="rounded border bg-muted/30 px-3 py-2 text-sm" data-testid="retry-result">
          {retryResult}
        </p>
      )}

      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>{t('botLoad.time')}</TableHead>
              <TableHead>{t('botLoad.category')}</TableHead>
              <TableHead>{t('botLoad.errorCode')}</TableHead>
              <TableHead>{t('botLoad.botName')}</TableHead>
              <TableHead>{t('botLoad.message')}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((f) => (
              <TableRow key={f.id}>
                <TableCell>
                  <Checkbox
                    checked={f.botUuid ? selected.has(f.botUuid) : false}
                    disabled={!f.botUuid}
                    // 读屏需要知道勾的是哪个 Bot（行内还有时间/类别/错误码，光看「复选框」无从判断）。
                    aria-label={f.botUuid ?? f.id}
                    onCheckedChange={() => {
                      if (!f.botUuid) return
                      const uuid = f.botUuid
                      setSelected((prev) => {
                        const next = new Set(prev)
                        if (next.has(uuid)) next.delete(uuid)
                        else next.add(uuid)
                        return next
                      })
                    }}
                  />
                </TableCell>
                <TableCell className="whitespace-nowrap text-xs">{f.occurredAt}</TableCell>
                <TableCell>
                  {t(`botLoad.failureCategory.${f.category}`, f.category)}
                  {f.legacyCategory === 'probe' && (
                    <span className="ml-1 text-[10px] text-amber-700">legacy</span>
                  )}
                </TableCell>
                <TableCell className="font-mono text-xs">{f.errorCode}</TableCell>
                <TableCell className="font-mono text-xs">{f.botUuid ?? '—'}</TableCell>
                <TableCell className="max-w-xs truncate text-xs">{f.message}</TableCell>
                <TableCell>
                  <Button size="xs" variant="ghost" onClick={() => setTrace(f)}>
                    {t('botLoad.traceBtn')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !isLoading && !isError && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('botLoad.noFailures')}
                </TableCell>
              </TableRow>
            )}
            {/* 失败明细是压测判定的证据面：取数失败绝不能被读成「没有失败」。 */}
            {isError && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-amber-700 dark:text-amber-300">
                  {t('botLoad.loadFailed')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <PaginationBar
        page={filter.page}
        totalPages={totalPages}
        total={total}
        onChange={(next) => onFilterChange({ page: next })}
        totalLabel={t('botLoad.totalCount', { count: total })}
        // 此处原文案是裸「页码/总页数」（无「第…页」包装），PaginationBar 默认拼接与之等价。
        prevLabel={t('bots.prevPage')}
        nextLabel={t('bots.nextPage')}
        className="text-xs"
      />

      <FailureTraceDrawer failure={trace} open={trace != null} onOpenChange={(o) => !o && setTrace(null)} />
    </div>
  )
}
