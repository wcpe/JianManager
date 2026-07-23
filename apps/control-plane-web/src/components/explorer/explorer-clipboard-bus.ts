/**
 * 资源管理器跨窗剪贴板 / 拖放总线（FR-377，ADR-078）。
 * 实例级共享；同页 EventTarget 订阅 + BroadcastChannel 跨标签；sessionStorage 冷启动镜像。
 */

import type { Clipboard, ClipboardEntry, ClipboardMode } from './clipboard'

export const CLIP_MIME = 'application/x-jm-explorer-entries'
export const CLIP_CHANNEL = 'jm-explorer-clip-v1'
export const CLIP_TTL_MS = 30 * 60 * 1000

export interface BusClipboard extends Clipboard {
  instanceId: number
  updatedAt: number
  sourceId: string
}

export interface DragPayload {
  instanceId: number
  entries: ClipboardEntry[]
}

type Listener = (clip: BusClipboard | null) => void

const memory = new Map<number, BusClipboard>()
const listeners = new Map<number, Set<Listener>>()
/** 同页 DnD 备用载荷（跨 Explorer 实例）。 */
let dragPayload: DragPayload | null = null

function storageKey(instanceId: number): string {
  return `jm.explorer.clip.${instanceId}`
}

function isFresh(c: BusClipboard): boolean {
  return Date.now() - c.updatedAt < CLIP_TTL_MS
}

