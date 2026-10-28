// 审批中心（FR-493）：列表取数、状态筛选与决策动作接线都在本层决定，行渲染与详情展开交共享视图。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useApprovals,
  useApproveApproval,
  useRejectApproval,
  type ApprovalTicketInfo,
} from '@/api/approvals'
import { Button } from '@jianmanager/ui/components/button'
import { ApprovalListView } from '@/components/views/approvals/ApprovalListView'

type ErrResp = { response?: { data?: { message?: string }; status?: number } }

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 审批中心页容器。
 *
 * 决策采用**二次确认**而不是直接提交：批准会真实触发副作用（停实例、删文件…），
 * 一次误点就是生产事故。确认文案里带上 action，让人在点下去之前知道自己批的是什么。
 *
 * 列表不做乐观更新：批准后状态要经过 executing → succeeded/failed，
 * 先显示「已批准」再回退会让审批人误以为已生效。
 */
export default function ApprovalsPage() {
  const { t } = useTranslation()
  const [status, setStatus] = useState('pending')
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [confirmTarget, setConfirmTarget] = useState<{ row: ApprovalTicketInfo; mode: 'approve' | 'reject' } | null>(
    null,
  )

  const listQ = useApprovals(status)
  const approve = useApproveApproval()
  const reject = useRejectApproval()

  const handleApprove = (row: ApprovalTicketInfo) => {
    setConfirmTarget({ row, mode: 'approve' })
  }
  const handleReject = (row: ApprovalTicketInfo) => {
    setConfirmTarget({ row, mode: 'reject' })
  }

  const submitDecision = async () => {
    if (!confirmTarget) return
    const { row, mode } = confirmTarget
    try {
      if (mode === 'approve') {
        await approve.mutateAsync(row.id)
        toast.success(t('approvals.approveSuccess'))
      } else {
        await reject.mutateAsync({ id: row.id })
        toast.success(t('approvals.rejectSuccess'))
      }
      setConfirmTarget(null)
    } catch (e) {
      // 失败关闭：后端拒绝（过期/已决策/漂移）时把真实原因透出，不让审批人猜。
      toast.error(errMsg(e, t('common.error')))
      setConfirmTarget(null)
    }
  }

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-lg font-semibold">{t('approvals.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('approvals.subtitle')}</p>
        </div>
        {/* 状态筛选只有两个高频取值：待办与全部。其余 5 态是结果，不该混进筛选主路径。 */}
        <div className="flex gap-2">
          <Button
            size="sm"
            variant={status === 'pending' ? 'default' : 'outline'}
            onClick={() => setStatus('pending')}
          >
            {t('approvals.pending')}
          </Button>
          <Button size="sm" variant={status === 'all' ? 'default' : 'outline'} onClick={() => setStatus('all')}>
            {t('approvals.all')}
          </Button>
        </div>
      </div>

      <ApprovalListView
        items={listQ.data ?? []}
        loading={listQ.isLoading}
        expandedId={expandedId}
        onToggleExpand={(id) => setExpandedId((cur) => (cur === id ? null : id))}
        onApprove={handleApprove}
        onReject={handleReject}
        canDecide
      />

      {confirmTarget ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label={
            confirmTarget.mode === 'approve'
              ? t('approvals.approveConfirmTitle')
              : t('approvals.rejectConfirmTitle')
          }
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
        >
          <div className="w-full max-w-md space-y-4 rounded-lg bg-background p-5 shadow-lg">
            <h2 className="text-base font-semibold">
              {confirmTarget.mode === 'approve'
                ? t('approvals.approveConfirmTitle')
                : t('approvals.rejectConfirmTitle')}
            </h2>
            <p className="text-sm text-muted-foreground">
              {confirmTarget.mode === 'approve'
                ? t('approvals.approveConfirmBody', { action: confirmTarget.row.action })
                : confirmTarget.row.action}
            </p>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={() => setConfirmTarget(null)}>
                {t('common.cancel')}
              </Button>
              <Button
                variant={confirmTarget.mode === 'approve' ? 'default' : 'destructive'}
                onClick={() => void submitDecision()}
                disabled={approve.isPending || reject.isPending}
              >
                {confirmTarget.mode === 'approve' ? t('approvals.approve') : t('approvals.reject')}
              </Button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}
