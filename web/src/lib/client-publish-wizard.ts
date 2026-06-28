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
