/** 
 * 实例页的「列设置 / 保存视图」本地偏好（页面重构件）。
 *
 * 键统一走 `jm.instances.*`：
 * - `jm.instances.columns`：可见的可选列（缺省=常用三列，见 `DEFAULT_COLUMNS`；固定列不参与存储）；
 * - `jm.instances.views`：保存的视图 = { 名称, 整串 search 快照, 列集合 }——
 *   页面有 `location.search → 状态` 的同步 effect，故恢复视图只需导航回该 search，
 *   过滤/分组/排序/页大小等全部字段天然保真，无需逐字段枚举。
 */
export const INSTANCE_OPTIONAL_COLUMNS = ['type', 'role', 'tags', 'metrics', 'active', 'heartbeat', 'business'] as const

export type InstanceOptionalColumn = (typeof INSTANCE_OPTIONAL_COLUMNS)[number]

/** 
 * 缺省可见的可选列：**不全开**。
 *
 * 五列全开时实测表格总宽 1604px（Chromium 1280 宽，容器仅 974px），横向溢出 630px，
 * 「状态」被截、「操作」整列被挤出视口——默认视图反而看不到最该看的两个东西。
 * 其中「实时指标」(240) 与「心跳」(240) 是两步宽度大头，且都是**按需深看**的信息。
 *
 * 故缺省只开「类型 / 角色 / 标签」三列（合计约 386px），把状态与操作放回视口内；
 * 需要盯指标或心跳时在「显示 › 列」里勾选，勾选结果仍会被记住。
 */
export const DEFAULT_COLUMNS: readonly InstanceOptionalColumn[] = ['type', 'role', 'tags', 'metrics', 'active']

/** 
 * 「业务插件」列**默认关**，与上面同理：它又是一列（约 120px），
 * 默认全开会让本已紧张的横向空间再挤掉「状态/操作」。
 *
 * 需要看「这台服装了哪些业务插件、哪些挂了」时在「显示 › 列」里勾选；
 * 勾选结果同样会被记住。该列走批量端点（50 台上限），不会逐行打探针。
 */

/** 保存的视图快照。 */
export interface SavedInstanceView {
  name: string
  /** 保存时的 `location.search`（含 `?`；空串表示默认视图）。 */
  search: string
  /** 保存时的可见可选列。 */
  columns: InstanceOptionalColumn[]
}

const COLUMNS_KEY = 'jm.instances.columns'
const VIEWS_KEY = 'jm.instances.views'

/** 读取可见可选列；无存储/非法值/存储键版本不符时取缺省集。 */
export function readStoredColumns(): Set<InstanceOptionalColumn> {
  try {
    const raw = localStorage.getItem(COLUMNS_KEY)
    if (raw === null) return new Set(DEFAULT_COLUMNS)
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return new Set(DEFAULT_COLUMNS)
    return new Set(parsed.filter((v): v is InstanceOptionalColumn => INSTANCE_OPTIONAL_COLUMNS.includes(v)))
  } catch {
    return new Set(DEFAULT_COLUMNS)
  }
}

/** 写入可见可选列（失败静默：隐私模式等场景仅本次会话生效）。 */
export function writeStoredColumns(columns: Iterable<InstanceOptionalColumn>): void {
  try {
    localStorage.setItem(COLUMNS_KEY, JSON.stringify([...columns]))
  } catch {
    /* 忽略：仅本会话生效 */
  }
}

