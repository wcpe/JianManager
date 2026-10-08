/**
 * 数据库资源管理器契约（FR-084，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** 表清单项：表名与行数，行数小于零时表示无法统计。 */
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
