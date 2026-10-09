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
    const host = document.querySelector('[role="dialog"]') ?? document.body
    host.appendChild(ta)
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, ta.value.length)
    const ok = document.execCommand('copy')
    host.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

/** {@link readClipboard} 的结果：失败必带可识别原因，绝不静默返回 undefined。 */
export type ClipboardReadResult =
  | { ok: true; text: string }
  | { ok: false; reason: 'unavailable' | 'denied' }

/**
 * 读取剪贴板文本，兼容 HTTP 非安全上下文——读不到时给出**可识别的失败原因**。
 *
 * 与 {@link copyToClipboard} 对称，但**读方向没有 execCommand 回退**：
 * `document.execCommand('paste')` 被现代浏览器出于安全原因一律禁用。
 *
 * 之所以必须显式区分失败：面板常部署在 `http://<LAN-IP>:50100` 明文 HTTP，
 * 此时整个 `navigator.clipboard` 为 undefined。写成 `navigator.clipboard?.readText()`
 * 时可选链既不抛错也读不到内容，只是静默返回 undefined，调用方若只判 `if (text)`
 * 就表现为「点粘贴毫无反应」——这不是「剪贴板为空」，是 API 根本不存在。
 *
 * 两种失败都无 JS 兜底，调用方必须提示用户改按键盘 `Ctrl+V`：原生粘贴事件不经剪贴板
 * API，在非安全上下文下依然可用。
 *
 * @returns `ok:true` 带文本；`ok:false` 带原因（`unavailable`=API 不存在（非安全上下文）、
 *          `denied`=API 存在但被拒（权限未授予 / 用户拒绝 / 文档失焦））
 */
export async function readClipboard(): Promise<ClipboardReadResult> {
  if (typeof navigator === 'undefined' || !navigator.clipboard?.readText) {
    return { ok: false, reason: 'unavailable' }
  }
  try {
    return { ok: true, text: await navigator.clipboard.readText() }
  } catch {
    return { ok: false, reason: 'denied' }
  }
}
