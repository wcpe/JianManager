/** 导入向导路径工具（FR-374）：绝对路径 join / 权限错误识别。 */

/** 在节点绝对路径下拼接相对段（兼容 Windows `\` 与 Unix `/`）。 */
export function joinAbsPath(root: string, rel: string): string {
  const r = root.replace(/[\\/]+$/, '')
  const s = rel.replace(/^[\\/]+/, '')
  if (!s) return r
  const sep = root.includes('\\') && !root.includes('/') ? '\\' : '/'
  return `${r}${sep}${s}`
}

/** 是否像权限类错误文案（中英 errno 与 FR-373 中文诊断）。 */
export function isPermissionErrorMessage(msg: string): boolean {
  const m = msg.toLowerCase()
  return (
    m.includes('permission denied') ||
    m.includes('没有权限') ||
    m.includes('eacces') ||
    m.includes('eperm') ||
    m.includes('access is denied')
  )
}
