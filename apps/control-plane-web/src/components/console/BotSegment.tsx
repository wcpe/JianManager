import { useState } from 'react'
import { toast } from 'sonner'
import {
  useBots,
  useBotSummary,
  useBotBatch,
  useSetBotBehavior,
  useDeleteBot,
} from '@/api/bots'
import type { BotBatchFilter } from '@jianmanager/ui/lib/bot'
import CreateBotDialog from './CreateBotDialog'
import BotSegmentView from '@/components/views/instances/BotSegment'

const PAGE_SIZE = 50

/**
 * Bot 段的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层持有筛选与分页（取数依赖它们）、取聚合与列表、
 * 接批量/改行为/删除三个动作、注入已迁包的创建对话框。保留同路径的默认导出与同一套 props，
 * 调用点无需改动。
 */
export default function BotSegment({ instanceId }: { instanceId: number }) {
  // 工具栏筛选状态（变更后回到第 1 页）
  const [q, setQ] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [behaviorFilter, setBehaviorFilter] = useState('')
  const [page, setPage] = useState(1)

  const resetPage = () => setPage(1)

  // 列表筛选维度（与摘要/批量共用），空串表示不限
  const filter: BotBatchFilter = {
    instanceId,
    ...(statusFilter ? { status: statusFilter } : {}),
    ...(behaviorFilter ? { behavior: behaviorFilter } : {}),
    ...(q.trim() ? { q: q.trim() } : {}),
  }

  const { data: summary } = useBotSummary({ instanceId })
  const { data: botList, isLoading } = useBots({ ...filter, page, pageSize: PAGE_SIZE })
  const batch = useBotBatch()
  const setBehavior = useSetBotBehavior()
  const del = useDeleteBot()

  return (
    <BotSegmentView
      instanceId={instanceId}
      q={q}
      onQChange={(v) => {
        setQ(v)
        resetPage()
      }}
      statusFilter={statusFilter}
      onStatusFilterChange={(v) => {
        setStatusFilter(v)
        resetPage()
      }}
      behaviorFilter={behaviorFilter}
      onBehaviorFilterChange={(v) => {
        setBehaviorFilter(v)
        resetPage()
      }}
      page={page}
      onPageChange={setPage}
      summary={summary}
      bots={botList?.items ?? []}
      total={botList?.total ?? 0}
      loading={isLoading}
      createDialog={({ open, onOpenChange }) => (
        <CreateBotDialog open={open} onOpenChange={onOpenChange} instanceId={instanceId} />
      )}
      onBatch={(payload) => batch.mutateAsync(payload)}
      onSetBehavior={(payload) => setBehavior.mutateAsync(payload).then(() => undefined)}
      onDeleteBot={(id) => del.mutateAsync(id).then(() => undefined)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
