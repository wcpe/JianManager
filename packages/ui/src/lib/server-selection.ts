/**
 * 服务器选择器的共享类型（FR-240 / FR-293）。
 *
 * 归包理由：选择器视图要读「最近/收藏」的条目结构，并用外壳注入的悬停预取器，
 * 两者都是双侧共用的形状；实现（localStorage store、TanStack Query 预取）仍留在应用侧。
 */

/** 「最近打开 / 收藏」里存的一条实例（轻量快照，不随实例变更实时更新）。 */
export interface StoredInstance {
  id: number
  uuid: string
  nodeId: number
  name: string
  status: string
}

/** 悬停预取器：稳定悬停一段时间后才预取，避免扫过列表时打出无谓请求。 */
export interface HoverPrefetcher {
  /** 悬停进入某实例行：重置计时并对该 id 起防抖计时。 */
  enter: (id: number) => void
  /** 悬停离开：取消未触发的预取。 */
  leave: () => void
  /** 组件卸载清理：等价 leave。 */
  cancel: () => void
}
