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

/**
 * 单实例可组合工作区状态（FR-166 / ADR「可组合卡片工作区」取代 ADR-030）。
 *
 * 承 ADR 的「状态外提 + 惰性挂载」：画布维护每实例的卡片树（哪些卡、各卡 layout、实例 id），
 * 各卡自管 dirty（在卡组件内部，store 不持久化草稿）；非激活/未挂载卡不建 WS。
 * 预设为命名布局快照，**个人级**先 `localStorage` 持久（纯函数 `lib/workspace-preset`）。
 *
 * 画布按实例 id 记忆：切实例不互相干扰；切回保留上次摆放。
 */

/** 单实例画布的运行态（卡片 + 当前预设 + 全屏卡）。 */
interface InstanceCanvas {
  /** 当前画布上的卡片（含布局）。 */
  cards: PlacedCard[]
  /** 当前应用的预设 id（内置或用户）；自由拖动后仍指向最近应用的预设。 */
  presetId: string
  /** 临时最大化的卡片 id；null = 无全屏。 */
  fullscreenCardId: string | null
}

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

/** 克隆预设的卡片并赋新 id，避免不同实例画布/多次应用共享同一卡 id。 */
function instantiateCards(preset: WorkspacePreset): PlacedCard[] {
  return preset.cards.map((c) => makeCard(c.type, { ...c.layout }))
}

interface WorkspaceState {
  /** 每实例画布运行态，按实例 id 记忆。 */
  canvasByInstance: Record<number, InstanceCanvas>
  /** 个人级用户预设（持久化 localStorage）。 */
  userPresets: WorkspacePreset[]

  /** 取某实例画布；不存在时惰性以默认「运维台」初始化（不写回，渲染时调 ensureCanvas）。 */
  ensureCanvas: (instanceId: number) => InstanceCanvas
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
  /** 删除一个用户预设（内置不可删，静默忽略）。 */
  deleteUserPreset: (presetId: string) => void
}

/** 内置预设按 id 取（每次取新实例，避免共享卡 id）。 */
function builtinById(presetId: string): WorkspacePreset | undefined {
  return builtinPresets().find((p) => p.id === presetId)
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  canvasByInstance: {},
  userPresets: loadUserPresets(),

  ensureCanvas: (instanceId) => {
    const existing = get().canvasByInstance[instanceId]
    if (existing) return existing
    const preset = defaultOpsPreset()
    const canvas: InstanceCanvas = {
      cards: instantiateCards(preset),
      presetId: 'ops',
      fullscreenCardId: null,
    }
    set((s) => ({ canvasByInstance: { ...s.canvasByInstance, [instanceId]: canvas } }))
    return canvas
  },

  applyPreset: (instanceId, presetId) => {
    const preset = builtinById(presetId) ?? get().userPresets.find((p) => p.id === presetId)
    if (!preset) return
    const canvas: InstanceCanvas = {
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
      // 落在当前最底行之下，避免与既有卡重叠。
      const bottom = canvas.cards.reduce((max, c) => Math.max(max, c.layout.y + c.layout.h), 0)
      const card = makeCard(type, { x: 0, y: bottom })
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
      const cards = layoutToCards(canvas.cards, layout)
      return {
        canvasByInstance: { ...s.canvasByInstance, [instanceId]: { ...canvas, cards } },
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
    const id = `u-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    const preset: WorkspacePreset = {
      id,
      name,
      // 存快照：复制布局，不带运行 id（应用时再实例化）。
      cards: (canvas?.cards ?? []).map((c) => ({ ...c, layout: { ...c.layout } })),
    }
    const next = [...get().userPresets, preset]
    persistUserPresets(next)
    set((s) => ({
      userPresets: next,
      canvasByInstance: s.canvasByInstance[instanceId]
        ? { ...s.canvasByInstance, [instanceId]: { ...s.canvasByInstance[instanceId], presetId: id } }
        : s.canvasByInstance,
    }))
    return id
  },

  deleteUserPreset: (presetId) => {
    const next = get().userPresets.filter((p) => p.id !== presetId)
    persistUserPresets(next)
    set({ userPresets: next })
  },
}))
