import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Input } from '@jianmanager/ui/components/input'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import type { BotLoadRunBot } from '@/lib/bot-load/bot-load-types'

export const SESSION_BOTS_PAGE_SIZE = 50

/** 会话 Bot 列表的过滤条件（容器从 URL 解析，此处只消费结构）。 */
export interface SessionBotsFilter {
  page: number
  q?: string
  status?: string
  node?: string
  step?: string
  error?: string
}

export interface SessionBotsProps {
  /** 当前过滤（含页码）。 */
  filter: SessionBotsFilter
  /** 过滤变更回写（容器写入 URL）。 */
  onFilterChange: (partial: Partial<SessionBotsFilter>) => void
  /** 当前页数据（容器经 useBotLoadRunBots 取数）。 */
  data?: { items: BotLoadRunBot[]; total: number }
  isLoading?: boolean
  /** 取数失败：显式报错，不得退化成「无 Bot」（那会把取数故障读成「这一批没起 Bot」）。 */
  isError?: boolean
}

/** 
 * 会话 Bot 明细表：搜索 / 状态 / 节点三个过滤输入（失焦或回车提交），
 * 整页全选与逐行勾选。勾选集合是纯 UI 状态，不外传。
 */
export function SessionBots({ filter, onFilterChange, data, isLoading, isError }: SessionBotsProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / SESSION_BOTS_PAGE_SIZE))

  const toggle = (uuid: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(uuid)) next.delete(uuid)
      else next.add(uuid)
      return next
    })
  }

  const togglePage = () => {
    const allOnPage = items.every((b) => selected.has(b.uuid))
    setSelected((prev) => {
      const next = new Set(prev)
      for (const b of items) {
        if (allOnPage) next.delete(b.uuid)
        else next.add(b.uuid)
      }
      return next
    })
  }

  return (
    <div className="space-y-3" data-testid="session-bots">
      <div className="flex flex-wrap gap-2">
        <Input
          className="max-w-xs"
          placeholder={t('botLoad.searchBots')}
          defaultValue={filter.q ?? ''}
          onBlur={(e) => onFilterChange({ q: e.target.value || undefined, page: 1 })}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              onFilterChange({ q: (e.target as HTMLInputElement).value || undefined, page: 1 })
            }
          }}
        />
        <Input
          className="w-36"
          placeholder={t('botLoad.status')}
          defaultValue={filter.status ?? ''}
          onBlur={(e) => onFilterChange({ status: e.target.value || undefined, page: 1 })}
        />
        <Input
          className="w-28"
          placeholder={t('botLoad.node')}
          defaultValue={filter.node ?? ''}
          onBlur={(e) => onFilterChange({ node: e.target.value || undefined, page: 1 })}
        />
      </div>

      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead className="w-10">
                {/* 列表多选 → 组件库 Checkbox（统一尺寸/焦点环/点击热区）。
                    Radix 的回调是 onCheckedChange(boolean | 'indeterminate')，故显式折算成 void。 */}
                <Checkbox
                  aria-label={t('botLoad.selectPage')}
                  checked={items.length > 0 && items.every((b) => selected.has(b.uuid))}
                  onCheckedChange={() => togglePage()}
                />
              </TableHead>
              <TableHead>{t('botLoad.botName')}</TableHead>
              <TableHead>{t('botLoad.status')}</TableHead>
              <TableHead>{t('botLoad.node')}</TableHead>
              <TableHead>{t('botLoad.step')}</TableHead>
              <TableHead>{t('botLoad.reconnects')}</TableHead>
              <TableHead>{t('botLoad.error')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((b) => (
              <TableRow key={b.uuid}>
                <TableCell>
                  <Checkbox
                    checked={selected.has(b.uuid)}
                    onCheckedChange={() => toggle(b.uuid)}
                    aria-label={b.name}
                  />
                </TableCell>
                <TableCell className="font-medium">{b.name}</TableCell>
                {/* 状态枚举走既有 `bots.status_*` 文案表（与 Bot 列表同一套），未知值回退裸枚举。 */}
                <TableCell>{t(`bots.status_${b.status}`, b.status)}</TableCell>
                <TableCell>{b.executorNodeId ?? '—'}</TableCell>
                <TableCell className="font-mono text-xs">{b.stepId ?? b.commandId ?? '—'}</TableCell>
                <TableCell className="tabular-nums">{b.reconnectCount}</TableCell>
                <TableCell className="max-w-[16rem] truncate text-xs text-destructive">
                  {b.lastError ?? '—'}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !isLoading && !isError && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('botLoad.noBots')}
                </TableCell>
              </TableRow>
            )}
            {/* 失败判定优先于空态：取数失败与「这一批没有 Bot」是两件事。 */}
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
        totalLabel={`${t('botLoad.totalCount', { count: total })} · ${t('botLoad.selectedCount', { count: selected.size })}`}
        // 此处原文案是裸「页码/总页数」（无「第…页」包装），PaginationBar 默认拼接与之等价。
        prevLabel={t('bots.prevPage')}
        nextLabel={t('bots.nextPage')}
        className="text-xs"
      />
    </div>
  )
}
