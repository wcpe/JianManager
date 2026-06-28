/**
 * 客户端发布版本向导的分步逻辑（FR-187）。
 *
 * 把原「一屏发布」重排为分步向导：选文件 → 逐文件配置 → 托管目录/说明 → 预览 → 发布。
 * 这里只放与 React/DOM 无关的纯逻辑（步骤顺序、单文件路径校验、各步可否前进、
 * 托管目录解析），便于单测；UI 仅消费这些函数渲染与门控按钮。
 */

/** 向导步骤稳定标识（决定顺序与进度指示）。 */
export type PublishStepId = 'files' | 'configure' | 'meta' | 'review'

/** 向导步骤固定顺序。 */
export const PUBLISH_STEPS: PublishStepId[] = ['files', 'configure', 'meta', 'review']

/** 校验单个文件草稿的目标路径：非空、相对（不以 / 开头）、不含 `..` 越界。 */
export function isDraftPathValid(path: string): boolean {
  const p = path.trim()
  return p !== '' && !p.startsWith('/') && !p.includes('..')
}

/** 全部草稿路径是否都合法（空列表视为不合法，发布无意义）。 */
export function allPathsValid(paths: string[]): boolean {
  return paths.length > 0 && paths.every(isDraftPathValid)
}

/**
 * 解析托管目录输入（逗号/换行分隔、去重、去首尾空白与结尾斜杠、去空项）。
 * 与原实现一致，仅抽出便于单测。
 */
export function parseManagedDirs(raw: string): string[] {
  const seen = new Set<string>()
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim().replace(/\/+$/, ''))
    .filter((s) => s !== '' && !seen.has(s) && (seen.add(s), true))
}

/** 当前向导状态（用于判断各步是否满足前进/发布条件）。 */
export interface WizardState {
  /** 已上传草稿文件数。 */
  draftCount: number
  /** 各草稿目标路径（按草稿顺序）。 */
  paths: string[]
  /** 是否正在上传文件（上传中禁止前进/发布）。 */
  uploading: boolean
}

/** 给定步骤在当前状态下能否「下一步」（review 之后由 canPublish 判定，不在此列）。 */
export function canAdvance(step: PublishStepId, state: WizardState): boolean {
  if (state.uploading) return false
  switch (step) {
    case 'files':
      // 选了文件才能进入逐文件配置。
      return state.draftCount > 0
    case 'configure':
      // 所有路径合法才能进入元信息步。
      return allPathsValid(state.paths)
    case 'meta':
      // 托管目录/说明均可选，进入预览无额外门槛。
      return true
    case 'review':
      // 预览步无「下一步」（终点为发布）。
      return false
  }
}

/** 是否允许最终发布（有文件、路径全合法、未在上传中）。 */
export function canPublish(state: WizardState): boolean {
  return state.draftCount > 0 && allPathsValid(state.paths) && !state.uploading
}

/** 取下一步标识（已是最后一步则返回自身）。 */
export function nextStep(step: PublishStepId): PublishStepId {
  const i = PUBLISH_STEPS.indexOf(step)
  return PUBLISH_STEPS[Math.min(i + 1, PUBLISH_STEPS.length - 1)]
}

/** 取上一步标识（已是第一步则返回自身）。 */
export function prevStep(step: PublishStepId): PublishStepId {
  const i = PUBLISH_STEPS.indexOf(step)
  return PUBLISH_STEPS[Math.max(i - 1, 0)]
}

// ── FR-191：zip 上传归一 / 文件树 / 草稿 dirty 判定 ─────────────────────────

/**
 * 归一为 manifest 用的 POSIX 相对路径（FR-191）：
 * 反斜杠→正斜杠、剥离前导 `./` 段与前导 `/`、压缩重复斜杠、去首尾空白。
 * 仅做形态归一，**不**解析 `..` 越界（越界由 {@link isDraftPathValid} 拦），
 * 既用于 zip entry 相对路径，也用于编排时的路径输入清洗。
 */
export function normalizeManifestPath(raw: string): string {
  let p = raw.trim().replace(/\\/g, '/')
  // 剥离前导 ./ 段（可能多层：././x）
  while (p.startsWith('./')) p = p.slice(2)
  if (p === '.') p = ''
  // 压缩重复斜杠
  p = p.replace(/\/{2,}/g, '/')
  // 剥离前导斜杠（绝对路径化为相对）
  p = p.replace(/^\/+/, '')
  return p
}

