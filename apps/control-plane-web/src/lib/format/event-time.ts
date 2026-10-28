/**
 * 事件时间戳的格式化（跨端单位不一致的收敛点）。
 *
 * ## 为什么需要容错
 *
 * 本仓库的时间戳来自两条链，单位**不统一**：
 *  - Worker 侧 `PluginEvent.Timestamp` 曾用 `time.Now().Unix()`（秒），
 *    后改为 `UnixMilli()`（毫秒，与 CP 的 `OccurredAt` 对齐）；
 *  - 但前端有地方仍按秒处理（`timestamp * 1000`）。
 *
 * 混用的后果不是「差一点」，而是**差三个数量级**：毫秒值再乘 1000 会显示成
 * 公元 5 万年。而且它不报错、不崩溃，只是显示一个荒谬的日期——最容易漏过 review。
 *
 * ## 判据为什么是 1e10
 *
 * 以秒计的现代时间戳约 1.7e9（10 位），以毫秒计约 1.7e12（13 位）。
 * 取 1e10 作分界：秒值永远小于它、毫秒值（2001 年以后）永远大于它。
 * 这不是精确判定，但对「秒 vs 毫秒」这两种实际可能的输入是充分的。
 *
 * **长期方案**是让后端只发一种单位并在 API 层归一；本函数是过渡期的收敛点，
 * 新增的展示路径应复用它，而不是各自写一遍乘除。
 */

/** 毫秒时间戳的判定阈值：超过它按毫秒解释，否则按秒。 */
const MILLIS_THRESHOLD = 10_000_000_000

/** 把可能是秒、也可能是毫秒的时间戳归一成毫秒；非法输入返回 null。 */
function toMillis(timestamp: number): number | null {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return null
  return timestamp > MILLIS_THRESHOLD ? timestamp : timestamp * 1000
}

/** 格式化为本地时间（时:分:秒）。非法输入回退 `--` 而不是 `Invalid Date`。 */
export function formatEventTime(timestamp: number): string {
  const millis = toMillis(timestamp)
  return millis === null ? '--' : new Date(millis).toLocaleTimeString()
}

/** 同上，但返回完整日期时间（用于跨天的事件列表）。 */
export function formatEventDateTime(timestamp: number): string {
  const millis = toMillis(timestamp)
  return millis === null ? '--' : new Date(millis).toLocaleString()
}
