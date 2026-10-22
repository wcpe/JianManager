/**
 * 实例页的「列设置 / 保存视图」本地偏好（页面重构件）。
 *
 * 键统一走 `jm.instances.*`：
 * - `jm.instances.columns`：可见的可选列（缺省=常用三列，见 `DEFAULT_COLUMNS`；固定列不参与存储）；
 * - `jm.instances.views`：保存的视图 = { 名称, 整串 search 快照, 列集合 }——
 *   页面有 `location.search → 状态` 的同步 effect，故恢复视图只需导航回该 search，
 *   过滤/分组/排序/页大小等全部字段天然保真，无需逐字段枚举。
 */
export const INSTANCE_OPTIONAL_COLUMNS = ['type', 'role', 'tags', 'metrics', 'heartbeat'] as const

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
export const DEFAULT_COLUMNS: readonly InstanceOptionalColumn[] = ['type', 'role', 'tags']

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
