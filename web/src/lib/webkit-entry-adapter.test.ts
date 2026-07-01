import { describe, it, expect } from 'vitest'
import { adaptEntry, type NativeFileSystemEntry } from './webkit-entry-adapter'
import { collectEntries } from './client-publish-wizard'

/**
 * BUG-F（FR-250 真机缺陷）复现 + 回归：拖拽走浏览器**原生**回调式 `FileSystemEntry`，
 * 而 collectEntries 期望 Promise 化的 FileSystemEntryLike。这里以原生形态（回调式
 * `file(cb)` / `createReader().readEntries(cb)`）构造 entry，经 adaptEntry 适配后喂
 * collectEntries，断言散文件拿到 File、目录递归全收（含跨批 >100 项）。
 * 未适配时（bug）：原生 file 无回调返回 undefined、目录 entry 无 readEntries 被跳过 → 全丢。
 */

/** 造原生文件 entry：`file(successCb, errorCb)` 回调式（非 Promise）。 */
function nativeFile(fullPath: string, size = 10): NativeFileSystemEntry {
  const name = fullPath.split('/').pop() ?? fullPath
  return {
    isFile: true,
    isDirectory: false,
    fullPath,
    name,
    file(onSuccess: (f: File) => void) {
      onSuccess(new File([new Uint8Array(size)], name))
    },
  }
}

/**
 * 造原生目录 entry：`createReader()` 返回的 reader 以**分批**回调式 `readEntries(cb)` 吐子项，
 * 一次一批、读空为止（模拟原生一次只返回 ~100 项的行为）。
 */
function nativeDir(fullPath: string, batches: NativeFileSystemEntry[][]): NativeFileSystemEntry {
  return {
    isFile: false,
    isDirectory: true,
    fullPath,
    name: fullPath.split('/').pop() ?? fullPath,
    createReader() {
      let i = 0
      return {
        readEntries(onSuccess: (entries: NativeFileSystemEntry[]) => void) {
          // 原生语义：返回当前批；无更多时返回空数组表示读完。
          const batch = i < batches.length ? batches[i] : []
          i += 1
          onSuccess(batch)
        },
      }
    },
  }
}

describe('adaptEntry（原生 → FileSystemEntryLike）', () => {
  it('散文件 entry：回调式 file(cb) → Promise 化后 collectEntries 拿到 File', async () => {
    const adapted = adaptEntry(nativeFile('/a.jar', 5))
    const units = await collectEntries([adapted])
    expect(units).toHaveLength(1)
    expect(units[0].path).toBe('a.jar')
    expect(units[0].file.size).toBe(5)
    expect(units[0].file.name).toBe('a.jar')
  })

  it('目录 entry：createReader().readEntries(cb) 分批 → 递归全收、保相对结构', async () => {
    // /pack 下：mods 目录（内含 a.jar 与 sub/b.jar）、根级 options.txt。
    const dir = nativeDir('/pack', [
      [
        nativeDir('/pack/mods', [
          [nativeFile('/pack/mods/a.jar'), nativeDir('/pack/mods/sub', [[nativeFile('/pack/mods/sub/b.jar')]])],
        ]),
        nativeFile('/pack/options.txt'),
      ],
    ])
    const units = await collectEntries([adaptEntry(dir)])
    expect(units.map((u) => u.path)).toEqual([
      'pack/mods/a.jar',
      'pack/mods/sub/b.jar',
      'pack/options.txt',
    ])
  })

  it('跨批读取：目录子项超过单批上限（>100）时循环读到空、一个不漏', async () => {
    // 造 250 个文件分三批（100 + 100 + 50）——若只读第一批（不循环）会漏 150 个。
    const mk = (n: number, base: number) =>
      Array.from({ length: n }, (_, i) => nativeFile(`/big/f${base + i}.jar`))
    const dir = nativeDir('/big', [mk(100, 0), mk(100, 100), mk(50, 200)])
    const units = await collectEntries([adaptEntry(dir)])
    expect(units).toHaveLength(250)
    // 抽查首、跨批边界、尾项都在。
    const paths = new Set(units.map((u) => u.path))
    expect(paths.has('big/f0.jar')).toBe(true)
    expect(paths.has('big/f99.jar')).toBe(true)
    expect(paths.has('big/f100.jar')).toBe(true)
    expect(paths.has('big/f249.jar')).toBe(true)
  })

  it('散文件 + 文件夹混合森林：均正确适配', async () => {
    const units = await collectEntries([
      adaptEntry(nativeFile('/loose.txt')),
      adaptEntry(nativeDir('/d', [[nativeFile('/d/x.jar')]])),
    ])
    expect(units.map((u) => u.path)).toEqual(['loose.txt', 'd/x.jar'])
  })

  it('file()/readEntries 的回调错误被 reject（可被上层捕获提示）', async () => {
    const failing: NativeFileSystemEntry = {
      isFile: true,
      isDirectory: false,
      fullPath: '/bad.jar',
      name: 'bad.jar',
      file(_onSuccess: (f: File) => void, onError?: (e: unknown) => void) {
        onError?.(new Error('read fail'))
      },
    }
    const adapted = adaptEntry(failing)
    await expect(adapted.file!()).rejects.toThrow('read fail')
  })
})
