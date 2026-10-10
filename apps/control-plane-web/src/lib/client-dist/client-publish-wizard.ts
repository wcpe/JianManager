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
  // 下标已夹在 [0, length-1] 内必然命中；取不到（步列表为空，类型上不可达）时退回自身。
  return PUBLISH_STEPS.at(Math.min(i + 1, PUBLISH_STEPS.length - 1)) ?? step
}

/** 取上一步标识（已是第一步则返回自身）。 */
export function prevStep(step: PublishStepId): PublishStepId {
  const i = PUBLISH_STEPS.indexOf(step)
  // 下标已夹在 [0, length-1] 内必然命中；取不到（步列表为空，类型上不可达）时退回自身。
  return PUBLISH_STEPS.at(Math.max(i - 1, 0)) ?? step
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
 * 发布向导是否存在未发布草稿（FR-191/FR-250 防误关判定）。
 * FR-191 语义：已上传任一文件即视为有草稿。FR-250 反转为**本地暂存**后语义不变——
 * 只要本地草稿非空（尚未发布）即 dirty，关闭/点遮罩/Esc/后退需二次确认才能放弃；空向导（0 文件）照常关闭。
 */
export function hasPublishDraft(draftCount: number): boolean {
  return draftCount > 0
}

// ── FR-250：目录 entry 递归收集 / 本地去重 / 批量上传进度归并 ─────────────────

/**
 * `webkitGetAsEntry()` 返回的 `FileSystemEntry` 的最小可测形态（FR-250）。
 * 只声明 {@link collectEntries} 递归所需字段，避免直接依赖 DOM lib 的 FileSystemEntry
 * （其 `file()`/`readEntries()` 为回调式，难在纯逻辑单测里构造）。真实调用处传浏览器原生 entry。
 */
export interface FileSystemEntryLike {
  /** 是否文件（对应原生 `entry.isFile`）。 */
  isFile: boolean
  /** 是否目录（对应原生 `entry.isDirectory`）。 */
  isDirectory: boolean
  /** 根到本 entry 的完整路径（对应原生 `entry.fullPath`，形如 `/mods/a.jar`）。 */
  fullPath: string
  /** 文件 entry：取 File（Promise 化原生回调 `file(cb)`）。 */
  file?: () => Promise<File>
  /** 目录 entry：一次性读全部直接子项（Promise 化原生 `createReader().readEntries` 的分批循环）。 */
  readEntries?: () => Promise<FileSystemEntryLike[]>
}

/** 一个待处理的本地单元：浏览器内 File + 归一后的相对 POSIX 路径（尚未上传，无 sha256）。 */
export interface LocalUnit {
  file: File
  /** 相对 gameDir 的目标路径（POSIX 归一、剥前导 `/`），源自 entry.fullPath 或散文件名。 */
  path: string
}

/**
 * 递归收集 `webkitGetAsEntry()` 的 entry 森林为扁平本地单元表（FR-250）。
 *
 * 文件 entry → 取 File + path=`normalizeManifestPath(fullPath)`（保相对目录结构）；
 * 目录 entry → `readEntries()` 深度优先下钻。跳过 `.DS_Store` 与 `__MACOSX/` 噪音、空路径项。
 * 纯逻辑（不碰 DOM，entry 经 {@link FileSystemEntryLike} 注入），便于单测目录树递归。
 * 顺序 = 深度优先前序（稳定，便于断言）。
 */
export async function collectEntries(entries: FileSystemEntryLike[]): Promise<LocalUnit[]> {
  const out: LocalUnit[] = []
  for (const entry of entries) {
    if (entry.isFile && entry.file) {
      const path = normalizeManifestPath(entry.fullPath)
      const base = path.split('/').pop() || ''
      if (path === '' || base === '.DS_Store' || path.startsWith('__MACOSX/')) continue
      out.push({ file: await entry.file(), path })
    } else if (entry.isDirectory && entry.readEntries) {
      const children = await entry.readEntries()
      const nested = await collectEntries(children)
      out.push(...nested)
    }
  }
  return out
}

/**
 * 本地去重键（FR-250）：`name + '\0' + size` 近似判同。
 * 精确判同需读全量算 hash（费时），本地仅用 name+size 近似省重复上传；
 * CAS 服务端按真实 sha256 兜底去重，前端误判最坏多传一次、不影响正确性。
 */
export function localDedupKey(name: string, size: number): string {
  // 分隔符用 \u0000 转义而非裸 NUL 字节：语义相同，但裸字节会让 git 把整个文件判为二进制，
  // 此后本文件的任何改动在 review 里只显示 Binary files differ，不可审阅。
  return `${name}\u0000${size}`
}

/** {@link dedupUnits} 结果：去重后需实际上传的单元 + 每个原单元指向的去重键（供发布时按键复用上传结果）。 */
export interface DedupPlan<T> {
  /** 去重后唯一单元（保留每个键首次出现者，顺序稳定）。 */
  unique: T[]
  /** 与输入等长：第 i 个原单元的去重键（发布时据此从「键→上传结果」映射取复用结果）。 */
  keys: string[]
}

/**
 * 按 {@link localDedupKey}(name,size) 去重本地单元（FR-250 省带宽）。
 * 同键仅首个进入 `unique`（只上传一次）；`keys[i]` 记第 i 个原单元的键，
 * 发布时先上传 `unique` 得「键→结果」，再按 `keys` 为每个原单元回填结果（含被去重者复用）。
 * 纯函数，`keyOf` 从元素取 name/size，便于单测。
 */
export function dedupUnits<T>(units: T[], keyOf: (u: T) => { name: string; size: number }): DedupPlan<T> {
  const unique: T[] = []
  const keys: string[] = []
  const seen = new Set<string>()
  for (const u of units) {
    const { name, size } = keyOf(u)
    const key = localDedupKey(name, size)
    keys.push(key)
    if (!seen.has(key)) {
      seen.add(key)
      unique.push(u)
    }
  }
  return { unique, keys }
}

/**
 * 归并批量上传的总体已传字节（FR-250）：已完成单元累计 `baseBytes` + 当前单元已传 `fileUploaded`，
 * 上限不超过 `totalBytes`（防末片按整片计或回填误差越界）。纯函数，便于单测进度数学。
 */
export function batchProgressBytes(baseBytes: number, fileUploaded: number, totalBytes: number): number {
  return Math.min(baseBytes + fileUploaded, totalBytes)
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
    // 末段即文件名；取不到说明没有有效段（纯斜杠/空路径），防御性跳过。
    const name = segments.at(-1)
    if (name === undefined) return
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

// ── FR-254：文件树拖拽编排（拖拽文件/目录节点改目标路径） ──────────────────

/**
 * 把文件移动到指定目录下，计算新目标路径（FR-254）。
 * @param filePath 文件当前完整相对路径
 * @param dirPath 目标目录完整路径；空串表示移到根（仅留文件名）
 * @returns 新路径；若文件名与目标目录组合后路径不变则返回原值
 */
export function moveFileToDir(filePath: string, dirPath: string): string {
  const name = filePath.split('/').pop() ?? filePath
  const d = dirPath.trim().replace(/\/+$/, '')
  const newPath = d === '' ? name : `${d}/${name}`
  return newPath === filePath ? filePath : newPath
}

/**
 * 把目录移动到目标目录下，计算新目录路径（FR-254）。
 * @param srcDirPath 被拖拽目录的完整路径
 * @param targetDirPath 目标目录完整路径；空串表示移到根
 * @returns 新目录路径；源等于目标时返回原值（no-op）
 */
export function moveDirToDir(srcDirPath: string, targetDirPath: string): string {
  const srcName = srcDirPath.split('/').pop() ?? srcDirPath
  const t = targetDirPath.trim().replace(/\/+$/, '')
  const newPath = t === '' ? srcName : `${t}/${srcName}`
  return newPath === srcDirPath ? srcDirPath : newPath
}

/**
 * 递归收集目录子树全部文件（含子目录），返回 index+path 列表（FR-254）。
 * 供拖拽目录节点时批量改路径用。
 */
export function collectSubtreeFiles(dir: TreeDir): { index: number; path: string }[] {
  const out: { index: number; path: string }[] = []
  for (const f of dir.files) {
    out.push({ index: f.index, path: f.path })
  }
  for (const child of dir.dirs) {
    out.push(...collectSubtreeFiles(child))
  }
  return out
}

/**
 * 判定 targetDir 是否为 srcDir 自身或其后代（FR-254）。
 * 拖拽目录到自身或子目录是非法操作（会造成循环嵌套），须阻止。
 */
export function isSelfOrDescendant(srcDirPath: string, targetDirPath: string): boolean {
  if (targetDirPath === srcDirPath) return true
  const prefix = srcDirPath.endsWith('/') ? srcDirPath : `${srcDirPath}/`
  return targetDirPath.startsWith(prefix)
}

// ── FR-255：清理范围目录树勾选 ──────────────────────────────────────────

/**
 * 从草稿文件列表派生所有目录路径（含深层嵌套，FR-255 managedDirs 目录树勾选用）。
 *
 * 遍历每个文件 path 的目录前缀段，逐层累加收集。例：
 * `mods/a.jar` → `mods`；`mods/sub/b.jar` → `mods`、`mods/sub`；
 * 根目录散文件（如 `options.txt`）不产生目录。
 * 结果按字母序排列，便于稳定渲染与断言。纯函数，便于单测。
 */
export function collectAllDirPaths(files: ManifestFileLike[]): string[] {
  const dirs = new Set<string>()
  for (const f of files) {
    const segs = normalizeManifestPath(f.path).split('/').filter((s) => s !== '')
    let acc = ''
    // 逐层累加目录前缀（最后一段是文件名，跳过）。
    for (const seg of segs.slice(0, -1)) {
      acc = acc === '' ? seg : `${acc}/${seg}`
      dirs.add(acc)
    }
  }
  return Array.from(dirs).sort()
}

/**
 * 「清空整个 gameDir」哨兵值（FR-255）：managedDirs 含此值时，
 * 客户端视为整个 gameDir 托管，删除清单未列的一切（玩家区 + cleanExclude 除外）。
 */
export const CLEAN_ALL_SENTINEL = '*'

/**
 * 判定 managedDirs 是否启用了 clean-all（含哨兵 `"*"`）。
 */
export function isCleanAll(managedDirs: string[]): boolean {
  return managedDirs.includes(CLEAN_ALL_SENTINEL)
}

// ── FR-261：文件资源管理器（新建文件夹/重命名/冲突处理） ────────────────────

/**
 * 构树时合并空目录（FR-261 新建文件夹）。
 *
 * 先用 {@link buildFileTree} 从 files 构树，再把 `emptyDirs` 中尚不存在的目录
 * 作为空目录节点插入树中。已存在（被文件占据）的目录跳过（不重复创建）。
 * 插入后重新排序与聚合，保证树结构一致。纯函数，便于单测。
 */
export function buildFileTreeWithDirs(files: ManifestFileLike[], emptyDirs: string[]): TreeDir {
  const root = buildFileTree(files)
  for (const raw of emptyDirs) {
    const segments = normalizeManifestPath(raw).split('/').filter((s) => s !== '')
    if (segments.length === 0) continue
    let cursor = root
    let acc = ''
    for (const seg of segments) {
      acc = acc === '' ? seg : `${acc}/${seg}`
      let child = cursor.dirs.find((d) => d.name === seg)
      if (!child) {
        child = { name: seg, path: acc, dirs: [], files: [], fileCount: 0, totalSize: 0 }
        cursor.dirs.push(child)
      }
      cursor = child
    }
  }
  sortTree(root)
  aggregate(root)
  return root
}

/**
 * 重命名路径的最后一段（文件名或目录名，FR-261）。
 *
 * 保留目录前缀，仅替换末段为 newName。例：`mods/sub/a.jar` + `b.jar` → `mods/sub/b.jar`；
 * 根级 `a.jar` + `b.jar` → `b.jar`。纯函数。
 */
export function renamePathSegment(path: string, newName: string): string {
  const segments = normalizeManifestPath(path).split('/').filter((s) => s !== '')
  if (segments.length === 0) return newName
  segments[segments.length - 1] = newName
  return segments.join('/')
}

/**
 * 在同级已有名称中生成不冲突的唯一名（FR-261 新建文件夹命名）。
 *
 * base 不在 existingNames 中 → 返回 base；否则尝试 `base 2`、`base 3`…直到唯一。
 * 纯函数。
 */
export function nextUniqueName(base: string, existingNames: string[]): string {
  const set = new Set(existingNames)
  if (!set.has(base)) return base
  let n = 2
  while (set.has(`${base} ${n}`)) n++
  return `${base} ${n}`
}

// ── FR-350：多级目录一次建 / 右键定点上传 ───────────────────────────────────

/** {@link parseDirPathsInput} 结果：合法目录路径 + 被拒绝的原始行。 */
export interface ParsedDirPaths {
  /** 归一后的目录路径（去重、保输入顺序、不含结尾斜杠）。 */
  dirs: string[]
  /** 被拒绝的原始行（trim 后）：含 `..` 越界段，或归一后为空的非空白行。 */
  invalid: string[]
}

/**
 * 解析「新建多级目录」多行输入（FR-350）：每行一条路径，一行可为 `a/b/c` 整条层级。
 *
 * 每行经 {@link normalizeManifestPath} 归一（反斜杠/前导 `./`、`/`、重复斜杠），
 * 再折叠 `.` 段与空段、剥结尾斜杠；含 `..` 段或归一后为空的非空白行进 `invalid`
 * （安全拒绝越界，与 isDraftPathValid 同口径）；纯空白行静默跳过；重复行去重保首个。
 * 纯函数，便于单测。
 */
export function parseDirPathsInput(raw: string): ParsedDirPaths {
  const dirs: string[] = []
  const invalid: string[] = []
  const seen = new Set<string>()
  for (const line of raw.split('\n')) {
    const trimmed = line.trim()
    if (trimmed === '') continue
    const segs = normalizeManifestPath(trimmed)
      .split('/')
      .filter((s) => s !== '' && s !== '.')
    if (segs.length === 0 || segs.some((s) => s === '..')) {
      invalid.push(trimmed)
      continue
    }
    const clean = segs.join('/')
    if (!seen.has(clean)) {
      seen.add(clean)
      dirs.push(clean)
    }
  }
  return { dirs, invalid }
}

/**
 * 把目录路径列表展开为全部层级链（FR-350）：`a/b/c` → `a`、`a/b`、`a/b/c`。
 * 跨路径共享的层级去重、保首次出现顺序。供模态预览「将创建哪些层级」与
 * 创建后展开祖先链使用。纯函数。
 */
export function expandDirChains(dirs: string[]): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const d of dirs) {
    let acc = ''
    for (const seg of d.split('/').filter((s) => s !== '')) {
      acc = acc === '' ? seg : `${acc}/${seg}`
      if (!seen.has(acc)) {
        seen.add(acc)
        out.push(acc)
      }
    }
  }
  return out
}

