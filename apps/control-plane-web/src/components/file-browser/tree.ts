/**
 * 共享文件浏览器的扁平→层级建树（FR-213）。
 *
 * 与具体后端无关：输入一组 {@link FileEntry}（path/isDir/size），按 `path` 的 `/` 分段
 * 构建目录树，目录聚合递归文件数与字节数。供「扁平全量」数据源（如客户端分发 manifest）
 * 一次成树渲染；「懒加载分层」数据源不经此（按层拉取直接渲染）。
 *
 * 泛化自 FR-191 `lib/client-publish-wizard` 的 `buildFileTree`（那版携 manifest 专有字段
 * sync/platform/index，与 manifest 编排耦合）。本版只依赖通用 `FileEntry`，纯函数、node 可测。
 */
import type { FileEntry } from './types'

/** 文件浏览器树的文件叶节点。 */
export interface BrowserTreeFile {
  /** 原始条目（供预览/下载回传，含完整 path）。 */
  entry: FileEntry
  /** 叶文件名（path 末段）。 */
  name: string
}

/** 文件浏览器树的目录节点（递归）。 */
export interface BrowserTreeDir {
  /** 目录名（末段，根为空串）。 */
  name: string
  /** 完整相对路径（POSIX，从根到本目录；根为空串）。 */
  path: string
  /** 子目录（字母序）。 */
  dirs: BrowserTreeDir[]
  /** 本目录直属文件（字母序）。 */
  files: BrowserTreeFile[]
  /** 递归文件总数（含子目录）。 */
  fileCount: number
  /** 递归字节总和（含子目录）。 */
  totalSize: number
}

/** POSIX 归一：反斜杠→正斜杠、剥前导 `./` 与 `/`、压缩重复斜杠、去首尾空白。 */
function normalizePath(raw: string): string {
  let p = raw.trim().replace(/\\/g, '/')
  while (p.startsWith('./')) p = p.slice(2)
  if (p === '.') p = ''
  p = p.replace(/\/{2,}/g, '/')
  p = p.replace(/^\/+/, '')
  return p
}

/**
 * 把扁平条目列表构建为目录树（FR-213）。
 *
 * - 显式 `isDir` 条目建立（或复用）对应目录节点（即便它没有子文件，空目录也会出现）。
 * - 文件条目挂到其父目录下；沿途缺失的中间目录自动补建。
 * - 目录在前、文件在后，各自字母序；目录聚合递归 fileCount/totalSize。
 */
export function buildTree(entries: FileEntry[]): BrowserTreeDir {
  const root: BrowserTreeDir = { name: '', path: '', dirs: [], files: [], fileCount: 0, totalSize: 0 }

  /** 下钻/创建到给定目录段路径，返回末级目录节点。 */
  const ensureDir = (dirSegments: string[]): BrowserTreeDir => {
    let cursor = root
    let acc = ''
    for (const seg of dirSegments) {
      acc = acc === '' ? seg : `${acc}/${seg}`
      let child = cursor.dirs.find((d) => d.name === seg)
      if (!child) {
        child = { name: seg, path: acc, dirs: [], files: [], fileCount: 0, totalSize: 0 }
        cursor.dirs.push(child)
      }
      cursor = child
    }
    return cursor
  }

  for (const entry of entries) {
    const segments = normalizePath(entry.path)
      .split('/')
      .filter((s) => s !== '')
    if (segments.length === 0) continue // 防御：纯斜杠/空路径跳过

    if (entry.isDir) {
      ensureDir(segments) // 空目录也建节点
      continue
    }

    const name = segments[segments.length - 1]
    const parent = ensureDir(segments.slice(0, -1))
    parent.files.push({ entry, name })
  }

  sortTree(root)
  aggregate(root)
  return root
}

/** 递归把每个目录的子目录/文件按名字母序排序。 */
function sortTree(dir: BrowserTreeDir): void {
  dir.dirs.sort((a, b) => a.name.localeCompare(b.name))
  dir.files.sort((a, b) => a.name.localeCompare(b.name))
  dir.dirs.forEach(sortTree)
}

/** 递归回填每个目录的 fileCount/totalSize（含子目录）。 */
function aggregate(dir: BrowserTreeDir): { count: number; size: number } {
  let count = dir.files.length
  let size = dir.files.reduce((s, f) => s + (f.entry.size ?? 0), 0)
  for (const child of dir.dirs) {
    const agg = aggregate(child)
    count += agg.count
    size += agg.size
  }
  dir.fileCount = count
  dir.totalSize = size
  return { count, size }
}
