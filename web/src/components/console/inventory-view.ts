/**
 * 背包定制页纯逻辑（FR-127，见 JM ADR-026 + ServerProbe ADR-0016）。抽成独立模块便于单测，
 * 且不污染组件文件的 fast-refresh（react-refresh/only-export-components）。与 economy-view.ts 同范式。
 *
 * 数据源：探针背包 Provider 的 `inventory.view` 动作输出（经 dispatchBusiness 透传，CP 不解析）。
 * 编码契约见探针侧 InventoryEnvelope.encodeView：物品以数组承载、每件含 slot + 全保真 nbtBase64 + UI 便利字段。
 * 本模块只读、不改背包；写动作（发/收物品）走 FR-121 dispatchBusiness 写路径，不在此处。
 */

/** 一格物品（来自探针 encodeItem；写以 nbtBase64 为准，其余为渲染便利字段）。 */
export interface RawItemSlot {
  /** 槽位下标（背包 0..35、末影箱 0..26）。 */
  slot: number
  /** 物品材质（如 DIAMOND_SWORD）。 */
  material: string
  /** 数量。 */
  amount: number
  /** 全保真往返真源（Bukkit 序列化 base64）；读视图可空。 */
  nbtBase64: string
  /** 自定义名（可空）。 */
  displayName?: string
  /** lore 行（可空）。 */
  lore?: string[]
  /** 附魔 id→等级（可空）。 */
  enchantments?: Record<string, number>
}

/** 玩家基础属性（探针 encodeBasicAttrs）。 */
export interface BasicAttrs {
  health: number
  foodLevel: number
  xpLevel: number
  xpProgress: number
  xpTotal: number
  gameMode: string
}

/** 解析后的背包视图（供格子视图渲染）。 */
export interface InventoryView {
  /** 玩家是否有落盘数据；false 与「空背包」严格区分（不谎报）。 */
  exists: boolean
  /** 玩家 UUID。 */
  player: string
  /** 是否在线（写他服在线玩家会被拒，UI 据此提示）；缺省 false。 */
  online: boolean
  /** 数据版本（乐观锁；0 表示未携带）。 */
  dataVersion: number
  /** 背包物品（保序，已剔除坏项）。 */
  inventory: RawItemSlot[]
  /** 末影箱物品。 */
  enderChest: RawItemSlot[]
  /** 基础属性；缺失为 null。 */
  basicAttrs: BasicAttrs | null
}

/** 玩家背包常规槽位数（9 快捷栏 + 27 主背包）。 */
export const INVENTORY_SLOTS = 36

/** 末影箱常规槽位数（3 行 × 9）。 */
export const ENDER_CHEST_SLOTS = 27

/** 格子视图每行格数（MC 容器一行 9 格）。 */
export const SLOTS_PER_ROW = 9

/**
 * 解析探针 `inventory.view` 输出为结构化背包视图；非对象 / 缺 exists 返回 null（坏数据降级，不渲染坏页）。
 * exists=false 时仅 player 有效，物品列表为空（区分「玩家无数据」与「空背包」）。
 */
export function parseInventoryView(output: unknown): InventoryView | null {
  if (typeof output !== 'object' || output === null) return null
  const o = output as Record<string, unknown>
  if (typeof o.exists !== 'boolean') return null
  const player = typeof o.player === 'string' ? o.player : ''
  if (!o.exists) {
    return { exists: false, player, online: false, dataVersion: 0, inventory: [], enderChest: [], basicAttrs: null }
  }
  return {
    exists: true,
    player,
    online: o.online === true,
    dataVersion: typeof o.dataVersion === 'number' ? o.dataVersion : 0,
    inventory: parseItems(o.inventory),
    enderChest: parseItems(o.enderChest),
    basicAttrs: parseBasicAttrs(o.basicAttrs),
  }
}

/** 解析物品数组，丢弃坏项（非对象 / slot 非数）。 */
function parseItems(raw: unknown): RawItemSlot[] {
  if (!Array.isArray(raw)) return []
  const out: RawItemSlot[] = []
  for (const it of raw) {
    if (typeof it !== 'object' || it === null) continue
    const r = it as Record<string, unknown>
    if (typeof r.slot !== 'number') continue
    out.push({
      slot: r.slot,
      material: typeof r.material === 'string' ? r.material : '',
      amount: typeof r.amount === 'number' && r.amount > 0 ? r.amount : 1,
      nbtBase64: typeof r.nbtBase64 === 'string' ? r.nbtBase64 : '',
      displayName: typeof r.displayName === 'string' ? r.displayName : undefined,
      lore: Array.isArray(r.lore) ? (r.lore.filter((l) => typeof l === 'string') as string[]) : undefined,
      enchantments: isStringNumberMap(r.enchantments) ? (r.enchantments as Record<string, number>) : undefined,
    })
  }
  return out
}

/** 解析基础属性；缺关键字段返回 null。 */
function parseBasicAttrs(raw: unknown): BasicAttrs | null {
  if (typeof raw !== 'object' || raw === null) return null
  const r = raw as Record<string, unknown>
  return {
    health: num(r.health),
    foodLevel: num(r.foodLevel),
    xpLevel: num(r.xpLevel),
    xpProgress: num(r.xpProgress),
    xpTotal: num(r.xpTotal),
    gameMode: typeof r.gameMode === 'string' ? r.gameMode : '',
  }
}

/** 数值容错：非有限数归 0。 */
function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

/** 判定是否为 string→number 映射（附魔表）。 */
function isStringNumberMap(v: unknown): boolean {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false
  return Object.values(v as Record<string, unknown>).every((x) => typeof x === 'number')
}

/**
 * 把槽位物品铺成定长格子（空槽为 null），供格子视图按行渲染。
 * 默认容量 `size`（背包 36 / 末影箱 27）；遇越界槽位按行对齐扩容到容纳该槽（凑 [SLOTS_PER_ROW] 整数倍，
 * 避免末行残缺）。同槽位重复以最后一个为准（防探针重发叠加）。
 */
export function buildSlotGrid(items: RawItemSlot[], size: number): (RawItemSlot | null)[] {
  let maxSlot = size - 1
  for (const it of items) if (it.slot > maxSlot) maxSlot = it.slot
  // 凑整到整行，末行不残缺。
  const rows = Math.ceil((maxSlot + 1) / SLOTS_PER_ROW)
  const total = Math.max(size, rows * SLOTS_PER_ROW)
  const grid: (RawItemSlot | null)[] = new Array(total).fill(null)
  for (const it of items) {
    if (it.slot >= 0 && it.slot < total) grid[it.slot] = it
  }
  return grid
}
