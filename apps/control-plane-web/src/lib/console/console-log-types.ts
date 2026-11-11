/** 
 * 日志条目的类型（控制台历史等）。
 *
 * 从应用侧 `api/logs.ts` 抽出：那里混着取数 hook，而日志条目是纯数据，
 * 纯逻辑与视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

export interface LogEntry {
  id: number
  /** 来源：instance（实例）/ control_plane（平台）/ worker。 */
  source: string
  /**
   * 来源原始标识（`inst:2/file` / `node:1/file`），后端 `log_source_id` 的同名值。
   *
   * 为何不能只靠 `instanceId` / `nodeId`：联邦事件并不保证带这两个结构化字段
   * （不同采集模式与版本差异很大），而 `log_source_id` 是采集侧必写的流标签。
   * 展示「具体是哪个实例/节点」时以本字段为真源，结构化字段只做兜底。
   */
  sourceRef?: string
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

/** 日志查询筛选条件（DB 侧过滤 + 分页）。 */
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
