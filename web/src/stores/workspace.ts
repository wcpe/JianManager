import { create } from 'zustand'
import type { CardType } from '@/lib/workspace-card'
import {
  builtinPresets,
  defaultOpsPreset,
  deserializePresets,
  layoutToCards,
  makeCard,
  serializePresets,
  PRESETS_STORAGE_KEY,
  type PlacedCard,
  type WorkspacePreset,
} from '@/lib/workspace-preset'
import { dedupeCards, dragPayloadToCards, type DragPayload } from '@/lib/instance-library'

/**
 * 可组合工作区状态（FR-166 单实例 / FR-167 跨实例超级工作台 / ADR-034 取代 ADR-030）。
 *
 * 承 ADR-034 的「状态外提 + 惰性挂载」：画布维护卡片树（哪些卡、各卡 layout、实例 id），
 * 各卡自管 dirty（在卡组件内部，store 不持久化草稿）；非激活/未挂载卡不建 WS。
 * 预设为命名布局快照，**个人级**先 `localStorage` 持久（纯函数 `lib/workspace-preset`）。
 *
 * **两种作用域并存（FR-167）**：
 * - 单实例画布（`canvasByInstance[instanceId]`）：卡片限当前实例，卡省略 instanceId，按实例 id 记忆。
 * - 超级工作台（`superCanvas`）：跨实例，卡显式携带 instanceId，同画布并存多实例卡（监看墙）。
 *
 * 用户预设（`userPresets`）两作用域共享同一份持久化；超级工作台的预设携 instanceId，
 * 单实例预设省略 instanceId，**向后兼容**（见 `lib/workspace-preset`）。
 */

/** 一块画布的运行态（卡片 + 当前预设 + 全屏卡）。单实例与超级工作台共用此结构。 */
interface Canvas {
  /** 当前画布上的卡片（含布局；超级工作台的卡携 instanceId）。 */
  cards: PlacedCard[]
  /** 当前应用的预设 id（内置或用户）；自由拖动后仍指向最近应用的预设。 */
  presetId: string
  /** 临时最大化的卡片 id；null = 无全屏。 */
  fullscreenCardId: string | null
}

/** 超级工作台默认预设 id（空画布起步：实例库拖入是其主要交互）。 */
const SUPER_EMPTY_PRESET = 'super-empty'

/** 从 localStorage 安全加载用户预设。 */
function loadUserPresets(): WorkspacePreset[] {
  if (typeof localStorage === 'undefined') return []
  return deserializePresets(localStorage.getItem(PRESETS_STORAGE_KEY))
}

/** 写回用户预设到 localStorage（内置预设不入库）。 */
function persistUserPresets(presets: WorkspacePreset[]): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(PRESETS_STORAGE_KEY, serializePresets(presets))
}

/**
 * 克隆预设的卡片并赋新 id，避免不同画布/多次应用共享同一卡 id；保留 instanceId（FR-167）。
 */
function instantiateCards(preset: WorkspacePreset): PlacedCard[] {
  return preset.cards.map((c) => makeCard(c.type, { ...c.layout }, c.instanceId))
}

/** 内置预设按 id 取（每次取新实例，避免共享卡 id）。 */
function builtinById(presetId: string): WorkspacePreset | undefined {
  return builtinPresets().find((p) => p.id === presetId)
}

/** 当前画布最底行（新卡落其下，避免纵向重叠）。 */
function bottomOf(cards: PlacedCard[]): number {
  return cards.reduce((max, c) => Math.max(max, c.layout.y + c.layout.h), 0)
}

interface WorkspaceState {
  /** 每实例画布运行态，按实例 id 记忆（FR-166 单实例作用域）。 */
  canvasByInstance: Record<number, Canvas>
  /** 超级工作台跨实例画布（FR-167）；首次访问惰性以空画布初始化。 */
  superCanvas: Canvas | null
  /** 个人级用户预设（持久化 localStorage；两作用域共享）。 */
  userPresets: WorkspacePreset[]

