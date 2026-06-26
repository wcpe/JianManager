import { create } from 'zustand'
import { makeCard, type PlacedCard, type WorkspacePreset } from '@/lib/workspace-preset'
import {
  activateScene as machineActivate,
  addScene as machineAdd,
  clampLimit,
  createDirectorState,
  nextSceneId as machineNext,
  removeScene as machineRemove,
  setLimit as machineSetLimit,
  type DirectorState,
} from '@/lib/director'

/**
 * 工作区导播台状态（FR-168 / ADR-035）。
 *
 * 导播台在多个**场景**间瞬切（OBS 式）。一个场景 = 一份命名的画布快照（卡片携 instanceId，
 * 复用 FR-167 跨实例预设结构），用户从既有用户预设导入为场景。导播台维护：
 * - 场景定义（`scenes`，持久 localStorage）。
 * - 预热并发状态机（`machine`，纯逻辑见 `lib/director`）：激活唯一 + 受上限的预热集合 + LRU 驱逐。
 * - 轮播配置（开关 + 间隔）。
 *
 * **保活与渲染节流的副作用在 React 层**（`DirectorConsolePage` 把预热场景的画布同时挂载、
 * 非激活场景用 `DirectorRenderProvider active=false` + content-visibility 暂停重绘）。本 store 只管状态。
 */

/** 一个导播台场景：命名的画布快照 + 稳定 id（其 cards 携 instanceId，跨实例）。 */
export interface DirectorScene {
  /** 场景 id（生成串，与来源预设 id 解耦，允许同一预设多份/重命名）。 */
  id: string
  /** 展示名。 */
  name: string
  /** 画布卡片快照（含布局与 instanceId）。 */
  cards: PlacedCard[]
}

/** localStorage 持久键：场景定义 / 并发上限 / 轮播间隔。 */
const SCENES_KEY = 'director.scenes'
const LIMIT_KEY = 'director.limit'
const CAROUSEL_KEY = 'director.carouselMs'

/** 轮播间隔默认值（毫秒）。 */
export const DEFAULT_CAROUSEL_MS = 10_000
/** 轮播间隔下界（防止过快切换把保活池搅乱 + 过载）。 */
export const MIN_CAROUSEL_MS = 3_000

/** 安全读取场景定义。 */
function loadScenes(): DirectorScene[] {
  if (typeof localStorage === 'undefined') return []
  const raw = localStorage.getItem(SCENES_KEY)
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter(isValidScene)
  } catch {
    return []
  }
}

/** 场景结构校验（容错：损坏项剔除）。 */
function isValidScene(v: unknown): v is DirectorScene {
  if (!v || typeof v !== 'object') return false
  const s = v as Record<string, unknown>
  return typeof s.id === 'string' && typeof s.name === 'string' && Array.isArray(s.cards)
}

function persistScenes(scenes: DirectorScene[]): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(SCENES_KEY, JSON.stringify(scenes))
}

/** 安全读取数值持久值并夹紧。 */
function loadNum(key: string, fallback: number, clamp: (n: number) => number): number {
  if (typeof localStorage === 'undefined') return fallback
  const raw = localStorage.getItem(key)
  if (!raw) return fallback
  const n = Number(raw)
  return Number.isFinite(n) ? clamp(n) : fallback
}

/** 夹紧轮播间隔（≥ 下界；非有限回退默认）。 */
function clampCarouselMs(n: number): number {
  if (!Number.isFinite(n)) return DEFAULT_CAROUSEL_MS
  return Math.max(MIN_CAROUSEL_MS, Math.floor(n))
}

/** 克隆预设的卡片为场景卡片，赋新 id，保留 instanceId（避免与画布共享卡 id）。 */
function cloneCards(cards: PlacedCard[]): PlacedCard[] {
  return cards.map((c) => makeCard(c.type, { ...c.layout }, c.instanceId))
}

interface DirectorStoreState {
  /** 场景定义（持久）。 */
  scenes: DirectorScene[]
  /** 预热并发状态机（激活/预热/cold + LRU + 上限）。 */
  machine: DirectorState
  /** 轮播开关。 */
  carouselOn: boolean
  /** 轮播间隔（毫秒，持久）。 */
  carouselMs: number

  /** 从一个预设导入为新场景（克隆其卡片），追加到末尾；返回新场景 id。 */
  addSceneFromPreset: (preset: WorkspacePreset) => string
  /** 删除一个场景（同步从状态机移除）。 */
  removeScene: (sceneId: string) => void
  /** 重命名场景。 */
  renameScene: (sceneId: string, name: string) => void
  /** 激活（瞬切）到某场景：状态机置 active + LRU 保活，原 active 降预热。 */
  activate: (sceneId: string) => void
  /** 轮播到下一个场景（按 sceneIds 顺序环绕）。 */
  advance: () => void
  /** 设并发上限（夹紧；调小即时 LRU 驱逐溢出）。 */
  setLimit: (limit: number) => void
  /** 开关轮播。 */
  setCarouselOn: (on: boolean) => void
  /** 设轮播间隔（夹紧，持久）。 */
  setCarouselMs: (ms: number) => void
}

const initialScenes = loadScenes()

export const useDirectorStore = create<DirectorStoreState>((set, get) => ({
  scenes: initialScenes,
  machine: createDirectorState(
    initialScenes.map((s) => s.id),
    loadNum(LIMIT_KEY, clampLimit(NaN), clampLimit),
  ),
  carouselOn: false,
  carouselMs: loadNum(CAROUSEL_KEY, DEFAULT_CAROUSEL_MS, clampCarouselMs),

  addSceneFromPreset: (preset) => {
    const scene: DirectorScene = {
      id: `s-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
      name: preset.name,
      cards: cloneCards(preset.cards),
    }
    set((st) => {
      const scenes = [...st.scenes, scene]
      persistScenes(scenes)
      return { scenes, machine: machineAdd(st.machine, scene.id) }
    })
    return scene.id
  },

  removeScene: (sceneId) => {
    set((st) => {
      const scenes = st.scenes.filter((s) => s.id !== sceneId)
      persistScenes(scenes)
      return { scenes, machine: machineRemove(st.machine, sceneId) }
    })
  },

  renameScene: (sceneId, name) => {
    set((st) => {
      const scenes = st.scenes.map((s) => (s.id === sceneId ? { ...s, name } : s))
      persistScenes(scenes)
      return { scenes }
    })
  },

  activate: (sceneId) => {
    set((st) => ({ machine: machineActivate(st.machine, sceneId) }))
  },

  advance: () => {
    const { machine } = get()
    const next = machineNext(machine, machine.activeId)
    if (next) set({ machine: machineActivate(machine, next) })
  },

  setLimit: (limit) => {
    const next = clampLimit(limit)
    if (typeof localStorage !== 'undefined') localStorage.setItem(LIMIT_KEY, String(next))
    set((st) => ({ machine: machineSetLimit(st.machine, next) }))
  },

  setCarouselOn: (on) => set({ carouselOn: on }),

  setCarouselMs: (ms) => {
    const next = clampCarouselMs(ms)
    if (typeof localStorage !== 'undefined') localStorage.setItem(CAROUSEL_KEY, String(next))
    set({ carouselMs: next })
  },
}))
