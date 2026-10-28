import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { approvalStatusVariant, type ApprovalTicketInfo } from '@/api/approvals'

/**
 * 审批中心列表视图（FR-493）。
 *
 * 决策动作**只对 pending 渲染**：状态机里只有 pending 可被批准/驳回，
 * 给已完成的行留一个必然被后端拒绝的按钮，等于让审批人白点一次。
 *
 * 申请理由与冻结参数都是审批人判断的依据，所以列表里就给「理由」摘要，
 * 完整 payload 在详情抽屉展开 —— 不让审批人在看不到参数的情况下点批准。
 */

/** 目标展示：targetType/targetId 由服务端冻结写入，客户端不得伪造归属。 */
function targetLabel(t: ApprovalTicketInfo): string {
  if (!t.targetType && !t.targetId) return '—'
  return `${t.targetType}:${t.targetId}`
}

function statusLabel(t: (k: string) => string, status: string): string {
  const known = ['pending', 'withdrawn', 'rejected', 'expired', 'executing', 'succeeded', 'failed']
  if (!known.includes(status)) return status
  const key = `approvals.status${status.charAt(0).toUpperCase()}${status.slice(1)}`
  return t(key)
}

export interface ApprovalListViewProps {
  items: ApprovalTicketInfo[]
  loading?: boolean
  /** 选中行的详情内容；undefined 表示未展开。 */
  expandedId?: number | null
  onToggleExpand?: (id: number) => void
  onApprove: (t: ApprovalTicketInfo) => void
  onReject: (t: ApprovalTicketInfo) => void
  /** 无决策权限时隐藏按钮（后端仍有一道关，这里只是不显示会失败的入口）。 */
  canDecide?: boolean
}

export function ApprovalListView({
  items,
  loading,
  expandedId,
  onToggleExpand,
  onApprove,
  onReject,
  canDecide = true,
}: ApprovalListViewProps) {
  const { t } = useTranslation()

  if (loading) {
    return (
      <div className="space-y-2" aria-busy="true">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-14 w-full" />
        ))}
      </div>
    )
  }

  if (items.length === 0) {
    return <EmptyState title={t('approvals.empty')} description={t('approvals.subtitle')} />
  }

  // `min-w-0` 是关键：表格自带 overflow-x-auto，但在 flex/grid 父级里它的最小宽度
  // 默认是内容宽度（min-width:auto），于是窄屏下整张表把**文档**撑宽而不是自己内部滚动，
  // 表现为决策按钮跑到视口外（e2e 实测 390px 视口下批准按钮落在 x≈849）。
  // 加上 min-w-0 后，滚动被收进表格容器内，页面不再横向溢出。
  return (
    <div className="min-w-0">
      <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('approvals.action')}</TableHead>
          <TableHead>{t('approvals.target')}</TableHead>
          <TableHead>{t('approvals.applicant')}</TableHead>
          <TableHead>{t('approvals.reason')}</TableHead>
          <TableHead>{t('approvals.status')}</TableHead>
          <TableHead>{t('approvals.createdAt')}</TableHead>
          <TableHead className="text-right">{t('common.actions')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((row) => {
          const expanded = expandedId === row.id
          return (
            <FragmentRow
              key={row.id}
              row={row}
              expanded={expanded}
              onToggleExpand={onToggleExpand}
              onApprove={onApprove}
              onReject={onReject}
              canDecide={canDecide}
              t={t}
            />
          )
        })}
        </TableBody>
      </Table>
    </div>
  )
}

