import type { AuditQueryParams } from '@/api/audit'

/**
 * 审计页筛选栏的可编辑状态（FR-015）。
 * 时间为 HTML `datetime-local` 输入的本地值（如 `2026-06-22T10:30`），无时区。
 */
export interface AuditFilterState {
  /** 用户文本输入，可填用户名匹配项对应的 userId（字符串以便兼容空值）。 */
  userId: string
  action: string
  targetType: string
  /** datetime-local 本地时间，空串表示不限。 */
  from: string
  to: string
  limit: number
}

/** 默认筛选状态：全部不过滤，limit 与后端默认一致（100）。 */
export const DEFAULT_AUDIT_FILTER: AuditFilterState = {
  userId: '',
  action: '',
  targetType: '',
  from: '',
  to: '',
  limit: 100,
}

/** 「加载更多」每次递增的条数。 */
export const AUDIT_PAGE_STEP = 100

/**
 * 把 datetime-local 本地时间转为后端期望的 RFC3339（带时区偏移）。
 * 后端用 `time.Parse(time.RFC3339, ...)` 解析，必须含时区；裸的本地值（缺秒/缺时区）解析会失败。
 * 空串或非法时间返回 undefined（该维度不过滤）。
 */
export function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

/**
 * 把筛选状态规整为 `GET /audit` 的 query 参数（FR-015）。
 * 留空 / 非法的维度一律省略，时间转为 RFC3339。
 */
export function toAuditParams(filter: AuditFilterState): AuditQueryParams {
  const params: AuditQueryParams = {}

  const userId = filter.userId.trim()
  if (userId) {
    const n = Number(userId)
    if (Number.isInteger(n) && n > 0) params.userId = n
  }

  const action = filter.action.trim()
  if (action) params.action = action

  const targetType = filter.targetType.trim()
  if (targetType) params.targetType = targetType

  const from = toRFC3339(filter.from)
  if (from) params.from = from

  const to = toRFC3339(filter.to)
  if (to) params.to = to

  if (filter.limit > 0) params.limit = filter.limit

  return params
}
