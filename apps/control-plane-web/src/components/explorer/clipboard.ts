import { baseName, joinPath, isWithin } from './paths'

/**
 * 资源管理器剪贴板模型（FR-070）。
 *
 * 范围决策（见 specs/file-explorer/impl.md）：
 * - 剪切（cut）→ 粘贴 = 移动，经后端 rename（跨目录），文件与目录均支持；
 * - 复制（copy）→ 粘贴 = 真复制，经后端 read+write 组合，**仅文件**（无目录复制后端，目录复制本 FR 不做）。
 *
 * 纯函数，便于 vitest 单测。粘贴的具体 API 编排由组件层按本模块产出的计划执行。
 */

export type ClipboardMode = 'cut' | 'copy'

/** 剪贴板中的一个条目。 */
export interface ClipboardEntry {
  /** 相对工作目录的完整路径。 */
  path: string
  /** 是否目录。 */
  isDir: boolean
}

/** 剪贴板内容。 */
export interface Clipboard {
  mode: ClipboardMode
  entries: ClipboardEntry[]
}

/** 单条粘贴操作（move=rename；copy=读源写目标）。 */
export interface PasteOp {
  kind: 'move' | 'copy'
  /** 源路径。 */
  from: string
  /** 目标路径（targetDir + 源 basename）。 */
  to: string
}

/** 粘贴计划：可执行操作 + 被跳过项（及原因）。 */
export interface PastePlan {
  ops: PasteOp[]
  /** 被跳过的条目（目录复制不支持、目标与源同目录、移入自身子目录、目标已存在等）。 */
  skipped: { path: string; reason: SkipReason }[]
}

export type SkipReason =
  | 'dir-copy-unsupported' // 复制目录：本 FR 不支持
  | 'same-dir' // 移动/复制到原目录，无意义
  | 'into-self' // 把目录移动/复制进自身或子目录
  | 'name-conflict' // 目标已存在同名

/**
 * 基于剪贴板内容、目标目录与目标目录现有名字集合，生成粘贴计划。
 * @param clip 剪贴板
 * @param targetDir 目标目录（相对路径，空串=根）
 * @param existingNames 目标目录下已有的名字集合（用于冲突检测）
 */
export function planPaste(
  clip: Clipboard | null,
  targetDir: string,
  existingNames: Set<string>,
): PastePlan {
  const ops: PasteOp[] = []
  const skipped: { path: string; reason: SkipReason }[] = []
  if (!clip || clip.entries.length === 0) return { ops, skipped }

  for (const entry of clip.entries) {
    const name = baseName(entry.path)
    const to = joinPath(targetDir, name)
    const srcDir = entry.path.slice(0, Math.max(0, entry.path.length - name.length - 1))

    // 复制目录：不支持。
    if (clip.mode === 'copy' && entry.isDir) {
      skipped.push({ path: entry.path, reason: 'dir-copy-unsupported' })
      continue
    }
    // 目标即源所在目录：无意义。
    if (srcDir === targetDir) {
      skipped.push({ path: entry.path, reason: 'same-dir' })
      continue
    }
    // 把目录移入自身或其子目录。
    if (entry.isDir && isWithin(entry.path, targetDir)) {
      skipped.push({ path: entry.path, reason: 'into-self' })
      continue
    }
    // 目标已存在同名。
    if (existingNames.has(name)) {
      skipped.push({ path: entry.path, reason: 'name-conflict' })
      continue
    }
    ops.push({ kind: clip.mode === 'cut' ? 'move' : 'copy', from: entry.path, to })
  }

  return { ops, skipped }
}

/** 构造剪切剪贴板。 */
export function cutEntries(entries: ClipboardEntry[]): Clipboard {
  return { mode: 'cut', entries }
}

/** 构造复制剪贴板。 */
export function copyEntries(entries: ClipboardEntry[]): Clipboard {
  return { mode: 'copy', entries }
}
