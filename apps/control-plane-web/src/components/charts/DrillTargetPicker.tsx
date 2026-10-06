import { useState } from 'react'
import { useInstanceSearch } from '@/api/instances'
import { useDebounced } from '@/lib/use-debounced'
import type { NodeInfo } from '@/api/nodes'
import {
  DrillTargetPicker as DrillTargetPickerView,
  type DrillTarget,
} from '@jianmanager/ui/components/views/instances/DrillTargetPicker'

// 类型与纯函数原样再导出（调用方与测试直接引用）。
export type { DrillTarget, DrillNode } from '@jianmanager/ui/components/views/instances/DrillTargetPicker'
export { targetKey } from '@jianmanager/ui/components/views/instances/DrillTargetPicker'

/** 候选窗口大小（与 InstancePicker 的约定一致）。 */
const CANDIDATE_LIMIT = 50
/** 键入防抖（毫秒）。 */
const SEARCH_DEBOUNCE_MS = 250

/**
 * 下钻目标选择器的应用接线层（ADR-097 a 范式）。
 *
 * 选择器本体已迁入组件库并受控；本层按当前节点向服务端搜索实例候选（FR-235 范式：
 * 默认取前 N、键入防抖、截断提示），并把候选窗口喂给内嵌的 InstancePicker。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function DrillTargetPicker({
  target,
  onChange,
  nodes,
  worlds,
}: {
  target: DrillTarget
  onChange: (t: DrillTarget) => void
  nodes: NodeInfo[]
  /** 当前实例的世界名列表（来自其分世界序列）；instance 层下钻到世界用。 */
  worlds: string[]
}) {
  const [query, setQuery] = useState('')
  const debouncedQuery = useDebounced(query.trim(), SEARCH_DEBOUNCE_MS)

  // node 层才需要实例候选；按当前节点过滤（服务端做，不本地过滤全量数组）。
  const nodeId = target.kind === 'node' ? nodes.find((n) => n.uuid === target.uuid)?.id : undefined
  const { data } = useInstanceSearch({
    ...(debouncedQuery ? { q: debouncedQuery } : {}),
    ...(nodeId !== undefined ? { nodeId } : {}),
    pageSize: CANDIDATE_LIMIT,
    sort: 'name' as const,
    order: 'asc' as const,
  })

  return (
    <DrillTargetPickerView
      target={target}
      onChange={onChange}
      nodes={nodes}
      worlds={worlds}
      instanceItems={data?.items ?? []}
      instanceTotal={data?.total}
      onInstanceQuery={setQuery}
    />
  )
}
