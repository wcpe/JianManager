import { useQuery } from '@tanstack/react-query'
import type { DbTableInfo, DbRowsResult } from '@/lib/database/db-contracts'
import api from '@/api/client'

/**
 * 数据库资源管理器（FR-084）API client。
 * 平台管理员只读浏览 Control Plane 自身数据库（表清单 + 分页行）；
 * 敏感列由后端脱敏（前端再兜底打码），无任何写端点。
 */

/**
 * 数据库浏览契约（FR-084）已回迁应用侧，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type { DbTableInfo, DbColumn, DbRowsResult } from '@/lib/database/db-contracts'

/** 行查询参数：分页 / 排序 / 简单过滤（列必须命中表列，否则后端忽略）。 */
export interface DbRowsParams {
  page?: number
  pageSize?: number
  sort?: string
  order?: 'asc' | 'desc'
  filterColumn?: string
  filterValue?: string
}

/** 列出 CP 数据库全部表及行数（仅平台管理员）。 */
export function useDbTables() {
  return useQuery({
    queryKey: ['db', 'tables'],
    queryFn: async () => {
      const { data } = await api.get<{ tables: DbTableInfo[] }>('/db/tables')
      return data.tables
    },
  })
}

/**
 * 分页查询某表的行（仅平台管理员，敏感列已脱敏）。
 * queryKey 含表名与全部分页/排序/过滤参数，切表/翻页/排序/过滤即重查（仅拉当前页，大表不卡）；
 * placeholderData 保留上一页结果，翻页/改排序时表格不闪。
 */
export function useDbTableRows(table: string, params: DbRowsParams) {
  return useQuery({
    queryKey: ['db', 'rows', table, params],
    queryFn: async () => {
      const { data } = await api.get<DbRowsResult>(
        `/db/tables/${encodeURIComponent(table)}/rows`,
        { params },
      )
      return data
    },
    enabled: !!table,
    placeholderData: (prev) => prev,
  })
}
