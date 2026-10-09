/**
 * 工作区导播台的纯逻辑（FR-168 / ADR-035）。
 *
 * 导播台在多个「场景」（= FR-167 预设）间瞬切。要零延迟，目标场景的卡（终端/监控）必须
 * **已建好 WS 并保活**；但「多场景同时保活 + 全速渲染」会过载浏览器（WS 连接数受限、多 xterm
 * 重绘吃满 CPU）。故本模块把 ADR-035 的并发模型显式化为一个**状态机**：
 *
 * - 每个场景三态之一：**激活**（全速渲染 + WS 全速）/ **预热**（WS 保活但渲染降频）/
 *   **未预热**（cold，无连接）。
 * - **激活唯一**；**预热是受上限约束的集合**（含 active），按 LRU 驱逐最久未激活者。
 *
 * 本模块**不依赖 React / DOM**，只做状态转移 / LRU 驱逐 / 轮播序列的纯计算，便于 vitest 覆盖
 * （承「状态外提 + 纯数据」决策）。WS 保活与渲染节流的副作用在 React 层（`stores/director`、
 * `DirectorConsolePage`、`Terminal` 的 paused 模式）。
 */

/** 场景在导播台中的运行态。 */
export type SceneStatus = 'active' | 'preheated' | 'cold'

/**
 * 导播台状态机的不可变快照。
 *
 * `preheatOrder` 是保活连接池的 **LRU 近用序**（数组尾 = 最近激活，头 = 最久未用），
 * **包含当前 active**（active 也占一个保活连接）。三态由 `activeId` + 该集合派生。
 */
export interface DirectorState {
  /** 缩略图条上的场景 id（顺序即展示与轮播顺序）。 */
  sceneIds: string[]
  /** 当前激活场景 id；null = 无激活（全部 cold/预热）。 */
  activeId: string | null
  /** 保活连接池（含 active）的 LRU 近用序；尾部最近用。受 `limit` 约束。 */
  preheatOrder: string[]
  /** 并发上限：保活连接数（预热集合大小，含 active）的硬上限。 */
  limit: number
}

/** 预热并发上限默认值（保守：浏览器同域 WS ~6，多卡多实例易撑爆，默认仅 3 个场景保活）。 */
export const DEFAULT_PREHEAT_LIMIT = 3
/** 预热并发上限下界（至少 1，否则连 active 都无法保活）。 */
export const MIN_PREHEAT_LIMIT = 1
/** 预热并发上限上界（再高浏览器连接/CPU 必然过载，真机压测为硬验收维度）。 */
export const MAX_PREHEAT_LIMIT = 6

/** 把上限夹回 [MIN, MAX]；非有限值回退默认；向下取整。 */
export function clampLimit(n: number): number {
  if (!Number.isFinite(n)) return DEFAULT_PREHEAT_LIMIT
  return Math.min(MAX_PREHEAT_LIMIT, Math.max(MIN_PREHEAT_LIMIT, Math.floor(n)))
}

/** 构造初始导播台态：给定场景列表与上限（无激活、预热集合为空）。 */
export function createDirectorState(sceneIds: string[], limit = DEFAULT_PREHEAT_LIMIT): DirectorState {
  return {
    sceneIds: [...sceneIds],
    activeId: null,
    preheatOrder: [],
    limit: clampLimit(limit),
  }
}

/** 保活连接池（含 active）的场景 id，按 LRU 近用序（尾部最近用）。 */
export function preheatedIds(s: DirectorState): string[] {
  return [...s.preheatOrder]
}

/** 派生某场景的三态：active（唯一）/ preheated（在池中非 active）/ cold（不在池）。 */
export function sceneStatus(s: DirectorState, sceneId: string): SceneStatus {
  if (s.activeId === sceneId) return 'active'
  return s.preheatOrder.includes(sceneId) ? 'preheated' : 'cold'
}

/** 把某 id 提到 LRU 序尾（最近用）；已存在则先移除再追加，保证唯一。 */
function touch(order: string[], id: string): string[] {
  return [...order.filter((x) => x !== id), id]
}

/**
 * 在保活池上施加并发上限：从**预热**（非 active）中按 LRU 从头驱逐，直到池大小 ≤ limit。
 * active 永不被驱逐（即便它在 LRU 序里最久未"重新激活"）。
 */
function enforceLimit(order: string[], activeId: string | null, limit: number): string[] {
  if (order.length <= limit) return order
  const result = [...order]
  // 从头（最久未用）开始找可驱逐者（非 active），逐个移除。
  let i = 0
  while (result.length > limit && i < result.length) {
    if (result[i] === activeId) {
      i += 1
      continue
    }
    result.splice(i, 1)
    // 不前进 i：splice 后当前位是原下一个元素。
  }
  return result
}

/**
 * 激活一个场景（瞬切的核心）：
 * - 目标置为 active；原 active 自动降为预热（仍保活，故瞬切回去零延迟）。
 * - 目标加入/刷新保活池 LRU 近用序；超上限则 LRU 驱逐最久未用的**预热**场景。
 * - 目标不在 `sceneIds` 中 → no-op（返回原态，引用不变）。
 */
export function activateScene(s: DirectorState, sceneId: string): DirectorState {
  if (!s.sceneIds.includes(sceneId)) return s
  const order = enforceLimit(touch(s.preheatOrder, sceneId), sceneId, s.limit)
  return { ...s, activeId: sceneId, preheatOrder: order }
}

/** 调整并发上限：夹紧后若调小则即时从预热（非 active）LRU 驱逐溢出连接。 */
export function setLimit(s: DirectorState, limit: number): DirectorState {
  const next = clampLimit(limit)
  const order = enforceLimit([...s.preheatOrder], s.activeId, next)
  return { ...s, limit: next, preheatOrder: order }
}

/** 加场景到缩略图条末尾（重复 id 忽略）。 */
export function addScene(s: DirectorState, sceneId: string): DirectorState {
  if (s.sceneIds.includes(sceneId)) return s
  return { ...s, sceneIds: [...s.sceneIds, sceneId] }
}

/** 删场景：从列表与保活池移除；若删的是 active 则清空 active（其余保活不变）。 */
export function removeScene(s: DirectorState, sceneId: string): DirectorState {
  if (!s.sceneIds.includes(sceneId)) return s
  return {
    ...s,
    sceneIds: s.sceneIds.filter((x) => x !== sceneId),
    preheatOrder: s.preheatOrder.filter((x) => x !== sceneId),
    activeId: s.activeId === sceneId ? null : s.activeId,
  }
}

/**
 * 轮播序列：按 `sceneIds` 顺序环绕到 `current` 的下一个。
 * `current` 为 null / 未知时从第一个开始；空列表返回 null。
 */
export function nextSceneId(s: DirectorState, current: string | null): string | null {
  if (s.sceneIds.length === 0) return null
  const idx = current === null ? -1 : s.sceneIds.indexOf(current)
  const nextIdx = (idx + 1) % s.sceneIds.length
  return s.sceneIds[nextIdx]
}
