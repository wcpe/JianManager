/**
 * 自解析 zip 以正确解码文件名的解包器（FR-250 / BUG-G）。
 *
 * 中文 Windows 打的 zip 常把文件名存为 GBK/CP936，且**未置** zip 通用位标记 bit 11（UTF-8 标志）。
 * fflate 的高层 `unzip` 对未置 UTF-8 的名字按 latin1 解 → 中文乱码；且它不暴露原始名字节与标志位。
 * 这里自行遍历 zip 的 **local file header**（签名 `PK\3\4`）取原始名字节 + 标志位：
 * 置 UTF-8 位 → `TextDecoder('utf-8')`，否则 → `TextDecoder('gbk')`（浏览器原生支持 GBK）。
 * 内容按压缩方法解出（0=STORE 原样切片、8=DEFLATE 走 fflate `inflateSync`），其余方法抛错。
 *
 * 仅覆盖 MC 整合包实际用到的 STORE/DEFLATE；不处理 zip64、加密、跨盘分卷（本场景不需要）。
 */
import { inflateSync } from 'fflate'

/** 解包出的单个 entry：正确解码的相对名 + 解压后内容（FR-250）。 */
export interface ZipEntry {
  /** 文件名（按 UTF-8 标志位选 UTF-8/GBK 解码后的正确名，含目录以 `/` 分隔）。 */
  name: string
  /** 解压后原始字节。 */
  data: Uint8Array
}

const LOCAL_FILE_HEADER_SIG = 0x04034b50
const CENTRAL_DIR_SIG = 0x02014b50
/** zip 通用位标记 bit 11：置位表示文件名/注释为 UTF-8。 */
const UTF8_FLAG = 0x0800
/** 通用位 bit 3：数据描述符（size/crc 在数据之后），此时 local header 里的 size 为 0。 */
const DATA_DESCRIPTOR_FLAG = 0x0008

/** 小端读 16 位。 */
function u16(d: Uint8Array, o: number): number {
  return d[o] | (d[o + 1] << 8)
}
/** 小端读 32 位（无符号）。 */
function u32(d: Uint8Array, o: number): number {
  return (d[o] | (d[o + 1] << 8) | (d[o + 2] << 16) | (d[o + 3] << 24)) >>> 0
}

const utf8Decoder = new TextDecoder('utf-8')
// GBK 解码器：浏览器原生支持；jsdom(测试)亦支持 gbk 标签。构造失败时回退 latin1 兜底（至少不抛）。
const gbkDecoder: TextDecoder = (() => {
  try {
    return new TextDecoder('gbk')
  } catch {
    return new TextDecoder('latin1')
  }
})()

/** 按 UTF-8 标志位选择解码器解出文件名。 */
function decodeName(nameBytes: Uint8Array, utf8: boolean): string {
  return (utf8 ? utf8Decoder : gbkDecoder).decode(nameBytes)
}

/**
 * 用 central directory 建「local header 偏移 → (原始名字节, UTF-8 标志)」表（FR-250）。
 *
 * local file header 在设置数据描述符位时其压缩尺寸字段为 0，单靠 local header 无法定位内容长度；
 * central directory 记录了每个 entry 的压缩尺寸与 local header 偏移，据此可稳妥切分。
 * 名字仍以 central header 的原始字节 + 标志位解码（与 local 一致）。
 * 返回 null 表示未找到 EOCD（非法/非 zip）。
 */
interface CentralEntry {
  nameBytes: Uint8Array
  utf8: boolean
  method: number
  compressedSize: number
  localOffset: number
}

function readCentralDirectory(d: Uint8Array): CentralEntry[] | null {
  // 从尾部回扫 EOCD 签名 PK\5\6（0x06054b50），comment 最长 65535。
  const minEocd = 22
  const maxBack = Math.min(d.length, minEocd + 0xffff)
  let eocd = -1
  for (let i = d.length - minEocd; i >= d.length - maxBack; i--) {
    if (i < 0) break
    if (u32(d, i) === 0x06054b50) {
      eocd = i
      break
    }
  }
  if (eocd < 0) return null
  const total = u16(d, eocd + 10)
  let ptr = u32(d, eocd + 16) // central directory 起始偏移
  const entries: CentralEntry[] = []
  for (let n = 0; n < total; n++) {
    if (ptr + 46 > d.length || u32(d, ptr) !== CENTRAL_DIR_SIG) break
    const flag = u16(d, ptr + 8)
    const method = u16(d, ptr + 10)
    const compressedSize = u32(d, ptr + 20)
    const nameLen = u16(d, ptr + 28)
    const extraLen = u16(d, ptr + 30)
    const commentLen = u16(d, ptr + 32)
    const localOffset = u32(d, ptr + 42)
    const nameBytes = d.subarray(ptr + 46, ptr + 46 + nameLen)
    entries.push({
      nameBytes,
      utf8: (flag & UTF8_FLAG) !== 0,
      method,
      compressedSize,
      localOffset,
    })
    ptr += 46 + nameLen + extraLen + commentLen
  }
  return entries
}

/** 从 local file header 偏移读出压缩数据的**起始位置**（跳过可变长 name/extra）。 */
function dataStartAt(d: Uint8Array, localOffset: number): number | null {
  if (localOffset + 30 > d.length || u32(d, localOffset) !== LOCAL_FILE_HEADER_SIG) return null
  const nameLen = u16(d, localOffset + 26)
  const extraLen = u16(d, localOffset + 28)
  return localOffset + 30 + nameLen + extraLen
}

/** 按压缩方法解出 entry 内容。 */
function inflateEntry(method: number, compressed: Uint8Array): Uint8Array {
  if (method === 0) return compressed.slice() // STORE：原样拷贝（独立缓冲）
  if (method === 8) return inflateSync(compressed) // DEFLATE
  throw new Error(`unsupported zip compression method ${method}`)
}

/**
 * 解包 zip 字节为 {@link ZipEntry}[]，文件名按 UTF-8/GBK 正确解码（FR-250 / BUG-G）。
 * 目录项（名以 `/` 结尾）与空名跳过。以 central directory 为准切分内容，稳妥处理数据描述符情形。
 * 遇不支持的压缩方法或损坏结构抛错，交上层提示（与既有 fflate 失败提示一致）。
 */
export async function unzipWithNames(data: Uint8Array): Promise<ZipEntry[]> {
  const central = readCentralDirectory(data)
  if (!central) throw new Error('invalid zip: end-of-central-directory not found')
  const out: ZipEntry[] = []
  for (const e of central) {
    const name = decodeName(e.nameBytes, e.utf8)
    if (name === '' || name.endsWith('/')) continue // 跳过目录项/空名
    const start = dataStartAt(data, e.localOffset)
    if (start === null) throw new Error(`invalid zip: bad local header for ${name}`)
    void DATA_DESCRIPTOR_FLAG // central 的 compressedSize 权威，数据描述符情形无需读 local size
    const compressed = data.subarray(start, start + e.compressedSize)
    out.push({ name, data: inflateEntry(e.method, compressed) })
  }
  return out
}