/**
 * 拼接目录前缀与子路径（FR-350 右键定点上传 / 模态基准目录）。
 * 两侧各自归一（剥结尾/前导斜杠），任一为空返回另一侧，避免产生 `//` 或前导斜杠。
 * 纯函数。
 */
export function joinDirPath(dirPath: string, subPath: string): string {
  const d = normalizeManifestPath(dirPath).replace(/\/+$/, '')
  const p = normalizeManifestPath(subPath)
  if (d === '') return p
  if (p === '') return d
  return `${d}/${p}`
}

/** 冲突解决策略（FR-261 同名冲突弹窗）。 */
export type ConflictResolution = 'skip' | 'replace' | 'keepBoth'

/**
 * 为冲突路径生成「保留两者」的新路径（文件名加 `(n)` 数字后缀，FR-261）。
 *
 * 保留目录前缀，在文件名 stem 与扩展名之间插入 `(1)`、`(2)`…直到与 existing 不冲突。
 * 例：`mods/a.jar` + existing=`['mods/a.jar']` → `mods/a (1).jar`。
 * 无扩展名时后缀直接追加。纯函数。
 */
export function keepBothPath(conflictPath: string, existing: string[]): string {
  const normalized = normalizeManifestPath(conflictPath)
  const slashIdx = normalized.lastIndexOf('/')
  const dir = slashIdx >= 0 ? normalized.slice(0, slashIdx + 1) : ''
  const last = normalized.slice(dir.length)
  const dot = last.lastIndexOf('.')
  // dot <= 0 排除隐藏文件（如 .gitignore，dot=0）与无扩展名
  const stem = dot > 0 ? last.slice(0, dot) : last
  const ext = dot > 0 ? last.slice(dot) : ''
  const existingSet = new Set(existing)
  let n = 1
  let candidate: string
  do {
    candidate = `${dir}${stem} (${n})${ext}`
    n++
  } while (existingSet.has(candidate))
  return candidate
}

