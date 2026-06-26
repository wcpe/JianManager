/**
 * 可组合工作区的预设与布局纯逻辑（FR-166）。
 *
 * 预设 = 命名的画布布局快照（个人级，先 `localStorage` 持久，后端同步留后续 FR）。
 * 本模块**不依赖 React / DOM**，只做卡片生成、布局规整、序列化/校验，便于 vitest 覆盖
 * （承 ADR「可组合卡片工作区」的「状态外提」决策——卡片树与持久化是纯数据）。
 */

import {
  CARD_TYPES,
  GRID_COLS,
  cardTypeDef,
  isCardType,
  type CardType,
} from './workspace-card'

/** 网格中的卡片矩形（react-grid-layout 坐标系，单位=网格格）。 */
export interface CardLayout {
  x: number
  y: number
  w: number
  h: number
}

/** 画布上的一张卡片：功能类型 + 网格位置 + 稳定 id。 */
export interface PlacedCard {
  /** 卡片唯一 id（react-grid-layout 的 `i`，跨重排稳定）。 */
  id: string
  /** 功能类型。 */
  type: CardType
  /** 网格位置与尺寸。 */
  layout: CardLayout
  /**
   * 卡片所属实例 id（FR-167 跨实例超级工作台）。
   * 单实例画布省略（全部卡同一实例，由画布上下文提供）；超级工作台每卡显式携带，
   * 故同一画布可并存不同实例的卡（监看墙）。**可选**以向后兼容 FR-166 单实例预设。
   */
  instanceId?: number
}

/** 一个命名预设：画布布局快照。 */
export interface WorkspacePreset {
  /** 预设 id（内置用固定串，用户预设用生成 id）。 */
  id: string
  /** 展示名。 */
  name: string
  /** 卡片列表。 */
  cards: PlacedCard[]
  /** 是否内置（快捷预设，不入库、不可删）。 */
  builtin?: boolean
}

/** react-grid-layout 单个布局项（含最小尺寸约束）。 */
export interface GridLayoutItem {
  i: string
  x: number
  y: number
  w: number
  h: number
  minW: number
  minH: number
}

/** localStorage 持久键（个人级用户预设；内置预设不入库）。 */
export const PRESETS_STORAGE_KEY = 'workspace.presets'

let cardSeq = 0

/**
 * 生成一张卡片：默认尺寸取自类型目录，可经 `over` 覆写位置/尺寸。
 * id 形如 `terminal-3-<rand>`，保证同会话内唯一且可读。
 * `instanceId` 可选（FR-167）：超级工作台传入卡片所属实例；单实例画布省略。
 */
export function makeCard(
  type: CardType,
  over: { x: number; y: number; w?: number; h?: number },
  instanceId?: number,
): PlacedCard {
  const def = cardTypeDef(type)!
  cardSeq += 1
  const rand = Math.random().toString(36).slice(2, 8)
  const card: PlacedCard = {
    id: `${type}-${cardSeq}-${rand}`,
    type,
    layout: {
      x: over.x,
      y: over.y,
      w: over.w ?? def.defaultSize.w,
      h: over.h ?? def.defaultSize.h,
    },
  }
  if (instanceId !== undefined) card.instanceId = instanceId
  return card
}

/**
 * 默认「运维台」布局：大终端（左主区）+ 服务器状态（右上）+ 资源（右下，文件+配置合一）。
 * 新手默认布局，等价于原固定 Tab 的「终端 + 状态 + 文件」一屏掌控（design §9）。
 */
export function defaultOpsPreset(): WorkspacePreset {
  return {
    id: 'ops',
    name: '',
    builtin: true,
    cards: [
      makeCard('terminal', { x: 0, y: 0, w: 7, h: 13 }),
      makeCard('serverstate', { x: 7, y: 0, w: 5, h: 7 }),
      makeCard('resource', { x: 7, y: 7, w: 5, h: 6 }),
    ],
  }
}

/** 仅终端（最简）。 */
function terminalPreset(): WorkspacePreset {
  return {
    id: 'terminal',
    name: '',
    builtin: true,
    cards: [makeCard('terminal', { x: 0, y: 0, w: 12, h: 13 })],
  }
}

/** 仅资源（文件+配置合一，承 FR-130）。 */
function resourcePreset(): WorkspacePreset {
  return {
    id: 'resource',
    name: '',
    builtin: true,
    cards: [makeCard('resource', { x: 0, y: 0, w: 12, h: 13 })],
  }
}

