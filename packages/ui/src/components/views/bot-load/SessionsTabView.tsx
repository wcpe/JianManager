import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import { Button } from '../../button'
import { Input } from '../../input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../../table'

/** 会话列表分页尺寸（容器取数须与本常量一致）。 */
export const SESSIONS_TAB_PAGE_SIZE = 20

/** 会话状态分布（服务端聚合）。 */
export interface SessionsTabViewCounts {
  total: number
  byStatus: Record<string, number>
}

/**
 * 列表行所需的最小会话结构。
 * 与应用侧 `BotStressSession` 结构兼容，包内不反向依赖应用契约（ADR-097）。
 */
export interface SessionsTabViewItem {
  id: number
  namePrefix: string
  instanceId: number
  status: string
  count: number
  counts: SessionsTabViewCounts
}

/**
 * 压测会话列表 tab 的注入契约（受控视图，ADR-097 b 范式）。
 *
 * 取数、翻页/URL 同步、路由跳转、启停 mutation 与 toast 全在容器；
 * 包内只保留不触发取数的搜索草稿 state。
 */
export interface SessionsTabViewProps {
  /** 当前页数据（容器经 `useBotStressSessions` 取数）。 */
  data?: { items: SessionsTabViewItem[]; total: number }
  /** 当前页码（URL 状态，容器维护）。 */
  page: number
  isLoading?: boolean
  isError?: boolean
  /** 错误态「刷新」回调（容器 `refetch`）。 */
  onRefresh: () => void
  /** 翻页（容器写回 URL）。 */
  onPageChange: (page: number) => void
  /** 打开会话详情（容器负责路由跳转）。 */
  onOpenSession: (id: number) => void
  /** 打开创建向导（向导属应用侧接线层，由容器渲染，包内不得引用）。 */
  onCreateClick: () => void
  /** 启动会话（容器执行 mutation 并决定成功/失败提示）。 */
  onStart: (id: number) => void
  /** 停止会话（容器执行 mutation 并决定成功/失败提示）。 */
  onStop: (id: number) => void
  /** 启动 mutation 进行中：与原实现一致，pending 期间禁用全部启动按钮。 */
  startPending?: boolean
  /** 停止 mutation 进行中：pending 期间禁用全部停止按钮。 */
  stopPending?: boolean
  /** 搜索框初值（取 URL 的 q；该筛选当前不参与取数，仅作展示草稿，故留在包内）。 */
  initialSearch?: string
}

/**
 * 压测会话列表 tab：搜索草稿 + 列表 + 状态分布 + 分页（FR-371）。
 * 详情页路由 `/bots/sessions/:id` 由 FR-372 承接；此处只暴露 `onOpenSession` 回调。
 */
export function SessionsTabView({
  data,
  page,
  isLoading,
  isError,
  onRefresh,
  onPageChange,
  onOpenSession,
  onCreateClick,
  onStart,
  onStop,
  startPending,
  stopPending,
  initialSearch,
}: SessionsTabViewProps) {
  const { t } = useTranslation()
  // 纯 UI 草稿：不参与取数，故状态留在包内，仅以 URL 的 q 作初值。
  const [search, setSearch] = useState(initialSearch ?? '')

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / SESSIONS_TAB_PAGE_SIZE))

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('botsLoad.searchSessions')}
          aria-label={t('botsLoad.searchSessions')}
          className="h-9 w-56"
        />
        <Button className="ml-auto" onClick={onCreateClick}>
          <Plus className="size-4" /> {t('botsLoad.createRun')}
        </Button>
      </div>

      {isError && (
        <div className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm">
          {t('botsLoad.sessionsLoadFailed')}
          <Button size="xs" variant="outline" className="ml-2" onClick={onRefresh}>
            {t('common.refresh')}
          </Button>
        </div>
      )}

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : items.length === 0 ? (
        <p className="rounded-lg border py-10 text-center text-muted-foreground">{t('botsLoad.sessionsEmpty')}</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/40">
              <TableRow>
                <TableHead>{t('bots.namePrefix')}</TableHead>
                <TableHead>{t('bots.instance')}</TableHead>
                <TableHead>{t('bots.status')}</TableHead>
                <TableHead className="text-right">{t('bots.count')}</TableHead>
                <TableHead className="text-right">{t('bots.statusDistribution')}</TableHead>
                <TableHead className="text-right">{t('bots.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((session) => (
                <TableRow
                  key={session.id}
                  className="cursor-pointer transition-colors hover:bg-accent/50"
                  onClick={() => onOpenSession(session.id)}
                >
                  <TableCell className="font-medium">{session.namePrefix}</TableCell>
                  <TableCell>{session.instanceId}</TableCell>
                  <TableCell>{t(`bots.stressStatus_${session.status}`, session.status)}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {session.counts.total}/{session.count}
                  </TableCell>
                  <TableCell>
                    <StatusDist counts={session.counts} />
                  </TableCell>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <div className="flex justify-end gap-1">
                      <Button
                        size="xs"
                        variant="outline"
                        disabled={session.status !== 'pending' || startPending}
                        onClick={() => onStart(session.id)}
                      >
                        {t('bots.startSession')}
                      </Button>
                      <Button
                        size="xs"
                        variant="outline"
                        disabled={session.status === 'stopped' || stopPending}
                        onClick={() => onStop(session.id)}
                      >
                        {t('bots.stopSession')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{t('bots.totalCount', { count: total })}</span>
        <div className="flex items-center gap-2">
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => onPageChange(page + 1)}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}

/** 各状态计数徽标；无任何计数时退化为占位符。 */
function StatusDist({ counts }: { counts: SessionsTabViewCounts }) {
  const { t } = useTranslation()
  const entries = Object.entries(counts.byStatus).filter(([, c]) => c > 0)
  if (entries.length === 0) {
    return <span className="block text-right text-xs text-muted-foreground">—</span>
  }
  return (
    <div className="flex flex-wrap justify-end gap-1">
      {entries.map(([status, count]) => (
        <span key={status} className="rounded border px-1.5 py-0.5 text-xs">
          {t(`bots.status_${status}`, status)} {count}
        </span>
      ))}
    </div>
  )
}
