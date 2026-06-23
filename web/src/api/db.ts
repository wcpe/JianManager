import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 数据库资源管理器（FR-084）API client。
 * 平台管理员只读浏览 Control Plane 自身数据库（表清单 + 分页行）；
 * 敏感列由后端脱敏（前端再兜底打码），无任何写端点。
 */

/** 表清单中的一项：表名 + 行数（行数未知时后端回 -1）。 */
export interface DbTableInfo {
  name: string
  rowCount: number
}

/** 一列的定义：名称 / 数据库类型 / 是否敏感（敏感列值已脱敏）。 */
export interface DbColumn {
  name: string
  type: string
  sensitive: boolean
}

/** GET /db/tables/:name/rows 响应：列定义 + 当前页行 + 分页元信息。 */
export interface DbRowsResult {
  table: string
  columns: DbColumn[]
  /** 行集合，键为列名；值类型随列而定（敏感列已被替换为打码占位）。 */
  rows: Array<Record<string, unknown>>
  page: number
  pageSize: number
  total: number
}

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
