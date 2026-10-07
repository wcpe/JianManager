/**
 * 日志条目的类型（FR-070 控制台历史等）。
 *
 * 从应用侧 `api/logs.ts` 抽出：那里混着取数 hook，而日志条目是纯数据，
 * 纯逻辑与视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

export interface LogEntry {
  id: number
  /** 来源：instance（实例）/ control_plane（平台）/ worker。 */
  source: string
  /** 级别：debug / info / warn / error。 */
  level: string
  instanceId: number
  instanceUuid: string
  nodeId: number
  /** 原始流名（stdout/stderr），仅实例日志。 */
  stream?: string
  message: string
  /** 日志产生时间（RFC3339）。 */
  time: string
}

/** 日志查询筛选条件（DB 侧过滤 + 分页，FR-049/FR-050）。 */
export interface LogQueryParams {
  /** 主视图：平台仅 CP；节点/实例聚合 Worker 与实例；all 仅平台管理员。 */
  view?: 'platform' | 'node_instance' | 'all' | 'legacy'
  source?: string
  level?: string
  instanceId?: number
  nodeId?: number
  /** 关键字，匹配 message。 */
  keyword?: string
  /** 起始时间（RFC3339）。 */
  from?: string
  /** 结束时间（RFC3339）。 */
  to?: string
  page?: number
  pageSize?: number
}
