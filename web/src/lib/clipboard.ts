/**
 * 复制文本到剪贴板，兼容 HTTP 非安全上下文（FR-188）。
 *
 * `navigator.clipboard` 仅在安全上下文（HTTPS / localhost）可用；本面板常部署在
 * `http://<LAN-IP>:8080` 明文 HTTP，此时 `navigator.clipboard` 为 undefined，
 * 直接调用必抛错。故优先用原生 API，不可用或抛错时回退到 `document.execCommand('copy')`
 * + 离屏 textarea（该路径在 HTTP 非安全上下文下仍可用）。全站复制点统一走本函数。
 *
 * @param text 待复制文本
 * @returns 是否复制成功（调用方据此提示成功/失败）
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 安全上下文 API 不可用（权限/非安全上下文），落到 execCommand 回退
    }
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-9999px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