/** 读取保存的视图列表（新→旧）。 */
export function readStoredViews(): SavedInstanceView[] {
  try {
    const raw = localStorage.getItem(VIEWS_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter(
      (v): v is SavedInstanceView =>
        !!v &&
        typeof v === 'object' &&
        typeof (v as SavedInstanceView).name === 'string' &&
        typeof (v as SavedInstanceView).search === 'string' &&
        Array.isArray((v as SavedInstanceView).columns),
    )
  } catch {
    return []
  }
}

/** 写入保存的视图列表。 */
export function writeStoredViews(views: SavedInstanceView[]): void {
  try {
    localStorage.setItem(VIEWS_KEY, JSON.stringify(views))
  } catch {
    /* 忽略：仅本会话生效 */
  }
}

/** 
 * 列顺序（表头拖拽换位）。
 *
 * 【为什么用「稳定 key 数组」而不是存 DOM 位置】列是语义单元，不是位置：把「状态」拖到最前
 * 与「第二个格子」是同一件事，但只有前者在列集合变化（隐藏/显示可选列）后仍然成立。
 * 存 key 数组还能在升级后自愈——见 `readStoredColumnOrder` 的补全逻辑。
 *
 * `select`（复选框）与 `actions`（操作）**不参与拖拽**：一个必须固定在最左侧才能批量选择，
 * 一个固定在行尾才符合「行内动作跟着行尾」的扫读习惯。它们仍出现在顺序数组里，
 * 但被钉住位置，拖拽时直接跳过。
 */
export const INSTANCE_COLUMN_KEYS = [
  'select',
  'region',
  'name',
  'identity',
  'nodePort',
  'status',
  'health',
  'business',
  'actions',
] as const

export type InstanceColumnKey = (typeof INSTANCE_COLUMN_KEYS)[number]

/** 不可拖拽的列（固定在首/尾）。 */
export const PINNED_COLUMNS: readonly InstanceColumnKey[] = ['select', 'actions']

export const DEFAULT_COLUMN_ORDER: readonly InstanceColumnKey[] = INSTANCE_COLUMN_KEYS

const ORDER_KEY = 'jm.instances.columnOrder'

/** 
 * 读取列顺序；存储缺失/非法/含未知 key 时回落默认。
 *
 * 补全规则：存储里**只保留已知 key**，再把缺失的已知 key 按默认顺序追加。
 * 这样新增列（未来加一列）不会因为旧存储而被永久隐藏——旧用户升级后自动看到新列，
 * 且他此前拖过的相对顺序仍被保留。
 */
export function readStoredColumnOrder(): InstanceColumnKey[] {
  const fallback = [...DEFAULT_COLUMN_ORDER]
  try {
    const raw = localStorage.getItem(ORDER_KEY)
    if (!raw) return fallback
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return fallback
    const known = new Set<string>(INSTANCE_COLUMN_KEYS)
    const kept = parsed.filter((k): k is InstanceColumnKey => typeof k === 'string' && known.has(k))
    // 去重（存储被手工改坏时可能出现重复 key，重复会导致 React key 冲突与列重复渲染）
    const seen = new Set<InstanceColumnKey>()
    const deduped = kept.filter((k) => (seen.has(k) ? false : (seen.add(k), true)))
    for (const k of DEFAULT_COLUMN_ORDER) if (!seen.has(k)) deduped.push(k)
    return deduped
  } catch {
    return fallback
  }
}

/** 写入列顺序（失败静默：隐私模式下 localStorage 不可写不应影响页面）。 */
export function writeStoredColumnOrder(order: readonly InstanceColumnKey[]): void {
  try {
    localStorage.setItem(ORDER_KEY, JSON.stringify(order))
  } catch {
    /* 忽略：无存储能力时退化为「本次会话内有效」 */
  }
}

/** 
 * 把 `from` 位置的列移动到 `to` 位置，返回新数组。
 *
 * 纯函数，便于单测；`to` 按「插入到该下标之前」解释，越界自动收敛到两端。
 */
export function moveColumn(
  order: readonly InstanceColumnKey[],
  from: InstanceColumnKey,
  to: InstanceColumnKey,
): InstanceColumnKey[] {
  if (from === to) return [...order]
  // 钉住的列不参与换位：拖不动，也不能被别人挤开（保持首/尾语义）
  if (PINNED_COLUMNS.includes(from) || PINNED_COLUMNS.includes(to)) return [...order]

  const fromIndex = order.indexOf(from)
  const toIndex = order.indexOf(to)
  if (fromIndex < 0 || toIndex < 0) return [...order]

  /* 
    【语义：移动到目标列的位置，方向决定插入点在目标的前面还是后面】
    初版写成「先摘掉 from，再插到 to 之前」，看似通用，实际对**向前拖**（from 在 to 左侧）
    完全无效：摘掉 from 后 to 的下标左移一位，插回 to 之前就等于插回原位，
    用户把「区域」拖到「名称」上会毫无反应（实测如此）。
    正确做法是按拖动方向决定插在目标之前还是之后：
    - 向前拖（fromIndex < toIndex）：落到目标**之后**，与「拖到我这个位置」的直觉一致；
    - 向后拖（fromIndex > toIndex）：落到目标**之前**。
    两种情形都让 from 恰好占据 to 原来的那一格。
  */
  const next = [...order]
  next.splice(fromIndex, 1)
  // 摘掉 from 后，若 from 在 to 之前，to 的下标已经左移 1。
  const insertAt = fromIndex < toIndex ? toIndex : toIndex
  next.splice(insertAt, 0, from)
  return next
}

/** 
 * 计算「本视图实际渲染的列顺序」——**表头 / 行 / colgroup 三处必须共用此函数**。
 *
 * 【为什么必须有这么一个函数】这三处此前各写了一遍 `present` 判断与兜底补全逻辑，
 * 结果同一类 bug 连续复现三次：
 *   1. 表头与行不同步（列顺序改了、行还用旧顺序）；
 *   2. 健康度格未跟随列设置（表头少一列、行内多一格）；
 *   3. 新增 `business` 列时，表头与行都加了、colgroup 漏了（少一个 `<col>`，整表列宽错位）。
 * 三处只要有一处漏改就错列，而错列在小数据量、默认列设置下不一定看得出来。
 * 收成一个纯函数后，「加一列」只需要改 `INSTANCE_COLUMN_KEYS` 与宽度表，
 * 三处自动一致——这类 bug 从结构上消失。
 */
export function computeVisibleColumns(
  columnOrder: readonly InstanceColumnKey[] | undefined,
  visibleColumns: ReadonlySet<string> | undefined,
): InstanceColumnKey[] {
  const order = columnOrder ?? DEFAULT_COLUMN_ORDER
  const present = new Set<InstanceColumnKey>(['select', 'region', 'name', 'nodePort', 'status', 'actions'])

  // 身份列：type / role / tags 任一可见即渲染合并后的「身份」格
  if (visibleColumns?.has('type') || visibleColumns?.has('role') || visibleColumns?.has('tags')) {
    present.add('identity')
  }
  // 健康度列：metrics / active / heartbeat 任一可见即渲染合并后的「健康度」格
  if (visibleColumns?.has('metrics') || visibleColumns?.has('active') || visibleColumns?.has('heartbeat')) {
    present.add('health')
  }
  // 业务插件列：单一开关
  if (visibleColumns?.has('business')) present.add('business')

  const ordered = order.filter((k) => present.has(k))
  // 兜底：顺序数组里缺失的已知列按默认顺序补在末尾，保证不漏列（旧存储/新增列）。
  for (const k of DEFAULT_COLUMN_ORDER) {
    if (present.has(k) && !ordered.includes(k)) ordered.push(k)
  }
  return ordered
}

/** 
 * 多选交互（用户提出：Shift 连选 / Ctrl 点选 / 反选）。
 *
 * 【为什么抽成纯函数】这三种交互的边界情况很多（无锚点、锚点不在当前页、跨页拖选、
 * 已选集合含不在当前列表的 id），写在组件里只能用 DOM 事件测，且列表与卡片两处会各写一套。
 * 抽出来后：输入「当前顺序 + 已选集合 + 点击目标 + 修饰键 + 锚点」→ 输出新的已选集合，
 * 可以在单测里把每种组合穷举一遍，两个视图共用同一份语义。
 *
 * 约定（与文件管理器/表格的通行手感一致）：
 * - 单击（无修饰键）：把选择集替换成这一项（想多选时按 Ctrl）；
 * - Ctrl / Meta + 单击：切换该项；
 * - Shift + 单击：以锚点为一端、该目标为另一端，选中区间内所有项；无锚点时退化为单击；
 * - 反选：当前可见集合里，已选的去掉、未选的加上（不在可见集合里的保持不动）。
 */
export interface SelectionInput {
  /** 当前视图的展示顺序（决定 Shift 连选覆盖哪些项）。 */
  orderedIds: readonly number[]
  /** 当前已选集合。 */
  selected: ReadonlySet<number>
  /** 本次点击的目标项。 */
  targetId: number
  /** 修饰键。 */
  shiftKey?: boolean
  ctrlKey?: boolean
  metaKey?: boolean
}

/** 处理一次点击后应得到的新选中集合。 */
export function applySelectionClick({
  orderedIds,
  selected,
  targetId,
  shiftKey = false,
  ctrlKey = false,
  metaKey = false,
}: SelectionInput): Set<number> {
  const next = new Set(selected)

  // Shift 连选：区间按「当前展示顺序」取，锚点取已选集合里在顺序中最靠前的那一项。
  if (shiftKey) {
    const targetIndex = orderedIds.indexOf(targetId)
    if (targetIndex < 0) return next
    // 锚点 = 已选集合中位于顺序表里的任意一项；用最靠前者，行为可预测。
    let anchorIndex = -1
    for (let i = 0; i < orderedIds.length; i++) {
      if (next.has(orderedIds[i]!)) {
        anchorIndex = i
        break
      }
    }
    // 无锚点（当前什么都没选）时，Shift 点击退化为单选，而不是「选中从头到这里的全部」。
    if (anchorIndex < 0) {
      next.add(targetId)
      return next
    }
    const [from, to] = anchorIndex <= targetIndex ? [anchorIndex, targetIndex] : [targetIndex, anchorIndex]
    for (let i = from; i <= to; i++) next.add(orderedIds[i]!)
    return next
  }

  // Ctrl / Cmd：切换单项（不清空其它选择）
  if (ctrlKey || metaKey) {
    if (next.has(targetId)) next.delete(targetId)
    else next.add(targetId)
    return next
  }

  // 无修饰键：替换为单项
  return new Set([targetId])
}

/** 
 * 反选：在当前可见集合内翻转选中状态。
 *
 * 只翻转 `orderedIds` 里的项——不在当前页/当前筛选结果里的已选项保持原样，
 * 否则「反选」会静默丢掉用户在其他页选好的内容。
 */
export function invertSelection(
  orderedIds: readonly number[],
  selected: ReadonlySet<number>,
): Set<number> {
  const next = new Set(selected)
  for (const id of orderedIds) {
    if (next.has(id)) next.delete(id)
    else next.add(id)
  }
  return next
}
