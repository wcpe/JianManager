import { describe, it, expect } from 'vitest'
import { unzipWithNames, type ZipEntry } from './zip-filename-decode'

/**
 * BUG-G（FR-250 真机缺陷）复现 + 回归：中文 Windows 打的 zip 常把文件名存为 GBK/CP936
 * 且未置 UTF-8 通用位（bit 11）。fflate 的高层 unzip 对未置 UTF-8 的名字按 latin1 解 → 乱码。
 * unzipWithNames 自解析 local file header 拿原始名字节 + 标志位，据此 UTF-8/GBK 解码。
 * 这里构造 GBK 文件名的 zip 字节 fixture 断言解出正确中文；UTF-8 名 zip 不回归。
 */

// ── 手工拼最小 zip（STORE，无压缩），可控 filename 字节与 UTF-8 标志位 ──────────────

/** CRC32（zip 用）。 */
function crc32(bytes: Uint8Array): number {
  let c = ~0
  for (let i = 0; i < bytes.length; i++) {
    c ^= bytes[i]
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1))
  }
  return ~c >>> 0
}

interface RawEntry {
  /** 文件名原始字节（调用方控制编码）。 */
  nameBytes: Uint8Array
  /** 内容。 */
  data: Uint8Array
  /** 是否置 UTF-8 通用位（bit 11）。 */
  utf8: boolean
}

/** 把若干原始 entry 拼成一个合法的 STORE zip（local headers + central dir + EOCD）。 */
function buildZip(entries: RawEntry[]): Uint8Array {
  const chunks: number[] = []
  const central: number[] = []
  const offsets: number[] = []

  const push16 = (arr: number[], n: number) => arr.push(n & 0xff, (n >>> 8) & 0xff)
  const push32 = (arr: number[], n: number) =>
    arr.push(n & 0xff, (n >>> 8) & 0xff, (n >>> 16) & 0xff, (n >>> 24) & 0xff)
  const pushBytes = (arr: number[], b: Uint8Array) => {
    for (let i = 0; i < b.length; i++) arr.push(b[i])
  }

  for (const e of entries) {
    const off = chunks.length
    offsets.push(off)
    const crc = crc32(e.data)
    const flag = e.utf8 ? 0x0800 : 0x0000
    // Local file header: PK\3\4
    push32(chunks, 0x04034b50)
    push16(chunks, 20) // version needed
    push16(chunks, flag)
    push16(chunks, 0) // method STORE
    push16(chunks, 0) // mod time
    push16(chunks, 0) // mod date
    push32(chunks, crc)
    push32(chunks, e.data.length) // compressed size
    push32(chunks, e.data.length) // uncompressed size
    push16(chunks, e.nameBytes.length)
    push16(chunks, 0) // extra len
    pushBytes(chunks, e.nameBytes)
    pushBytes(chunks, e.data)

    // Central directory header: PK\1\2
    push32(central, 0x02014b50)
    push16(central, 20) // version made by
    push16(central, 20) // version needed
    push16(central, flag)
    push16(central, 0) // method STORE
    push16(central, 0)
    push16(central, 0)
    push32(central, crc)
    push32(central, e.data.length)
    push32(central, e.data.length)
    push16(central, e.nameBytes.length)
    push16(central, 0) // extra
    push16(central, 0) // comment
    push16(central, 0) // disk number
    push16(central, 0) // internal attrs
    push32(central, 0) // external attrs
    push32(central, off) // local header offset
    pushBytes(central, e.nameBytes)
  }

  const cdStart = chunks.length
  pushBytes(chunks, new Uint8Array(central))
  const cdSize = central.length

  // EOCD: PK\5\6
  const eocd: number[] = []
  push32(eocd, 0x06054b50)
  push16(eocd, 0) // disk
  push16(eocd, 0) // cd disk
  push16(eocd, entries.length) // entries on disk
  push16(eocd, entries.length) // total entries
  push32(eocd, cdSize)
  push32(eocd, cdStart)
  push16(eocd, 0) // comment len
  pushBytes(chunks, new Uint8Array(eocd))

  return new Uint8Array(chunks)
}

/** 用 TextEncoder 造 GBK 字节（jsdom 的 TextEncoder 只出 UTF-8，故手写常量映射测试字）。 */
function gbkBytes(...codes: number[][]): Uint8Array {
  const flat: number[] = []
  for (const c of codes) flat.push(...c)
  return new Uint8Array(flat)
}

// 常用测试汉字的 GBK 码：
//  「模」= 0xC4 0xA3   「组」= 0xD7 0xE9   「配」= 0xC5 0xE4   「置」= 0xD6 0xC3
//  「整」= 0xD5 0xFB   「合」= 0xBA 0xCF   「包」= 0xB0 0xFC
const GBK = {
  模: [0xc4, 0xa3],
  组: [0xd7, 0xe9],
  配: [0xc5, 0xe4],
  置: [0xd6, 0xc3],
  整: [0xd5, 0xfb],
  合: [0xba, 0xcf],
  包: [0xb0, 0xfc],
}
const ascii = (s: string) => Array.from(s).map((c) => c.charCodeAt(0))

describe('unzipWithNames — GBK 文件名解码（BUG-G）', () => {
  it('未置 UTF-8 位的 GBK 文件名解出正确中文（不乱码）', async () => {
    // 文件名 "模组/配置.txt" 以 GBK 编码，扩展名/斜杠为 ASCII。
    const nameBytes = gbkBytes(GBK.模, GBK.组, ascii('/'), GBK.配, GBK.置, ascii('.txt'))
    const zip = buildZip([{ nameBytes, data: new Uint8Array([1, 2, 3]), utf8: false }])
    const entries = await unzipWithNames(zip)
    const names = entries.map((e: ZipEntry) => e.name)
    expect(names).toContain('模组/配置.txt')
    // 内容仍正确解出。
    const target = entries.find((e) => e.name === '模组/配置.txt')!
    expect(Array.from(target.data)).toEqual([1, 2, 3])
  })

  it('置了 UTF-8 位的中文名按 UTF-8 解（不回归）', async () => {
    const nameBytes = new TextEncoder().encode('整合包/说明.txt')
    const zip = buildZip([{ nameBytes, data: new Uint8Array([9]), utf8: true }])
    const entries = await unzipWithNames(zip)
    expect(entries.map((e) => e.name)).toContain('整合包/说明.txt')
  })

  it('纯 ASCII 名两种标志位都正确', async () => {
    const zipA = buildZip([{ nameBytes: new Uint8Array(ascii('mods/a.jar')), data: new Uint8Array([1]), utf8: false }])
    const zipB = buildZip([{ nameBytes: new Uint8Array(ascii('mods/a.jar')), data: new Uint8Array([1]), utf8: true }])
    expect((await unzipWithNames(zipA)).map((e) => e.name)).toContain('mods/a.jar')
    expect((await unzipWithNames(zipB)).map((e) => e.name)).toContain('mods/a.jar')
  })

  it('多 entry 混合（GBK + UTF-8 + ASCII）各自正确', async () => {
    const zip = buildZip([
      { nameBytes: gbkBytes(GBK.模, GBK.组, ascii('.jar')), data: new Uint8Array([1]), utf8: false },
      { nameBytes: new TextEncoder().encode('整合.txt'), data: new Uint8Array([2]), utf8: true },
      { nameBytes: new Uint8Array(ascii('readme.md')), data: new Uint8Array([3]), utf8: false },
    ])
    const names = (await unzipWithNames(zip)).map((e) => e.name).sort()
    expect(names).toEqual(['readme.md', '整合.txt', '模组.jar'].sort())
  })
})