/**
 * 内置快捷预设集合。原固定 Tab（终端/文件/配置…）降级为这些一键布局。
 * 名称为空串：UI 用 i18n 键 `workspace.preset.<id>` 渲染，避免把中文写死进逻辑层。
 */
export function builtinPresets(): WorkspacePreset[] {
  return [defaultOpsPreset(), terminalPreset(), resourcePreset()]
}

/** 数值有限性校验。 */
function isFiniteNum(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

/** 规整单张卡片：夹回网格、提到最小尺寸；非法返回 null（由调用方剔除）。 */
function normalizeCard(raw: unknown): PlacedCard | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (typeof r.id !== 'string' || !isCardType(r.type)) return null
  const l = r.layout
  if (!l || typeof l !== 'object') return null
  const lr = l as Record<string, unknown>
  if (!isFiniteNum(lr.x) || !isFiniteNum(lr.y) || !isFiniteNum(lr.w) || !isFiniteNum(lr.h)) return null

  const def = cardTypeDef(r.type)!
  // 宽度：夹在 [minW, GRID_COLS]；x：夹在 [0, GRID_COLS - w]；高度：≥ minH。
  const w = Math.min(GRID_COLS, Math.max(def.minSize.w, Math.floor(lr.w)))
  const x = Math.min(Math.max(0, Math.floor(lr.x)), GRID_COLS - w)
  const h = Math.max(def.minSize.h, Math.floor(lr.h))
  const y = Math.max(0, Math.floor(lr.y))
  const card: PlacedCard = { id: r.id, type: r.type, layout: { x, y, w, h } }
  // FR-167：携带 instanceId 的卡（超级工作台）。非有限值视为无主卡（容错而非整体失败）。
  if (isFiniteNum(r.instanceId)) card.instanceId = r.instanceId
  return card
}

/**
 * 规整一个预设对象（来自 localStorage / 任意来源）。
 * 结构非法（缺 id/name/cards 数组）返回 null；卡片逐张规整，非法卡片剔除（允许空 cards）。
 */
export function normalizePreset(raw: unknown): WorkspacePreset | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (typeof r.id !== 'string' || typeof r.name !== 'string' || !Array.isArray(r.cards)) return null
  const cards = r.cards.map(normalizeCard).filter((c): c is PlacedCard => c !== null)
  return { id: r.id, name: r.name, cards, builtin: r.builtin === true }
}

/** 序列化用户预设为 localStorage 字符串（内置预设不入库，由调用方过滤后传入）。 */
export function serializePresets(presets: WorkspacePreset[]): string {
  return JSON.stringify({ presets: presets.map((p) => ({ ...p, builtin: undefined })) })
}

/** 反序列化用户预设；损坏/缺字段安全回退空数组，逐预设规整。 */
export function deserializePresets(json: string | null): WorkspacePreset[] {
  if (!json) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(json)
  } catch {
    return []
  }
  if (!parsed || typeof parsed !== 'object') return []
  const arr = (parsed as Record<string, unknown>).presets
  if (!Array.isArray(arr)) return []
  return arr.map(normalizePreset).filter((p): p is WorkspacePreset => p !== null)
}

/** 卡片列表 → react-grid-layout 布局项（注入每类型的最小尺寸约束）。 */
export function cardsToLayout(cards: PlacedCard[]): GridLayoutItem[] {
  return cards.map((c) => {
    const def = cardTypeDef(c.type)!
    return {
      i: c.id,
      x: c.layout.x,
      y: c.layout.y,
      w: c.layout.w,
      h: c.layout.h,
      minW: def.minSize.w,
      minH: def.minSize.h,
    }
  })
}

/**
 * react-grid-layout 的 onLayoutChange 回调 → 写回卡片坐标（保留 type/id）。
 * 回调里出现的未知 `i`（理论上不应发生）被忽略，卡片顺序与入参一致。
 */
export function layoutToCards(
  cards: PlacedCard[],
  layout: { i: string; x: number; y: number; w: number; h: number }[],
): PlacedCard[] {
  const byId = new Map(layout.map((l) => [l.i, l]))
  return cards.map((c) => {
    const l = byId.get(c.id)
    if (!l) return c
    return { ...c, layout: { x: l.x, y: l.y, w: l.w, h: l.h } }
  })
}

/** 全部卡片类型（供 UI「添加卡片」菜单遍历）。 */
export const ALL_CARD_TYPES = CARD_TYPES