/**
 * 检测 incoming 路径中与 existing 路径冲突的项（FR-261）。
 *
 * @returns 冲突的 incoming 路径数组（保持 incoming 顺序，去重）
 */
export function detectConflicts(incoming: string[], existing: string[]): string[] {
  const set = new Set(existing)
  const seen = new Set<string>()
  const out: string[] = []
  for (const p of incoming) {
    if (set.has(p) && !seen.has(p)) {
      seen.add(p)
      out.push(p)
    }
  }
  return out
}

// ── FR-262：清理目录树形右键菜单可视化（三态标记 + 父子联动） ──────────────

/** 目录清理标记：清理或排除。 */
export type CleanMark = 'clean' | 'exclude'

/** 目录视觉四态：清理（红）/ 排除（绿）/ 混合（橙）/ 不管理（无色）。 */
export type DirVisualState = 'clean' | 'exclude' | 'mixed' | 'none'

/**
 * 从 manifest 的 managedDirs + cleanExclude 反向构建 cleanMap（FR-262）。
 *
 * managedDirs 中每条 → 'clean'；cleanExclude 中每条 → 'exclude'。
 * 同一路径同时出现在两者时，cleanExclude 优先（后写入覆盖）。
 * 纯函数，便于单测。
 */
export function buildCleanMap(managedDirs: string[], cleanExclude: string[]): Map<string, CleanMark> {
  const m = new Map<string, CleanMark>()
  for (const d of managedDirs) m.set(d, 'clean')
  for (const e of cleanExclude) m.set(e, 'exclude')
  return m
}

