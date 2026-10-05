/**
 * 更新说明（release body markdown）内链接的安全打开策略（FR-186）。
 * 渲染 GitHub release body 的 markdown 时，链接不在应用内直跳（防被诱导导航/钓鱼），
 * 而是经「宿主确认」后在新标签打开。本模块抽出可测的纯逻辑：判定链接是否安全可打开。
 */

/** 允许在新标签打开的协议白名单（仅 http/https，排除 javascript:/data: 等危险 scheme）。 */
const SAFE_PROTOCOLS = new Set(['http:', 'https:'])

/**
 * 判定一个 href 是否为「可安全在新标签打开」的外部链接。
 * 仅放行 http/https 绝对 URL；相对链接、锚点、mailto、javascript: 等一律不放行（返回 false）。
 */
export function isSafeExternalLink(href: string | undefined | null): boolean {
  if (!href) return false
  let u: URL
  try {
    u = new URL(href)
  } catch {
    return false // 相对链接 / 锚点 / 非法 → 不作外部跳转。
  }
  return SAFE_PROTOCOLS.has(u.protocol)
}

/**
 * 构造点击外链时的确认提示文案（展示完整目标 URL，便于用户核对再决定是否离开）。
 */
export function confirmOpenMessage(href: string): string {
  return `即将在新标签页打开外部链接：\n${href}\n\n确定继续吗？`
}
