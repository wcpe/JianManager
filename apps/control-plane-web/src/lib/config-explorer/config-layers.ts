import type { ConfigLayerHead, ConfigScopeLevel, TracedEntry } from '@/api/configFiles'

/**
 * 分层配置的纯逻辑（FR-529）：层分组、实例层优先级判定、溯源表行构造。
 *
 * 抽成纯函数而非塞在页面里，是因为两条 UI 约束（预览为规范化结果、实例层优先级最高）
 * 的**显示条件**必须可单独断言——它们是真机验收提的要求，写错条件就会误导运维
 * 拿着错误认知去改配置。
 */

/** 层列表的两组：分组层（可多层）与实例层（同一实例至多一层）。 */
export interface GroupedLayers {
  /** 分组层，按分组 ID 升序（同一份配置的分组层通常不多，顺序稳定即可）。 */
  groups: ConfigLayerHead[]
  /** 实例层，按实例 ID 升序。 */
  instances: ConfigLayerHead[]
}

/** 按作用域级别把层列表分成两组；保持组内稳定顺序（scopeRefId 升序）。 */
export function groupLayersByScope(layers: ConfigLayerHead[] | undefined): GroupedLayers {
  const groups: ConfigLayerHead[] = []
  const instances: ConfigLayerHead[] = []
  for (const layer of layers ?? []) {
    if (layer.scopeLevel === 'instance') instances.push(layer)
    else groups.push(layer)
  }
  const byRef = (a: ConfigLayerHead, b: ConfigLayerHead) => a.scopeRefId - b.scopeRefId
  return { groups: [...groups].sort(byRef), instances: [...instances].sort(byRef) }
}

/**
 * 该实例是否已有实例层版本。
 * 实例层存在即意味着作用域链最右端被它占据，分组层的值在这台实例上不可能生效。
 */
export function hasInstanceLayer(layers: ConfigLayerHead[] | undefined, instanceId: number | null): boolean {
  if (instanceId == null) return false
  return (layers ?? []).some((l) => l.scopeLevel === 'instance' && l.scopeRefId === instanceId)
}

/**
 * 是否要显示「实例层优先级最高」提示。
 *
 * 判据（与需求文字逐条对应）：
 * 1. `layers` 里**同时**存在 `scopeLevel=group` 与 `scopeLevel=instance`（两侧都得有，
 *    只有实例层时不构成「分组层覆盖不了它」这句话的语境）；
 * 2. 且当前预览选中的那个实例**自己有** instance 层（别的实例的实例层与本实例无关）。
 *
 * 选中实例为空时（还没选实例）不提示——此时没有任何「哪层生效」的问题。
 */
export function shouldWarnInstancePrecedence(
  layers: ConfigLayerHead[] | undefined,
  instanceId: number | null,
): boolean {
  if (instanceId == null) return false
  const list = layers ?? []
  const hasGroup = list.some((l) => l.scopeLevel === 'group')
  if (!hasGroup) return false
  return hasInstanceLayer(list, instanceId)
}

/** 溯源表的一行（已排序、已格式化，视图只负责渲染）。 */
export interface TracedRow {
  /** 键路径（yaml/json 为点分路径，properties 为平铺键）。 */
  key: string
  /**
   * 值的展示文本（对象/数组会被 JSON 化；被脱敏的键后端已给 `***`）。
   * 被删除的键不在这里拼中文，由视图按 `deleted` 渲染对应译文。
   */
  valueText: string
  /** 来源层级别（`group` / `instance`）。 */
  level: string
  /** 来源名称（分组名/实例名，缺失时为空串）。 */
  sourceName: string
  /** 来源 ID（分组 ID / 实例 ID）。 */
  sourceId: number
  /** 来源版本号。 */
  versionNo: number
  /** 该键被 `null` 删除（yaml/json 语义）。 */
  deleted: boolean
}

/** 把任意值渲染成单行展示文本（对象/数组 JSON 化，字符串原样）。 */
export function formatTracedValue(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

/**
 * 把 `traced`（键 → 值 + 来源）转成稳定排序的行数组。
 *
 * 排序：先按「来源层级别」把实例层排前面（运维最关心实例层压住了什么），
 * 再按版本号降序（新版本优先），最后按键名字典序——保证同样输入渲染顺序恒定，
 * 不会因为对象键序差异在两次刷新之间跳动。
 */
export function buildTracedRows(traced: Record<string, TracedEntry> | undefined): TracedRow[] {
  const rows: TracedRow[] = []
  for (const [key, entry] of Object.entries(traced ?? {})) {
    rows.push({
      key,
      valueText: formatTracedValue(entry.value),
      level: entry.source?.level ?? '',
      sourceName: entry.source?.name ?? '',
      sourceId: entry.source?.id ?? 0,
      versionNo: entry.source?.versionNo ?? 0,
      deleted: entry.deleted === true,
    })
  }
  const levelRank = (level: string) => (level === 'instance' ? 0 : 1)
  return rows.sort(
    (a, b) =>
      levelRank(a.level) - levelRank(b.level) ||
      b.versionNo - a.versionNo ||
      a.key.localeCompare(b.key),
  )
}

/** 作用域级别的展示键（i18n）。 */
export function scopeLevelLabelKey(level: ConfigScopeLevel | string): string {
  return level === 'instance' ? 'configFiles.scope.instance' : 'configFiles.scope.group'
}