function FragmentRow({
  row,
  expanded,
  onToggleExpand,
  onApprove,
  onReject,
  canDecide,
  t,
}: {
  row: ApprovalTicketInfo
  expanded: boolean
  onToggleExpand?: (id: number) => void
  onApprove: (t: ApprovalTicketInfo) => void
  onReject: (t: ApprovalTicketInfo) => void
  canDecide: boolean
  t: (k: string) => string
}) {
  const pending = row.status === 'pending'
  return (
    <>
      <TableRow>
        <TableCell className="font-medium">
          {onToggleExpand ? (
            <button
              type="button"
              className="underline-offset-2 hover:underline"
              onClick={() => onToggleExpand(row.id)}
              aria-expanded={expanded}
            >
              {row.action}
            </button>
          ) : (
            row.action
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">{targetLabel(row)}</TableCell>
        <TableCell className="text-muted-foreground">
          {row.applicantKind === 'agent' ? t('approvals.applicantAgent') : t('approvals.applicantHuman')}
          {row.applicantUserId ? ` #${row.applicantUserId}` : ''}
        </TableCell>
        <TableCell className="max-w-[280px] truncate" title={row.reason}>
          {row.reason}
        </TableCell>
        <TableCell>
          <Badge variant={approvalStatusVariant(row.status)}>{statusLabel(t, row.status)}</Badge>
        </TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">
          {new Date(row.createdAt).toLocaleString()}
        </TableCell>
        <TableCell className="text-right">
          {pending && canDecide ? (
            <div className="flex justify-end gap-2">
              <Button size="sm" onClick={() => onApprove(row)}>
                {t('approvals.approve')}
              </Button>
              <Button size="sm" variant="outline" onClick={() => onReject(row)}>
                {t('approvals.reject')}
              </Button>
            </div>
          ) : (
            <span className="text-sm text-muted-foreground">—</span>
          )}
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow>
          <TableCell colSpan={7} className="bg-muted/40">
            <ApprovalDetail row={row} t={t} />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

/**
 * 后端把 evidence 存成 text 列，未填时序列化出的是字符串 `"null"` / `"{}"` 而不是 Go 的 nil。
 * 若只判真假值，`"null"` 是真值，界面就会凭空多出一块写着 `null` 的「申请时快照」——
 * 审批人会以为申请人留了快照却看不到内容。所以这里按**解析后的内容**判断有没有东西可看。
 */
function hasEvidence(evidence?: string): boolean {
  if (!evidence) return false
  const trimmed = evidence.trim()
  if (trimmed === '' || trimmed === 'null' || trimmed === '{}') return false
  try {
    const parsed = JSON.parse(trimmed) as unknown
    if (parsed === null) return false
    if (typeof parsed === 'object' && Object.keys(parsed as object).length === 0) return false
  } catch {
    // 不是 JSON（历史数据/异常写入）：有内容就照原样展示，不吞掉证据。
  }
  return true
}

/** 详情：冻结参数与申请时快照是审批人真正要看的东西。 */
function ApprovalDetail({ row, t }: { row: ApprovalTicketInfo; t: (k: string) => string }) {
  return (
    <div className="space-y-3 py-2 text-sm">
      <div>
        <div className="mb-1 font-medium">{t('approvals.payload')}</div>
        <pre className="max-h-48 overflow-auto rounded bg-background p-2 text-xs">{row.payloadFrozen || '—'}</pre>
      </div>
      {hasEvidence(row.evidence) ? (
        <div>
          <div className="mb-1 font-medium">{t('approvals.evidence')}</div>
          <pre className="max-h-48 overflow-auto rounded bg-background p-2 text-xs">{row.evidence}</pre>
        </div>
      ) : null}
      <div className="grid grid-cols-2 gap-2 text-muted-foreground sm:grid-cols-4">
        <div>
          {t('approvals.expiresAt')}: {new Date(row.expiresAt).toLocaleString()}
        </div>
        {row.decidedAt ? (
          <div>
            {t('approvals.decidedAt')}: {new Date(row.decidedAt).toLocaleString()}
          </div>
        ) : null}
        {row.decidedByUserId ? (
          <div>
            {t('approvals.decidedBy')}: #{row.decidedByUserId}
          </div>
        ) : null}
        {row.failureReason ? (
          <div className="text-destructive">
            {t('approvals.failureReason')}: {row.failureReason}
          </div>
        ) : null}
      </div>
    </div>
  )
}
