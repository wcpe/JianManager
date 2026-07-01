import { describe, it, expect } from 'vitest'
import { localDraftSource, type LocalDraftFile } from './localDraftSource'
import { PREVIEW_MAX_BYTES } from './instanceSource'

/** 造一个含指定文本内容的本地 File。 */
function textFile(name: string, content: string): File {
  return new File([new TextEncoder().encode(content)], name)
}

/** 造一个指定字节大小的本地 File（内容全 0，用于超大/二进制断言）。 */
function bytesFile(name: string, bytes: Uint8Array): File {
  return new File([bytes], name)
}

describe('localDraftSource 映射（FR-250）', () => {
  it('flat=true 且 list 一次返回全部条目（path/name/size，均为文件、取本地 File.size）', async () => {
    const files: LocalDraftFile[] = [
      { path: 'mods/foo.jar', file: textFile('foo.jar', 'x'.repeat(1024)) },
      { path: 'config/server.properties', file: textFile('server.properties', 'motd=Hi') },
    ]
    const src = localDraftSource(files)
    expect(src.flat).toBe(true)
    const entries = await src.list('')
    expect(entries).toHaveLength(2)
    expect(entries[0]).toMatchObject({ path: 'mods/foo.jar', name: 'foo.jar', isDir: false, size: 1024 })
    expect(entries[1]).toMatchObject({ path: 'config/server.properties', name: 'server.properties', isDir: false, size: 7 })
    expect(entries.every((e) => !e.isDir)).toBe(true)
  })

  it('readContent 文本文件 → 直接读本地 File 文本（零网络）', async () => {
    const src = localDraftSource([{ path: 'config/x.toml', file: textFile('x.toml', 'a=1\nb=2') }])
    const res = await src.readContent({ path: 'config/x.toml', name: 'x.toml', isDir: false })
    expect(res).toEqual({ kind: 'text', content: 'a=1\nb=2' })
  })

  it('readContent 含 NUL 字节 → 降级二进制', async () => {
    const src = localDraftSource([
      { path: 'mods/a.jar', file: bytesFile('a.jar', new Uint8Array([1, 2, 0, 3])) },
    ])
    const res = await src.readContent({ path: 'mods/a.jar', name: 'a.jar', isDir: false })
    expect(res).toEqual({ kind: 'binary' })
  })

  it('readContent 超大（> 1 MiB）→ 不读全量、降级 too-large', async () => {
    const big = bytesFile('big.bin', new Uint8Array(PREVIEW_MAX_BYTES + 1))
    const src = localDraftSource([{ path: 'big.bin', file: big }])
    const res = await src.readContent({ path: 'big.bin', name: 'big.bin', isDir: false })
    expect(res).toEqual({ kind: 'too-large', size: PREVIEW_MAX_BYTES + 1 })
  })

  it('readContent 未知 path → 错误占位（防御越界）', async () => {
    const src = localDraftSource([{ path: 'a.txt', file: textFile('a.txt', 'x') }])
    const res = await src.readContent({ path: 'does/not/exist', name: 'exist', isDir: false })
    expect(res.kind).toBe('error')
  })

  it('download 未知 path → 不触发（不抛）', () => {
    const src = localDraftSource([{ path: 'a.txt', file: textFile('a.txt', 'x') }])
    expect(() => src.download?.({ path: 'nope', name: 'nope', isDir: false })).not.toThrow()
  })
})