function readStorage(instanceId: number): BusClipboard | null {
  try {
    const raw = sessionStorage.getItem(storageKey(instanceId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as BusClipboard
    if (parsed?.instanceId !== instanceId || !parsed.mode || !Array.isArray(parsed.entries)) return null
    if (!isFresh(parsed)) {
      sessionStorage.removeItem(storageKey(instanceId))
      return null
    }
    return parsed
  } catch {
    return null
  }
}

function writeStorage(instanceId: number, clip: BusClipboard | null): void {
  try {
    if (!clip) sessionStorage.removeItem(storageKey(instanceId))
    else sessionStorage.setItem(storageKey(instanceId), JSON.stringify(clip))
  } catch {
    /* private mode 等忽略 */
  }
}

let bc: BroadcastChannel | null = null
let bcBound = false

function ensureChannel(): BroadcastChannel | null {
  if (typeof BroadcastChannel === 'undefined') return null
  if (!bc) {
    try {
      bc = new BroadcastChannel(CLIP_CHANNEL)
    } catch {
      return null
    }
  }
  if (!bcBound && bc) {
    bcBound = true
    bc.onmessage = (ev: MessageEvent) => {
      const data = ev.data as { type?: string; clip?: BusClipboard | null }
      if (!data || data.type !== 'clip') return
      const clip = data.clip
      if (clip == null) {
        // 无 instanceId 时无法定向；要求 payload 带 instanceId
        return
      }
      const id = clip.instanceId
      if (!isFresh(clip)) {
        memory.delete(id)
        writeStorage(id, null)
        emit(id, null)
        return
      }
      memory.set(id, clip)
      writeStorage(id, clip)
      emit(id, clip)
    }
  }
  return bc
}

/** 清除指定实例的剪贴板（含 BC 广播）。 */
export function clearBusClipboard(instanceId: number, sourceId = 'local'): void {
  memory.delete(instanceId)
  writeStorage(instanceId, null)
  emit(instanceId, null)
  const ch = ensureChannel()
  try {
    ch?.postMessage({
      type: 'clip',
      clip: {
        instanceId,
        mode: 'cut' as ClipboardMode,
        entries: [],
        updatedAt: 0,
        sourceId,
        _clear: true,
      },
    })
  } catch {
    /* ignore */
  }
  // 显式 clear 消息：用 type clear 更清晰
  try {
    ch?.postMessage({ type: 'clear', instanceId })
  } catch {
    /* ignore */
  }
}

// 扩展 onmessage 处理 clear
function emit(instanceId: number, clip: BusClipboard | null): void {
  const set = listeners.get(instanceId)
  if (!set) return
  for (const fn of set) {
    try {
      fn(clip)
    } catch {
      /* listener 错误不传播 */
    }
  }
}

// 重新绑定以支持 clear
function rebindChannel(): void {
  const ch = ensureChannel()
  if (!ch) return
  ch.onmessage = (ev: MessageEvent) => {
    const data = ev.data as {
      type?: string
      instanceId?: number
      clip?: BusClipboard | null
    }
    if (!data) return
    if (data.type === 'clear' && typeof data.instanceId === 'number') {
      memory.delete(data.instanceId)
      writeStorage(data.instanceId, null)
      emit(data.instanceId, null)
      return
    }
    if (data.type === 'clip' && data.clip) {
      const clip = data.clip
      // 空 entries + updatedAt 0 视为 clear
      if (clip.updatedAt === 0 || (clip.entries?.length === 0 && (clip as { _clear?: boolean })._clear)) {
        memory.delete(clip.instanceId)
        writeStorage(clip.instanceId, null)
        emit(clip.instanceId, null)
        return
      }
      if (!isFresh(clip)) return
      memory.set(clip.instanceId, clip)
      writeStorage(clip.instanceId, clip)
      emit(clip.instanceId, clip)
    }
  }
}
rebindChannel()

/** 写入剪贴板并跨标签广播。 */
export function setBusClipboard(
  instanceId: number,
  mode: ClipboardMode,
  entries: ClipboardEntry[],
  sourceId: string,
): BusClipboard {
  const clip: BusClipboard = {
    instanceId,
    mode,
    entries,
    updatedAt: Date.now(),
    sourceId,
  }
  memory.set(instanceId, clip)
  writeStorage(instanceId, clip)
  emit(instanceId, clip)
  try {
    ensureChannel()?.postMessage({ type: 'clip', clip })
  } catch {
    /* ignore */
  }
  return clip
}

/** 读取当前实例剪贴板（内存 → sessionStorage）。 */
export function getBusClipboard(instanceId: number): BusClipboard | null {
  const mem = memory.get(instanceId)
  if (mem) {
    if (!isFresh(mem)) {
      memory.delete(instanceId)
      writeStorage(instanceId, null)
      return null
    }
    // 空 entries 不算有效剪贴板
    if (mem.entries.length === 0) return null
    return mem
  }
  const stored = readStorage(instanceId)
  if (stored && stored.entries.length > 0) {
    memory.set(instanceId, stored)
    return stored
  }
  return null
}

/** 转为 FR-070 Clipboard 形状（无元数据）。 */
export function toClipboard(bus: BusClipboard | null): Clipboard | null {
  if (!bus || bus.entries.length === 0) return null
  return { mode: bus.mode, entries: bus.entries }
}

export function subscribeBusClipboard(instanceId: number, listener: Listener): () => void {
  // 确保 BC 已监听
  rebindChannel()
  let set = listeners.get(instanceId)
  if (!set) {
    set = new Set()
    listeners.set(instanceId, set)
  }
  set.add(listener)
  return () => {
    set!.delete(listener)
    if (set!.size === 0) listeners.delete(instanceId)
  }
}

export function setDragPayload(payload: DragPayload | null): void {
  dragPayload = payload
}

export function getDragPayload(): DragPayload | null {
  return dragPayload
}

/** 序列化到 dataTransfer。 */
export function writeDragToDataTransfer(
  dt: DataTransfer,
  instanceId: number,
  entries: ClipboardEntry[],
): void {
  const payload: DragPayload = { instanceId, entries }
  setDragPayload(payload)
  try {
    dt.setData(CLIP_MIME, JSON.stringify(payload))
    dt.effectAllowed = 'move'
  } catch {
    /* 某些环境 setData 受限 */
  }
}

/** 从 drop 事件解析拖放条目。 */
export function readDragFromDataTransfer(
  dt: DataTransfer | null,
  expectedInstanceId: number,
): ClipboardEntry[] | null {
  if (dt) {
    try {
      const raw = dt.getData(CLIP_MIME)
      if (raw) {
        const parsed = JSON.parse(raw) as DragPayload
        if (parsed.instanceId === expectedInstanceId && Array.isArray(parsed.entries)) {
          return parsed.entries
        }
        return null
      }
    } catch {
      /* fall through */
    }
  }
  const mem = getDragPayload()
  if (mem && mem.instanceId === expectedInstanceId) return mem.entries
  return null
}

/** 测试辅助：清空总线状态。 */
export function resetClipboardBusForTests(): void {
  memory.clear()
  listeners.clear()
  dragPayload = null
  try {
    const keys: string[] = []
    for (let i = 0; i < sessionStorage.length; i++) {
      const k = sessionStorage.key(i)
      if (k?.startsWith('jm.explorer.clip.')) keys.push(k)
    }
    keys.forEach((k) => sessionStorage.removeItem(k))
  } catch {
    /* ignore */
  }
}