/** 是否为 zip 文件名（按扩展名，大小写不敏感）。 */
export function isZipFilename(name: string): boolean {
  return /\.zip$/i.test(name.trim())
}

/**
 * 发布向导是否存在未发布草稿（FR-191 防误关判定）。
 * 已上传任一文件（内容寻址、已落服务端）即视为有草稿——
 * 关闭/点遮罩/Esc 需二次确认才能放弃；空向导（0 文件）照常关闭，不拦截。
 */
export function hasPublishDraft(draftCount: number): boolean {
  return draftCount > 0
}

/** 文件树叶节点（一个 manifest 文件 + 回源 index 供编排定位）。 */
export interface TreeFile {
  /** 在草稿/文件数组中的下标（编排 patch/remove 定位用）。 */
  index: number
  /** 完整相对路径（POSIX）。 */
  path: string
  /** 叶文件名（path 末段）。 */
  name: string
  sync: ManifestFileLike['sync']
  platform: ManifestFileLike['platform']
  size: number
  /** 内容是否锁定（内容寻址不可改字节，仅可编排/移除）。 */
  locked: boolean
}

/** 文件树目录节点（递归）。聚合字段便于 UI 显示子树规模。 */
export interface TreeDir {
  /** 目录名（末段）。 */
  name: string
  /** 完整相对路径（POSIX，从根到本目录）。 */
  path: string
  /** 子目录（字母序）。 */
  dirs: TreeDir[]
  /** 本目录直属文件（字母序）。 */
  files: TreeFile[]
  /** 递归文件总数（含子目录）。 */
  fileCount: number
  /** 递归字节总和（含子目录）。 */
  totalSize: number
}

/** 构树所需的最小 manifest 文件形态（避免与 api 层类型耦合）。 */
export interface ManifestFileLike {
  path: string
  sync: 'strict' | 'once' | 'ignore'
  platform: '' | 'windows' | 'macos' | 'linux'
  size: number
}

/**
 * 把扁平的 manifest 文件列表按 `path` 的 `/` 分段构建为目录树（FR-191）。
 * 叶=文件、枝=目录；目录在前文件在后、各自字母序；目录聚合递归文件数与字节数。
 * 文件携回源 `index` 以便编排（改路径/sync/platform/删除）定位原数组项。
 * 纯函数、与 React 无关，便于单测。
 */
export function buildFileTree(files: ManifestFileLike[]): TreeDir {
  const root: TreeDir = { name: '', path: '', dirs: [], files: [], fileCount: 0, totalSize: 0 }

  files.forEach((f, index) => {
    const segments = normalizeManifestPath(f.path)
      .split('/')
      .filter((s) => s !== '')
    if (segments.length === 0) return // 防御：纯斜杠/空路径跳过
    const name = segments[segments.length - 1]
    const dirSegments = segments.slice(0, -1)

    // 逐段下钻/创建目录节点
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

    cursor.files.push({
      index,
      path: segments.join('/'),
      name,
      sync: f.sync,
      platform: f.platform,
      size: f.size,
      locked: true,
    })
  })

  sortTree(root)
  aggregate(root)
  return root
}

/** 递归把每个目录的子目录/文件按名字母序排序（目录天然在 dirs、文件在 files，渲染时目录在前）。 */
function sortTree(dir: TreeDir): void {
  dir.dirs.sort((a, b) => a.name.localeCompare(b.name))
  dir.files.sort((a, b) => a.name.localeCompare(b.name))
  dir.dirs.forEach(sortTree)
}

/** 递归回填每个目录的 fileCount/totalSize（含子目录），返回本目录聚合值。 */
function aggregate(dir: TreeDir): { count: number; size: number } {
  let count = dir.files.length
  let size = dir.files.reduce((s, f) => s + f.size, 0)
  for (const child of dir.dirs) {
    const agg = aggregate(child)
    count += agg.count
    size += agg.size
  }
  dir.fileCount = count
  dir.totalSize = size
  return { count, size }
}
