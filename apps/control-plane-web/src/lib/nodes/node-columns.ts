import { readStoredOrder, writeStoredOrder, moveColumnKey } from '@/lib/shared/column-order'

/**
 * 节点表格的列模型（表头拖拽换位，用户要求「表头参考服务器列表可以拖拽」）。
 *
 * 【为什么不复用实例页的 INSTANCE_COLUMN_KEYS】两者语义不同（实例有角色/标签/健康度，
 * 节点有系统/隧道/心跳）。混用会让「给实例加一列」意外影响节点页，反之亦然。
 * 共用的是**实现**（`lib/shared/column-order`），不是列定义。
 */
export const NODE_COLUMN_KEYS = [
  'status',
  'name',
  'address',
  'system',
  'load',
  'instances',
  'tunnel',
  'heartbeat',
] as const

export type NodeColumnKey = (typeof NODE_COLUMN_KEYS)[number]

/** 不可拖拽的列：状态钉在最左（扫读的第一列），其余都可拖。 */
export const NODE_PINNED_COLUMNS: readonly NodeColumnKey[] = ['status']

export const NODE_DEFAULT_ORDER: readonly NodeColumnKey[] = NODE_COLUMN_KEYS

const ORDER_KEY = 'jm.nodes.columnOrder'

export function readStoredNodeOrder(): NodeColumnKey[] {
  return readStoredOrder(ORDER_KEY, NODE_COLUMN_KEYS, NODE_DEFAULT_ORDER)
}

export function writeStoredNodeOrder(order: readonly NodeColumnKey[]): void {
  writeStoredOrder(ORDER_KEY, order)
}

export function moveNodeColumn(from: NodeColumnKey, to: NodeColumnKey): NodeColumnKey[] {
  return moveColumnKey(readStoredNodeOrder(), from, to, NODE_PINNED_COLUMNS)
}

/** 纯函数版换位（供页面用当前 order 而非存储值计算）。 */
export function applyNodeColumnMove(
  order: readonly NodeColumnKey[],
  from: NodeColumnKey,
  to: NodeColumnKey,
): NodeColumnKey[] {
  return moveColumnKey(order, from, to, NODE_PINNED_COLUMNS)
}

/** 列宽（px）。名称列吃剩余宽度，故不设值。 */
export const NODE_COLUMN_WIDTH: Record<NodeColumnKey, number | undefined> = {
  /*
    合计必须 <= 内容区宽度（1280 视口下实测 964px，含侧栏与内边距）。
    初版写 1032 超出 68px，而容器 `overflow-auto` 在 `table-fixed` 下没触发横向滚动，
    结果是「隧道」被压、「心跳」整列看不见——最右侧的列静默消失是最难发现的一类缺陷。
    现在合计 932，留 32px 余量；改列宽时请按同一算式核对。
  */
  status: 88,
  name: 160,
  address: 140,
  system: 130,
  load: 180,
  instances: 80,
  tunnel: 76,
  heartbeat: 78,
}

/** 分页大小档位（与服务器页同一组数值，减少两页之间的认知切换）。 */
export const NODE_PAGE_SIZES = [10, 20, 50] as const
export const NODE_DEFAULT_PAGE_SIZE = 20
