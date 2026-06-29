import type { NodeInfo } from '@/api/nodes'
import type { ComboboxOption } from '@/components/ui/combobox'

/** buildNodeOptions 仅需节点的标识与状态字段（结构化子集，便于解耦与纯函数测试）。 */
export type NodeOptionInput = Pick<NodeInfo, 'id' | 'name' | 'status'> & { maintenance?: boolean }

/** 节点状态文案集（由调用方注入 i18n 文案，便于纯函数测试）。 */
export interface NodeStatusLabels {
  online: string
  offline: string
  starting: string
  maintenance: string
}

/**
 * 构造创建实例向导的节点下拉项（FIX-8）。
 *
 * 列出**全部**节点（不再只列在线）：创建实例仅持久化记录，节点离线/启动中也能建、上线后再注册并启动；
 * 原 `filter(status===1)` 致节点离线或尚未上报心跳时下拉为空且无任何提示（用户报「节点选不了、都是空的」）。
 * 状态与维护态标注在标签上，离线节点可选但一目了然。
 */
export function buildNodeOptions(
  nodes: NodeOptionInput[] | undefined,
  labels: NodeStatusLabels,
): ComboboxOption[] {
  return (nodes ?? []).map((n) => {
    const status = n.status === 1 ? labels.online : n.status === 2 ? labels.starting : labels.offline
    const maint = n.maintenance ? ` · ${labels.maintenance}` : ''
    return { value: String(n.id), label: `${n.name}（${status}${maint}）` }
  })
}
