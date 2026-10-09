import { describe, expect, it } from 'vitest'

import {
  instanceIdsInTrees,
  MAX_RATIO,
  MIN_RATIO,
  paneCount,
  paneLeaves,
  reidTree,
  replacePaneInstance,
  removePane,
  setSplitRatio,
  splitPane,
  type ImmersivePaneLeaf,
} from './console-immersive-layout'

const A: ImmersivePaneLeaf = { type: 'leaf', id: 'a', instanceId: 1 }
const B: ImmersivePaneLeaf = { type: 'leaf', id: 'b', instanceId: 2 }
const C: ImmersivePaneLeaf = { type: 'leaf', id: 'c', instanceId: 3 }
describe('控制台沉浸模式 pane 树', () => {
  it('按目标 pane 与方向创建嵌套分屏，且不变更原树', () => {
    const horizontal = splitPane(A, 'a', 'horizontal', B)
    const nested = splitPane(horizontal, 'b', 'vertical', C)

    expect(paneCount(A)).toBe(1)
    expect(paneLeaves(nested).map((pane) => pane.id)).toEqual(['a', 'b', 'c'])
    expect(nested).toMatchObject({ type: 'split', direction: 'horizontal' })
  })

  it('关闭 pane 会收缩只剩一个子树的分割，最后一个 leaf 返回 null', () => {
    const tree = splitPane(splitPane(A, 'a', 'horizontal', B), 'b', 'vertical', C)

    const withoutC = removePane(tree, 'c')
    expect(paneLeaves(withoutC!).map((pane) => pane.id)).toEqual(['a', 'b'])
    expect(removePane(withoutC!, 'b')).toEqual(A)
    expect(removePane(A, 'a')).toBeNull()
  })

  it('可视实例选择器只替换目标 pane 的实例，不影响其他 pane', () => {
    const tree = splitPane(splitPane(A, 'a', 'horizontal', B), 'a', 'vertical', C)
    const replaced = replacePaneInstance(tree, 'b', 99)

    expect(paneLeaves(replaced)).toEqual([
      A,
      C,
      { type: 'leaf', id: 'b', instanceId: 99 },
    ])
    expect(replacePaneInstance(tree, 'missing', 99)).toBe(tree)
  })

  it('嵌套分屏去重实例会话，供热集按真实连接数保活', () => {
    const tree = splitPane(splitPane(A, 'a', 'horizontal', B), 'b', 'vertical', C)
    const duplicate = splitPane({ type: 'leaf', id: 'd', instanceId: 4 }, 'd', 'horizontal', { ...A, id: 'a2' })

    expect(instanceIdsInTrees([tree, duplicate])).toEqual([1, 2, 3, 4])
  })

  it('拖拽占比：按引用更新 split 的 ratio 并钳制到 [MIN_RATIO, MAX_RATIO]', () => {
    const tree = splitPane(A, 'a', 'horizontal', B)
    const target = tree as Extract<typeof tree, { type: 'split' }>

    const widened = setSplitRatio(tree, target, 0.68)
    expect(widened).toMatchObject({ ratio: 0.68 })
    // 原树不可变
    expect(tree.ratio).toBeUndefined()

    expect(setSplitRatio(tree, target, 0.05)).toMatchObject({ ratio: MIN_RATIO })
    expect(setSplitRatio(tree, target, 0.95)).toMatchObject({ ratio: MAX_RATIO })
    expect(setSplitRatio(tree, target, Number.NaN)).toMatchObject({ ratio: 0.5 })
    // 引用不匹配（节点已被上一轮更新替换）时原样返回
    expect(setSplitRatio(widened, target, 0.3)).toBe(widened)
  })

  it('快照重编 id：全部 leaf 拿新 id 且结构/实例不变，避免与生成序列撞车', () => {
    const tree = splitPane(splitPane(A, 'a', 'horizontal', B), 'b', 'vertical', C)
    const reid = reidTree(tree, (oldId) => `pane-restored-${oldId}`)

    expect(paneLeaves(reid).map((leaf) => leaf.id)).toEqual([
      'pane-restored-a',
      'pane-restored-b',
      'pane-restored-c',
    ])
    expect(paneLeaves(reid).map((leaf) => leaf.instanceId)).toEqual([1, 2, 3])
    // 原树不受影响
    expect(paneLeaves(tree).map((leaf) => leaf.id)).toEqual(['a', 'b', 'c'])
  })
})