  // —— 单实例作用域（FR-166，签名不变） ——
  /** 取某实例画布；不存在时惰性以默认「运维台」初始化（写回）。 */
  ensureCanvas: (instanceId: number) => Canvas
  /** 应用预设到某实例画布（内置或用户），卡片重新实例化为新 id。 */
  applyPreset: (instanceId: number, presetId: string) => void
  /** 添加单卡到某实例画布（落在网格底部新行）。 */
  addCard: (instanceId: number, type: CardType) => void
  /** 关闭某实例画布上的一张卡。 */
  removeCard: (instanceId: number, cardId: string) => void
  /** react-grid-layout 拖拽/缩放后写回坐标。 */
  updateLayout: (instanceId: number, layout: { i: string; x: number; y: number; w: number; h: number }[]) => void
  /** 设置/清除全屏卡。 */
  setFullscreen: (instanceId: number, cardId: string | null) => void
  /** 把某实例当前画布另存为命名用户预设；返回新预设 id。 */
  savePresetAs: (instanceId: number, name: string) => string

  // —— 超级工作台作用域（FR-167，跨实例） ——
  /** 取超级工作台画布；不存在时惰性以空画布初始化（写回）。 */
  ensureSuperCanvas: () => Canvas
  /** 应用预设到超级工作台（卡片携 instanceId 重新实例化为新 id）。 */
  applySuperPreset: (presetId: string) => void
  /** 添加单卡（指定实例）到超级工作台。 */
  addSuperCard: (instanceId: number, type: CardType) => void
  /** 实例库拖拽落到超级工作台：按载荷加实例默认卡组 / 单卡 / 多选监看墙；跨实例卡去重。 */
  dropToSuper: (payload: DragPayload) => void
  /** 关闭超级工作台上的一张卡。 */
  removeSuperCard: (cardId: string) => void
  /** 超级工作台布局写回。 */
  updateSuperLayout: (layout: { i: string; x: number; y: number; w: number; h: number }[]) => void
  /** 设置/清除超级工作台全屏卡。 */
  setSuperFullscreen: (cardId: string | null) => void
  /** 把超级工作台当前画布另存为命名用户预设（携 instanceId）；返回新预设 id。 */
  saveSuperPresetAs: (name: string) => string

  // —— 两作用域共享 ——
  /** 删除一个用户预设（内置不可删，静默忽略）。 */
  deleteUserPreset: (presetId: string) => void
}

