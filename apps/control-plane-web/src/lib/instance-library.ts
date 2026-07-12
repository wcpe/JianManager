/**
 * 实例库拖拽的纯逻辑（FR-167 跨实例超级工作台 / 复用 ADR-034 可组合卡片工作区）。
 *
 * 实例库面板用 HTML5 原生 DnD 把「实例 / 单功能 / 多选实例」拖到画布。本模块只做
 * **拖拽载荷的序列化/解析**与**载荷 → 卡片**的纯转换（不依赖 React / DOM），便于 vitest 覆盖，
 * 承 ADR-034「状态外提 + 纯数据」决策。放置/落位的 DOM 交互在 `InstanceLibrary` 组件里。
 */

import { defaultOpsPreset, makeCard, type PlacedCard } from './workspace-preset'
import { isCardType, type CardType } from './workspace-card'

/** 工作区拖拽的自定义 MIME（避免与文本/文件拖拽混淆）。 */
export const WORKSPACE_DND_MIME = 'application/x-jm-workspace'

/**
 * 拖拽载荷三态：
 * - `instance`：拖整个实例 → 加该实例「运维台」默认卡组。
 * - `card`：拖实例下某功能 → 加该实例单卡。
 * - `instances`：多选批量拖 → 每个实例一张终端卡（一次拼监看墙）。
 */
export type DragPayload =
  | { kind: 'instance'; instanceId: number }
  | { kind: 'card'; instanceId: number; cardType: CardType }
  | { kind: 'instances'; instanceIds: number[] }

/** 有限整数判定。 */
function isFiniteNum(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

/** 序列化拖拽载荷为 dataTransfer 字符串。 */
export function encodeDragPayload(payload: DragPayload): string {
  return JSON.stringify(payload)
}

/**
 * 解析拖拽载荷字符串；结构/类型非法一律回退 null（由放置区忽略），永不抛。
 * 校验严格：kind 必须合法、instanceId 必须有限数、cardType 必须合法卡片类型。
 */
export function parseDragPayload(raw: string | null | undefined): DragPayload | null {
  if (!raw) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null
  const p = parsed as Record<string, unknown>
  switch (p.kind) {
    case 'instance':
      return isFiniteNum(p.instanceId) ? { kind: 'instance', instanceId: p.instanceId } : null
    case 'card':
      return isFiniteNum(p.instanceId) && isCardType(p.cardType)
        ? { kind: 'card', instanceId: p.instanceId, cardType: p.cardType }
        : null
    case 'instances':
      return Array.isArray(p.instanceIds) && p.instanceIds.every(isFiniteNum)
        ? { kind: 'instances', instanceIds: p.instanceIds as number[] }
        : null
    default:
      return null
  }
}

/**
 * 把拖拽载荷转成落到画布的卡片列表（均携 instanceId）。
 * `bottomY` = 当前画布最底行（新卡落其下，避免纵向重叠；水平由网格 compact 收拢）。
 *
 * - 拖单功能 → 一张该实例该类型卡。
 * - 拖实例 → 该实例「运维台」默认卡组（终端 + 状态 + 资源）。
 * - 多选批量拖 → 每个实例一张终端卡（监看墙）。
 */
export function dragPayloadToCards(payload: DragPayload, bottomY: number): PlacedCard[] {
  switch (payload.kind) {
    case 'card':
      return [makeCard(payload.cardType, { x: 0, y: bottomY }, payload.instanceId)]
    case 'instance': {
      // 复用运维台默认布局的卡型与尺寸，整体下移到 bottomY，并打上实例 id。
      return defaultOpsPreset().cards.map((c) =>
        makeCard(c.type, { x: c.layout.x, y: bottomY + c.layout.y, w: c.layout.w, h: c.layout.h }, payload.instanceId),
      )
    }
    case 'instances':
      // 监看墙：每个实例一张终端卡。横排由网格自动收拢（compactType vertical）。
      return payload.instanceIds.map((id, i) => makeCard('terminal', { x: 0, y: bottomY + i }, id))
  }
}

/**
 * 跨实例卡去重：同「实例 × 功能」只保留先到者（监看墙允许多实例同功能并存）。
 * 无 instanceId 的遗留卡（单实例预设）按「type only」与带 id 的卡互不误伤，各自独立保留。
 * 顺序稳定（保留首次出现位置）。
 */
export function dedupeCards(cards: PlacedCard[]): PlacedCard[] {
  const seen = new Set<string>()
  const out: PlacedCard[] = []
  for (const c of cards) {
    const key = c.instanceId === undefined ? `${c.type}@none@${c.id}` : `${c.type}@${c.instanceId}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(c)
  }
  return out
}
