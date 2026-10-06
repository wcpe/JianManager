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