/** 把当前画布快照存为用户预设（保留 instanceId）；写回 localStorage，返回新 id。 */
function snapshotPreset(cards: PlacedCard[], name: string): WorkspacePreset {
  return {
    id: `u-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
    name,
    // 存快照：复制布局与 instanceId，不带运行 id（应用时再实例化）。
    cards: cards.map((c) => ({ ...c, layout: { ...c.layout } })),
  }
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  canvasByInstance: {},
  superCanvas: null,
  userPresets: loadUserPresets(),

  // —— 单实例作用域 ——

  ensureCanvas: (instanceId) => {
    const existing = get().canvasByInstance[instanceId]
    if (existing) return existing
    const canvas: Canvas = {
      cards: instantiateCards(defaultOpsPreset()),
      presetId: 'ops',
      fullscreenCardId: null,
    }
    set((s) => ({ canvasByInstance: { ...s.canvasByInstance, [instanceId]: canvas } }))
    return canvas
  },

  applyPreset: (instanceId, presetId) => {
    const preset = builtinById(presetId) ?? get().userPresets.find((p) => p.id === presetId)
    if (!preset) return
    const canvas: Canvas = {
      cards: instantiateCards(preset),
      presetId,
      fullscreenCardId: null,
    }
    set((s) => ({ canvasByInstance: { ...s.canvasByInstance, [instanceId]: canvas } }))
  },

  addCard: (instanceId, type) => {
    set((s) => {
      const canvas = s.canvasByInstance[instanceId] ?? {
        cards: instantiateCards(defaultOpsPreset()),
        presetId: 'ops',
        fullscreenCardId: null,
      }
      const card = makeCard(type, { x: 0, y: bottomOf(canvas.cards) })
      return {
        canvasByInstance: {
          ...s.canvasByInstance,
          [instanceId]: { ...canvas, cards: [...canvas.cards, card] },
        },
      }
    })
  },

  removeCard: (instanceId, cardId) => {
    set((s) => {
      const canvas = s.canvasByInstance[instanceId]
      if (!canvas) return s
      return {
        canvasByInstance: {
          ...s.canvasByInstance,
          [instanceId]: {
            ...canvas,
            cards: canvas.cards.filter((c) => c.id !== cardId),
            fullscreenCardId: canvas.fullscreenCardId === cardId ? null : canvas.fullscreenCardId,
          },
        },
      }
    })
  },

  updateLayout: (instanceId, layout) => {
    set((s) => {
      const canvas = s.canvasByInstance[instanceId]
      if (!canvas) return s
      return {
        canvasByInstance: { ...s.canvasByInstance, [instanceId]: { ...canvas, cards: layoutToCards(canvas.cards, layout) } },
      }
    })
  },

  setFullscreen: (instanceId, cardId) => {
    set((s) => {
      const canvas = s.canvasByInstance[instanceId]
      if (!canvas) return s
      return {
        canvasByInstance: { ...s.canvasByInstance, [instanceId]: { ...canvas, fullscreenCardId: cardId } },
      }
    })
  },

  savePresetAs: (instanceId, name) => {
    const canvas = get().canvasByInstance[instanceId]
    const preset = snapshotPreset(canvas?.cards ?? [], name)
    const next = [...get().userPresets, preset]
    persistUserPresets(next)
    set((s) => ({
      userPresets: next,
      canvasByInstance: s.canvasByInstance[instanceId]
        ? { ...s.canvasByInstance, [instanceId]: { ...s.canvasByInstance[instanceId], presetId: preset.id } }
        : s.canvasByInstance,
    }))
    return preset.id
  },

  // —— 超级工作台作用域 ——

  ensureSuperCanvas: () => {
    const existing = get().superCanvas
    if (existing) return existing
    const canvas: Canvas = { cards: [], presetId: SUPER_EMPTY_PRESET, fullscreenCardId: null }
    set({ superCanvas: canvas })
    return canvas
  },

  applySuperPreset: (presetId) => {
    const preset = builtinById(presetId) ?? get().userPresets.find((p) => p.id === presetId)
    if (!preset) return
    set({ superCanvas: { cards: instantiateCards(preset), presetId, fullscreenCardId: null } })
  },

  addSuperCard: (instanceId, type) => {
    set((s) => {
      const canvas = s.superCanvas ?? { cards: [], presetId: SUPER_EMPTY_PRESET, fullscreenCardId: null }
      const card = makeCard(type, { x: 0, y: bottomOf(canvas.cards) }, instanceId)
      return { superCanvas: { ...canvas, cards: dedupeCards([...canvas.cards, card]) } }
    })
  },

  dropToSuper: (payload) => {
    set((s) => {
      const canvas = s.superCanvas ?? { cards: [], presetId: SUPER_EMPTY_PRESET, fullscreenCardId: null }
      const fresh = dragPayloadToCards(payload, bottomOf(canvas.cards))
      // 去重：同实例同功能不重复添加（监看墙允许多实例同功能）。
      return { superCanvas: { ...canvas, cards: dedupeCards([...canvas.cards, ...fresh]) } }
    })
  },

  removeSuperCard: (cardId) => {
    set((s) => {
      const canvas = s.superCanvas
      if (!canvas) return s
      return {
        superCanvas: {
          ...canvas,
          cards: canvas.cards.filter((c) => c.id !== cardId),
          fullscreenCardId: canvas.fullscreenCardId === cardId ? null : canvas.fullscreenCardId,
        },
      }
    })
  },

  updateSuperLayout: (layout) => {
    set((s) => {
      const canvas = s.superCanvas
      if (!canvas) return s
      return { superCanvas: { ...canvas, cards: layoutToCards(canvas.cards, layout) } }
    })
  },

  setSuperFullscreen: (cardId) => {
    set((s) => {
      const canvas = s.superCanvas
      if (!canvas) return s
      return { superCanvas: { ...canvas, fullscreenCardId: cardId } }
    })
  },

  saveSuperPresetAs: (name) => {
    const canvas = get().superCanvas
    const preset = snapshotPreset(canvas?.cards ?? [], name)
    const next = [...get().userPresets, preset]
    persistUserPresets(next)
    set((s) => ({
      userPresets: next,
      superCanvas: s.superCanvas ? { ...s.superCanvas, presetId: preset.id } : s.superCanvas,
    }))
    return preset.id
  },

  // —— 共享 ——

  deleteUserPreset: (presetId) => {
    const next = get().userPresets.filter((p) => p.id !== presetId)
    persistUserPresets(next)
    set({ userPresets: next })
  },
}))
