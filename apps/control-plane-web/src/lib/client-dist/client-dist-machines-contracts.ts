/** 
 * 机器级更新清单与钻取契约。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** 机器更新聚合项。
 *
 * 字段与后端 `service.ClientMachineSummary` 逐字对齐（CP 侧 SQL 聚合产出）。
 * machineId 已是哈希值，展示层无需再脱敏。
 */
export interface ClientMachineSummary {
  machineId: string
  channelId: string
  /** 窗口内更新总次数。 */
  updates: number
  /** 最近一次成功/尝试更新时间（ISO）。 */
  lastUpdateAt: string
  latestVersion: number
  currentVersion: number
  /** latest - current；0=已最新。 */
  versionLag: number
  downloadBytes: number
  /**
   * 玩家名。来自运行态心跳快照（`client_runtime_state`），不是明细表聚合得出。
   *
   * **可能为空串**：老客户端未上报、或该机器从未发过心跳（只拉取不启动）。
   * 视图统按「未上报」渲染，不要当成真实玩家名。
   */
  playerName: string
  /** 安装 ID。来自遥测明细（`client_telemetry.install_id`）；未上报/已过保留期时为空串。 */
  installId: string
  /** 最近来源 IP。来自运行态心跳快照；未知时为空串。 */
  ip: string
}

/** 单机器更新事件（时间线钻取项）。 */
export interface ClientMachineEvent {
  ts: string
  version: number
  result: 'success' | 'fail-static' | 'rollback' | 'error'
  bytes: number
  durationMs: number
}

export interface ClientMachineListResponse {
  channelId: string
  from: string
  to: string
  /** true=窗口在明细保留窗(14d)内，聚合精确；false=超窗近似。 */
  exact: boolean
  total: number
  page: number
  pageSize: number
  items: ClientMachineSummary[]
}

export interface ClientMachineEventsResponse {
  machineId: string
  channelId: string
  from: string
  to: string
  items: ClientMachineEvent[]
}

export type MachineSortField = 'updates' | 'lastUpdateAt' | 'versionLag'