/**
 * 取某目录的所有后代目录路径（不含自身，FR-262）。
 *
 * 按路径前缀 `dirPath/` 匹配 allDirPaths 中的项。纯字符串操作，不依赖树结构。
 */
export function getDescendantDirPaths(dirPath: string, allDirPaths: string[]): string[] {
  const prefix = `${dirPath}/`
  return allDirPaths.filter((p) => p.startsWith(prefix))
}

/**
 * 计算目录的视觉四态（FR-262）。
 *
 * 规则：
 * - 有效标记 = 自身显式标记（优先）或最近祖先的显式标记（继承）
 * - 有有效标记 + 子树无不同显式标记 → 该标记（clean/exclude）
 * - 有有效标记 + 子树有不同显式标记 → mixed（橙）
 * - 无有效标记 + 子树全一致 → 继承子树标记（clean/exclude）
 * - 无有效标记 + 子树混杂 → mixed（橙）
 * - 无有效标记 + 子树无标记 → none（无色）
 *
 * 向上查找祖先标记使「exportMarkings 去子优化后子目录仍能继承父标记的视觉」。
 * 纯函数，便于单测。
 */
export function computeDirVisualState(
  dirPath: string,
  cleanMap: Map<string, CleanMark>,
  allDirPaths: string[],
): DirVisualState {
  const own = cleanMap.get(dirPath)
  const ancestorMark = findAncestorMark(dirPath, cleanMap)
  const effectiveMark = own ?? ancestorMark

  const descendants = getDescendantDirPaths(dirPath, allDirPaths)
  const descendantValues = new Set<CleanMark>()
  for (const dp of descendants) {
    const v = cleanMap.get(dp)
    if (v) descendantValues.add(v)
  }

  if (effectiveMark) {
    for (const v of descendantValues) {
      if (v !== effectiveMark) return 'mixed'
    }
    return effectiveMark
  }
  // 子树无显式标记 → 无色；只有一个不同标记 → 继承该标记；多于一个 → 混杂。
  const [soleMark, ...otherMarks] = descendantValues
  if (soleMark === undefined) return 'none'
  if (otherMarks.length === 0) return soleMark
  return 'mixed'
}

