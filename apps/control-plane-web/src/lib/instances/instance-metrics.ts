/**
 * 实例实时指标的契约（FR-446 / FR-447）。
 *
 * 归包理由：监控段的直探卡、健康条与来源标注都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 单个世界的实时统计。 */
export interface WorldMetric {
  name: string
  loadedChunks: number
  entities: number
  tileEntities: number
}

/** 实例实时指标一拍。 */
export interface InstanceMetricsData {
  tps: number
  onlinePlayers: number
  memoryMb: number
  msptMillis: number
  threads: number
  cpuPercent: number
  heapMaxMb: number
  uptimeSeconds: number
  worlds: WorldMetric[] | null
  probeAvailable: boolean
  // === MC 直探（SLP / Query，FR-446 / FR-447）补充字段与显式可用性位 ===
  // 采集优先级链 `探针 → SLP → Query → 不可用`：`*Available=false` 即「不可用」，
  // 前端据此渲染「不可用」而非 `0` / `-1` / `--` 伪值（消除「0 人在线」回归）。
  playersAvailable: boolean
  motd: string
  motdAvailable: boolean
  version: string
  versionAvailable: boolean
  /**
   * 服务端图标（`data:image/png;base64,...`，可达 ~50KB）。**预留字段**：后端 SLP → Worker →
   * `MetricsData.favicon` 已端到端回传，前端当前仅声明未渲染；若要展示应在实例详情页 MOTD
   * 卡片按需渲染，避免把大 base64 注入卡片列表（FR-446 审计项 7）。
   */
  favicon: string
  maxPlayers: number
  maxPlayersAvailable: boolean
  playerNames: string[] | null
  playerNamesAvailable: boolean
  /** true=名单取自 SLP sample（弱信息，可能不完整，非实名）。 */
  playerNamesPartial: boolean
  plugins: string[] | null
  pluginsAvailable: boolean
  map: string
  mapAvailable: boolean
  slpAvailable: boolean
  queryAvailable: boolean
  /** 本拍命中来源位：1=探针 2=SLP 4=Query（与后端 `metrics.SourceMask` 对齐）。 */
  sourceMask: number
}
