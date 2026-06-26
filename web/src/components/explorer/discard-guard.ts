/**
 * 编辑器「未保存草稿」拦截的纯决策（BUG-018）。
 *
 * 切换文件 / 关闭编辑器 / 打开归档·反编译前，据当前打开文件的脏态与配置编辑器自管脏态，
 * 判断是否需要二次确认（避免静默丢失编辑）。决策逻辑从 ResourceExplorer 抽出为纯函数便于单测；
 * 实际的 window.confirm 弹出仍留在组件内（薄包装）。
 */

/** 资源管理器中打开文件的最小快照：路径 + 已保存内容 + 当前草稿（OpenFile 的子集）。 */
export interface OpenFileSnapshot {
  /** 文件相对路径（工作目录内）。 */
  path: string
  /** 上次保存到磁盘的内容。 */
  saved: string
  /** 编辑器当前草稿内容；与 saved 不同即为脏。 */
  draft: string
}

/**
 * 是否需要「丢弃未保存草稿」二次确认。
 *
 * @param open       当前打开的文本文件快照；null 表示未打开文本文件（如配置模式或空态）。
 * @param configDirty 配置编辑器子组件自管的脏态（配置模式经 onDirtyChange 上报，BUG-018 #36）。
 * @param nextPath   即将打开的目标路径；重开正在编辑的同一文件（path === nextPath）不算丢弃、不拦截；关闭场景不传。
 * @returns true=存在未保存草稿且非「重开同一文件」，需二次确认。
 */
export function needsDiscardConfirm(
  open: OpenFileSnapshot | null,
  configDirty: boolean,
  nextPath?: string,
): boolean {
  const fileDirty = open != null && open.draft !== open.saved && open.path !== nextPath
  const cfgDirty = configDirty && (open == null || open.path !== nextPath)
  return fileDirty || cfgDirty
}
