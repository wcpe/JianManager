/**
 * 审计契约（FR-015 / FR-172 / FR-321，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内筛选逻辑与应用侧 api 共用。
 */

export interface AuditLogInfo {
  id: number
  uuid: string
  userId: number
  action: string
  targetType: string
  targetId: string
  detail: string
  ip: string
  /** 操作是否失败（FR-321：失败操作也留痕；历史行零值=未失败）。 */
  failed: boolean
  /** 失败时的错误内容（响应 error body 截断，FR-321）。 */
  error: string
  createdAt: string
  user?: { id: number; username: string }
}

/**
 * 审计日志筛选参数（FR-015）：任意组合，留空表示该维度不过滤。
 * 全部透传为 `GET /audit` 的 query；后端按 RFC3339 解析 from/to。
 */
export interface AuditQueryParams {
  userId?: number
  action?: string
  targetType?: string
  /** 起始时间（RFC3339，含时区，如 2026-06-22T10:30:00Z）。 */
  from?: string
  /** 结束时间（RFC3339，含时区）。 */
  to?: string
}

export interface AuditLogPage {
  items: AuditLogInfo[]
  total: number
  page: number
  pageSize: number
}
