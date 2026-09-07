/**
 * 沉浸控制台的 pane 树纯逻辑（FR-420 / FR-421）。
 *
 * 组件只负责把树画成 flex 布局；分割、切换实例和关闭都在这里按不可变数据处理，
 * 避免把递归状态转换散落在可见按钮的事件处理器里。
 */

export type PaneSplitDirection = 'horizontal' | 'vertical'

/** 分割点占比下限：再小的一面就读不了日志了（ADR-087 可用性兜底）。 */
export const MIN_RATIO = 0.2
/** 分割点占比上限。 */
export const MAX_RATIO = 0.8

export interface ImmersivePaneLeaf {
  type: 'leaf'
  id: string
  instanceId: number
}

export interface ImmersivePaneSplit {
  type: 'split'
  direction: PaneSplitDirection
  first: ImmersivePaneTree
  second: ImmersivePaneTree
  /**
   * 分割点占容器的比例（first 侧的份额，0-1）。
   * 省略时两半均分（flex-1）；拖拽分隔条后写入并钳制在 [MIN_RATIO, MAX_RATIO]。
   */
  ratio?: number
}

export type ImmersivePaneTree = ImmersivePaneLeaf | ImmersivePaneSplit

/** 把任意比例钳制到可用的分割区间。 */
export function clampRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) return 0.5
  return Math.min(MAX_RATIO, Math.max(MIN_RATIO, ratio))
}

export function paneLeaves(tree: ImmersivePaneTree): ImmersivePaneLeaf[] {
  if (tree.type === 'leaf') return [tree]
  return [...paneLeaves(tree.first), ...paneLeaves(tree.second)]
}

export function paneCount(tree: ImmersivePaneTree): number {
  return paneLeaves(tree).length
}

export function findPane(tree: ImmersivePaneTree, paneId: string): ImmersivePaneLeaf | null {
  return paneLeaves(tree).find((pane) => pane.id === paneId) ?? null
}

/** 在目标 leaf 的第二半插入新 pane；目标不存在时原样返回。 */
export function splitPane(
  tree: ImmersivePaneTree,
  paneId: string,
  direction: PaneSplitDirection,
  nextPane: ImmersivePaneLeaf,
): ImmersivePaneTree {
  if (tree.type === 'leaf') {
    return tree.id === paneId ? { type: 'split', direction, first: tree, second: nextPane } : tree
  }

  const first = splitPane(tree.first, paneId, direction, nextPane)
  if (first !== tree.first) return { ...tree, first }
  const second = splitPane(tree.second, paneId, direction, nextPane)
  return second === tree.second ? tree : { ...tree, second }
}

/**
 * 移除一个 pane，并把只剩一个子树的 split 自动收缩。
 *
 * 返回 null 表示根 leaf 被移除；调用方据此决定最后一个 pane 是否允许关闭。
 */
export function removePane(tree: ImmersivePaneTree, paneId: string): ImmersivePaneTree | null {
  if (tree.type === 'leaf') return tree.id === paneId ? null : tree
  const first = removePane(tree.first, paneId)
  const second = removePane(tree.second, paneId)
  if (first === tree.first && second === tree.second) return tree
  if (!first) return second
  if (!second) return first
  return { ...tree, first, second }
}

/** 只替换目标 pane 的实例来源；找不到目标时保留同一棵树引用。 */
export function replacePaneInstance(tree: ImmersivePaneTree, paneId: string, instanceId: number): ImmersivePaneTree {
  if (tree.type === 'leaf') {
    return tree.id === paneId ? { ...tree, instanceId } : tree
  }
  const first = replacePaneInstance(tree.first, paneId, instanceId)
  if (first !== tree.first) return { ...tree, first }
  const second = replacePaneInstance(tree.second, paneId, instanceId)
  return second === tree.second ? tree : { ...tree, second }
}

export function instanceIdsInTrees(trees: readonly ImmersivePaneTree[]): number[] {
  return [...new Set(trees.flatMap((tree) => paneLeaves(tree).map((pane) => pane.instanceId)))]
}

/**
 * 更新目标 split 节点的分割占比；目标按**引用**匹配（渲染层持有的就是状态树里的节点），
 * 找不到时原样返回同一棵树。写入前钳制到 [MIN_RATIO, MAX_RATIO]。
 */
export function setSplitRatio(tree: ImmersivePaneTree, target: ImmersivePaneSplit, ratio: number): ImmersivePaneTree {
  if (tree.type === 'leaf') return tree
  if (tree === target) return { ...tree, ratio: clampRatio(ratio) }
  const first = setSplitRatio(tree.first, target, ratio)
  if (first !== tree.first) return { ...tree, first }
  const second = setSplitRatio(tree.second, target, ratio)
  return second === tree.second ? tree : { ...tree, second }
}

/**
 * 深拷贝整棵树并给每个 leaf 发新 id（restored 回调收旧换新）。
 *
 * 从 sessionStorage 恢复布局时必须重编 id：历史快照里的 id 可能与新会话的
 * `pane-<instanceId>-<n>` 序列撞车（React key 冲突 + 焦点错乱）。
 */
export function reidTree(tree: ImmersivePaneTree, nextId: (oldId: string) => string): ImmersivePaneTree {
  if (tree.type === 'leaf') return { ...tree, id: nextId(tree.id) }
  return {
    ...tree,
    first: reidTree(tree.first, nextId),
    second: reidTree(tree.second, nextId),
  }
}
