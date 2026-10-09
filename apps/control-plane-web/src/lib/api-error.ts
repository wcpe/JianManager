/**
 * HTTP / axios 错误的展示文案提取（全仓共用）。
 *
 * 为什么抽出来：后端所有错误响应统一为 `{ error, message }`，message 是面向用户的
 * 中文定向提示（如「实例运行中，请先停止实例再执行版本变更」）。原先每个 api 模块各自
 * 内联 `err.response?.data?.message || fallback`，个别模块（snapshots / binaryVersion）
 * 还各写了一份类型守卫版 —— 同一逻辑三处形态，改一处忘一处就会让某块界面丢定向提示。
 *
 * 只做「取 message」这一件事，不带 toast、不带 i18n：调用方自己决定 fallback 与呈现方式。
 */

/** 后端错误响应体（与 CP 全局 `gin.H{"error": ..., "message": ...}` 对齐）。 */
interface ApiErrorBody {
  error?: string
  message?: string
}

/**
 * 从未知错误对象中提取后端 message，取不到时返回 fallback。
 *
 * @param err 捕获到的错误（axios 错误 / 普通 Error / 任意抛出物）
 * @param fallback 无 message 时的兜底文案
 */
export function apiErrorMessage(err: unknown, fallback: string): string {
  if (typeof err === 'object' && err !== null && 'response' in err) {
    const message = (err as { response?: { data?: ApiErrorBody } }).response?.data?.message
    if (message) return message
  }
  return fallback
}