/**
 * 向上查找最近的祖先显式标记（FR-262）。
 *
 * 从 dirPath 的直接父目录逐级向上，返回 cleanMap 中第一个命中的标记。
 * 纯函数。
 */
export function findAncestorMark(dirPath: string, cleanMap: Map<string, CleanMark>): CleanMark | undefined {
  const segs = dirPath.split('/')
  for (let i = segs.length - 1; i >= 1; i--) {
    const ancestor = segs.slice(0, i).join('/')
    const v = cleanMap.get(ancestor)
    if (v) return v
  }
  return undefined
}

/**
 * 去掉已被同标记祖先覆盖的子路径（FR-262）。
 *
 * 如 `mods` 和 `mods/sub` 都标记 clean，只产出 `mods`（祖先已覆盖子）。
 * 纯函数。
 */
function dedupTopLevel(paths: string[]): string[] {
  const set = new Set(paths)
  return paths
    .filter((p) => {
      const segs = p.split('/')
      for (let i = 1; i < segs.length; i++) {
        if (set.has(segs.slice(0, i).join('/'))) return false
      }
      return true
    })
    .sort()
}

/**
 * 从 cleanMap 导出 managedDirs + cleanExclude（FR-262）。
 *
 * 去子优化：若祖先目录同标记，子目录不重复产出（客户端按前缀匹配，祖先已覆盖）。
 * 结果按字母序排列，便于稳定断言。纯函数。
 */
export function exportMarkings(cleanMap: Map<string, CleanMark>): {
  managedDirs: string[]
  cleanExclude: string[]
} {
  const cleanPaths: string[] = []
  const excludePaths: string[] = []
  for (const [path, mark] of cleanMap) {
    if (mark === 'clean') cleanPaths.push(path)
    else excludePaths.push(path)
  }
  return {
    managedDirs: dedupTopLevel(cleanPaths),
    cleanExclude: dedupTopLevel(excludePaths),
  }
}
