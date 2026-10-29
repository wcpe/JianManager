import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 
 * 平台审批中心。
 *
 * 状态机是 7 态而不是「批准/驳回」两态：pending 可被决策，决策后进入
 * executing → succeeded / failed，另有 withdrawn / rejected / expired 三条终止路径。
 * 前端**不自己推导**可执行操作，而是按 status 判定 —— 否则会给出后端必然拒绝的按钮。
 */
export type ApprovalStatus =
  | 'pending'
  | 'withdrawn'
  | 'rejected'
  | 'expired'
  | 'executing'
  | 'succeeded'
  | 'failed'

export interface ApprovalTicketInfo {
  id: number
  uuid: string
  applicantUserId: number
  applicantKind: string
  tokenId?: string
  action: string
  targetType: string
  targetId: string
  payloadHash: string
  payloadFrozen: string
  evidence?: string
  reason: string
  status: ApprovalStatus
  idempotencyKey?: string
  decidedByUserId?: number
  decidedAt?: string
  consumedAt?: string
  expiresAt: string
  failureReason?: string
  createdAt: string
  updatedAt: string
}

/** 
 * 待审/已决策列表。
 *
 * 默认只拉 pending —— 审批台的日常用法是「清空待办」，把全部历史混进来会淹没待办。
 *
 * `status` 传 'all' 表示不过滤状态，由服务端归一（缺省 pending / 'all' 不加条件）。
 * 这个语义必须由服务端兑现：缺省值 pending 与「不过滤」若共用一个参数，
 * 前端无论传什么都表达不出「全部」，只会拿到一张空列表。
 */
export function useApprovals(status = 'pending', limit = 50) {
  return useQuery({
    queryKey: ['approvals', status, limit],
    queryFn: async () => {
      const { data } = await api.get<{ items: ApprovalTicketInfo[] }>('/approvals', {
        params: { status, limit },
      })
      return data.items ?? []
    },
  })
}

export function useApproval(id: number | null) {
  return useQuery({
    queryKey: ['approval', id],
    enabled: id != null,
    queryFn: async () => {
      const { data } = await api.get<ApprovalTicketInfo>(`/approvals/${id}`)
      return data
    },
  })
}

/** 
 * 批准。
 *
 * 批准是**触发真实副作用**的动作（停实例、删文件…），所以不做乐观更新：
 * 只有服务端确认执行结果后才刷新列表，否则界面会先显示「已批准」再回退。
 */
export function useApproveApproval() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post(`/approvals/${id}/approve`)
      return data
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['approvals'] })
      void qc.invalidateQueries({ queryKey: ['approval'] })
    },
  })
}

export function useRejectApproval() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, note }: { id: number; note?: string }) => {
      const { data } = await api.post(`/approvals/${id}/reject`, { note })
      return data
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['approvals'] })
      void qc.invalidateQueries({ queryKey: ['approval'] })
    },
  })
}

/** 
 * 状态 → Badge 变体（对应 `@jianmanager/ui` 的 chip-* 语义变体）。
 *
 * 整个 7 态里只有 succeeded 是阳性；pending/executing 是「进行中」，用 idle 而非 ok ——
 * 把待审批画成绿色会让人误以为已经通过。未知状态回落 neutral，不静默当成阳性。
 */
export function approvalStatusVariant(
  status: string,
): 'chip-ok' | 'chip-bad' | 'chip-idle' | 'chip-neutral' {
  switch (status) {
    case 'succeeded':
      return 'chip-ok'
    case 'rejected':
    case 'expired':
    case 'failed':
      return 'chip-bad'
    case 'pending':
    case 'executing':
      return 'chip-idle'
    default:
      return 'chip-neutral'
  }
}
