/**
 * 实例分组的核心契约（FR-165）。
 *
 * 归包理由：分组树视图与树的纯逻辑都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 一个实例分组节点。 */
export interface InstanceGroupNode {
  id: number
  uuid: string
  name: string
  /** 父节点 ID，null=根分组。 */
  parentId: number | null
  sort: number
  /** 子树（含自身及所有后代）去重后的实例数。 */
  instanceCount: number
  /**
   * 该组**直接**挂载（不含后代）的实例 ID（FR-452）。
   * 供列表页 `groupTree` 维度一次取数构建「实例→组」映射，避免 per-group N+1。旧后端缺省。
   */
  memberInstanceIds?: number[]
}
