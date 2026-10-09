import { useState } from 'react'
import { useInstanceSearch, type InstanceInfo } from '@/api/instances'
import { useDebounced } from '@/lib/use-debounced'
import {
  CANDIDATE_LIMIT,
  InstancePicker as InstancePickerView,
  type InstancePickerProps as InstancePickerViewProps,
} from '@/components/views/instances/InstancePicker'

/** 哨兵值由组件库定义，这里再导出以保持既有引用路径可用。 */
export { INSTANCE_PICKER_ALL } from '@/components/views/instances/InstancePicker'

/**
 * 服务端搜索的实例选择器（应用接线层，ADR-097 a 范式）。
 *
 * 选择器本体已迁入组件库并受控；本层承担「何时发请求」这一策略：
 * 默认取前 N 条、键入经 300ms 防抖下发服务端 `q`、按 `nodeIdFilter` 收窄范围。
 * 泛型参数显式写成 `InstanceInfo`，使 `onChange` 的第二参数仍是完整实例对象
 * （调用方可顺手取 serverPort 等字段，无需再查一次列表）。
 */
type PickerProps = Omit<InstancePickerViewProps<InstanceInfo>, 'items' | 'total' | 'onQueryChange'> & {
  /**
   * 限定只搜某个节点上的实例。下钻选择器在 node 层只需列出该节点下的实例；
   * 不传则搜全部（服务端 `/instances/search` 原生支持 nodeId 参数）。
   */
  nodeIdFilter?: number
  /** 只在需要时发请求（例如弹窗打开）。默认 true。 */
  enabled?: boolean
}

export function InstancePicker({ nodeIdFilter, enabled = true, ...rest }: PickerProps) {
  const [kw, setKw] = useState('')
  const q = useDebounced(kw, 300).trim()

  const { data: page } = useInstanceSearch(
    {
      ...(q ? { q } : {}),
      ...(nodeIdFilter != null ? { nodeId: nodeIdFilter } : {}),
      page: 1,
      pageSize: CANDIDATE_LIMIT,
      sort: 'name',
      order: 'asc',
    },
    enabled,
  )

  return (
    <InstancePickerView<InstanceInfo>
      {...rest}
      items={page?.items}
      total={page?.total}
      onQueryChange={setKw}
    />
  )
}
