import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  setBusClipboard,
  getBusClipboard,
  clearBusClipboard,
  toClipboard,
  subscribeBusClipboard,
  setDragPayload,
  getDragPayload,
  writeDragToDataTransfer,
  readDragFromDataTransfer,
  resetClipboardBusForTests,
  CLIP_MIME,
  CLIP_TTL_MS,
} from './explorer-clipboard-bus'

/** node 工程无 DOM：为 sessionStorage 镜像测补简易 polyfill。 */
function installSessionStoragePolyfill() {
  if (typeof globalThis.sessionStorage !== 'undefined') return
  const map = new Map<string, string>()
  const store = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
    removeItem: (k: string) => {
      map.delete(k)
    },
    clear: () => map.clear(),
    key: (i: number) => [...map.keys()][i] ?? null,
    get length() {
      return map.size
    },
  }
  Object.defineProperty(globalThis, 'sessionStorage', { value: store, configurable: true })
}

describe('explorer-clipboard-bus（FR-377）', () => {
  beforeEach(() => {
    installSessionStoragePolyfill()
    resetClipboardBusForTests()
  })

  it('set/get 同实例；不同实例隔离', () => {
    setBusClipboard(1, 'copy', [{ path: 'a.txt', isDir: false }], 't1')
    setBusClipboard(2, 'cut', [{ path: 'b.txt', isDir: false }], 't2')
    expect(getBusClipboard(1)?.entries[0].path).toBe('a.txt')
    expect(getBusClipboard(2)?.mode).toBe('cut')
    expect(getBusClipboard(3)).toBeNull()
  })

  it('subscribe 收到更新与 clear', () => {
    const fn = vi.fn()
    const unsub = subscribeBusClipboard(1, fn)
    setBusClipboard(1, 'cut', [{ path: 'x', isDir: true }], 's')
    expect(fn).toHaveBeenCalled()
    expect(fn.mock.calls.at(-1)?.[0]?.entries[0].path).toBe('x')
    clearBusClipboard(1)
    expect(fn.mock.calls.at(-1)?.[0]).toBeNull()
    unsub()
  })

  it('toClipboard 空条目返回 null', () => {
    expect(toClipboard(null)).toBeNull()
    const c = setBusClipboard(1, 'copy', [{ path: 'a', isDir: false }], 's')
    expect(toClipboard(c)).toEqual({ mode: 'copy', entries: [{ path: 'a', isDir: false }] })
  })

  it('sessionStorage 镜像冷启动可读', () => {
    setBusClipboard(5, 'copy', [{ path: 'p.yml', isDir: false }], 'a')
    // 清内存保留 storage
    resetClipboardBusForTests()
    // reset 清了 storage；再写一次验证 read 路径
    setBusClipboard(5, 'copy', [{ path: 'p.yml', isDir: false }], 'a')
    const raw = sessionStorage.getItem('jm.explorer.clip.5')
    expect(raw).toBeTruthy()
    // 仅删内存
    const { resetClipboardBusForTests: _r } = { resetClipboardBusForTests }
    void _r
    // 直接 get 仍命中 memory；模拟仅 storage：
    resetClipboardBusForTests()
    sessionStorage.setItem(
      'jm.explorer.clip.5',
      JSON.stringify({
        instanceId: 5,
        mode: 'copy',
        entries: [{ path: 'p.yml', isDir: false }],
        updatedAt: Date.now(),
        sourceId: 'a',
      }),
    )
    expect(getBusClipboard(5)?.entries[0].path).toBe('p.yml')
  })

  it('过期条目视为 null', () => {
    setBusClipboard(1, 'copy', [{ path: 'old', isDir: false }], 's')
    const c = getBusClipboard(1)!
    c.updatedAt = Date.now() - CLIP_TTL_MS - 1000
    sessionStorage.setItem('jm.explorer.clip.1', JSON.stringify(c))
    // 内存仍 fresh；清内存后读 storage
    resetClipboardBusForTests()
    sessionStorage.setItem(
      'jm.explorer.clip.1',
      JSON.stringify({
        instanceId: 1,
        mode: 'copy',
        entries: [{ path: 'old', isDir: false }],
        updatedAt: Date.now() - CLIP_TTL_MS - 1,
        sourceId: 's',
      }),
    )
    expect(getBusClipboard(1)).toBeNull()
  })

  it('drag payload 与 dataTransfer 读写', () => {
    const entries = [{ path: 'a.txt', isDir: false }]
    setDragPayload({ instanceId: 1, entries })
    expect(getDragPayload()?.entries).toEqual(entries)

    const store = new Map<string, string>()
    const dt = {
      setData: (k: string, v: string) => {
        store.set(k, v)
      },
      getData: (k: string) => store.get(k) ?? '',
      effectAllowed: 'none' as string,
    } as unknown as DataTransfer
    writeDragToDataTransfer(dt, 2, entries)
    expect(store.get(CLIP_MIME)).toContain('"instanceId":2')
    expect(readDragFromDataTransfer(dt, 2)).toEqual(entries)
    expect(readDragFromDataTransfer(dt, 9)).toBeNull()
  })
})
