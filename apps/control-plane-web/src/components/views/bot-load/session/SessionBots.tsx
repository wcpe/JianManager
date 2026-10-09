import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
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
}

/**
 * 会话 Bot 明细表（FR-371）：搜索 / 状态 / 节点三个过滤输入（失焦或回车提交），
 * 整页全选与逐行勾选。勾选集合是纯 UI 状态，不外传。
 */
export function SessionBots({ filter, onFilterChange, data, isLoading }: SessionBotsProps) {
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
                <input
                  type="checkbox"
                  aria-label={t('botLoad.selectPage')}
                  checked={items.length > 0 && items.every((b) => selected.has(b.uuid))}
                  onChange={togglePage}
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
                  <input
                    type="checkbox"
                    checked={selected.has(b.uuid)}
                    onChange={() => toggle(b.uuid)}
                    aria-label={b.name}
                  />
                </TableCell>
                <TableCell className="font-medium">{b.name}</TableCell>
                <TableCell>{b.status}</TableCell>
                <TableCell>{b.executorNodeId ?? '—'}</TableCell>
                <TableCell className="font-mono text-xs">{b.stepId ?? b.commandId ?? '—'}</TableCell>
                <TableCell className="tabular-nums">{b.reconnectCount}</TableCell>
                <TableCell className="max-w-[16rem] truncate text-xs text-destructive">
                  {b.lastError ?? '—'}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !isLoading && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('botLoad.noBots')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>
          {t('botLoad.totalCount', { count: total })} · {t('botLoad.selectedCount', { count: selected.size })}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="xs"
            variant="ghost"
            disabled={filter.page <= 1}
            onClick={() => onFilterChange({ page: filter.page - 1 })}
          >
            {t('bots.prevPage')}
          </Button>
          <span>
            {filter.page}/{totalPages}
          </span>
          <Button
            size="xs"
            variant="ghost"
            disabled={filter.page >= totalPages}
            onClick={() => onFilterChange({ page: filter.page + 1 })}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}
