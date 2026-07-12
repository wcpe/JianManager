/**
 * Cron 表达式的前端工具（FR-012 / FR-153）：基本格式校验、下次执行预览、人类可读描述、常用预设。
 *
 * 校验仅做「基本格式」用于即时提示，不追求与后端 cron 库逐字段语义等价——最终合法性与调度
 * 以 Control Plane 解析为准。接受标准 5 字段（分 时 日 月 周）或含秒的 6 字段；预览/描述仅
 * 覆盖标准 5 字段，含秒的 6 字段只做格式校验、不参与预览/描述。
 */

/** 校验结果：是否合法 + 不合法时的提示 key（i18n schedules 命名空间）。 */
export interface CronValidation {
  valid: boolean
  /** 不合法时的 i18n key；合法时为 undefined。 */
  messageKey?: string
}

/** 单个 term 的结构：星号、数字、数字范围（a-b），可选加步长（斜杠 n）。整体锚定，杜绝空 term 与残缺写法（BUG-020）。 */
const TERM_RE = /^(\*|\d+(-\d+)?)(\/\d+)?$/

/** 校验单个字段：逗号分隔的 term 列表，每个 term 非空且结构合法（杜绝空 term 与残缺步长，BUG-020）。 */
function isValidField(field: string): boolean {
  if (field === '') return false
  return field.split(',').every((term) => term !== '' && TERM_RE.test(term))
}

/**
 * 校验 cron 表达式基本格式。
 * 空串、字段数不为 5/6、或任一字段结构非法均判为不合法并给出提示 key。
 */
export function validateCron(expr: string): CronValidation {
  const trimmed = expr.trim()
  if (trimmed === '') {
    return { valid: false, messageKey: 'schedules.cronRequired' }
  }
  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5 && fields.length !== 6) {
    return { valid: false, messageKey: 'schedules.cronFieldCount' }
  }
  if (!fields.every(isValidField)) {
    return { valid: false, messageKey: 'schedules.cronInvalidChar' }
  }
  return { valid: true }
}

/** 把单个字段展开为其取值域 [min,max] 内的允许值集合。字段须已通过 isValidField。 */
function expandField(field: string, min: number, max: number): Set<number> {
  const out = new Set<number>()
  for (const term of field.split(',')) {
    const [rangePart, stepPart] = term.split('/')
    const step = stepPart ? parseInt(stepPart, 10) : 1
    let lo = min
    let hi = max
    if (rangePart !== '*') {
      if (rangePart.includes('-')) {
        const [a, b] = rangePart.split('-').map((x) => parseInt(x, 10))
        lo = a
        hi = b
      } else {
        lo = hi = parseInt(rangePart, 10)
      }
    }
    for (let v = lo; v <= hi; v += step > 0 ? step : 1) {
      if (v >= min && v <= max) out.add(v)
    }
  }
  return out
}

/**
 * 计算 cron 表达式从 `from` 起的下 `count` 次执行时间（按浏览器本地时区逐分钟扫描）。
 * 仅支持标准 5 字段；非法或 6 字段返回空数组。最多向前扫描 366 天（永不命中的表达式返回已找到的部分）。
 *
 * 注意：实际调度由 Control Plane 按服务器时区解释，本预览仅供配置时直觉参考。
 */
export function nextRuns(expr: string, count = 5, from: Date = new Date()): Date[] {
  const trimmed = expr.trim()
  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5 || !validateCron(trimmed).valid) return []
  const [minF, hourF, domF, monF, dowF] = fields
  const mins = expandField(minF, 0, 59)
  const hours = expandField(hourF, 0, 23)
  const doms = expandField(domF, 1, 31)
  const mons = expandField(monF, 1, 12)
  const dows = expandField(dowF, 0, 7) // 0 与 7 均表周日
  const domRestricted = domF !== '*'
  const dowRestricted = dowF !== '*'

  const results: Date[] = []
  const cursor = new Date(from)
  cursor.setSeconds(0, 0)
  cursor.setMinutes(cursor.getMinutes() + 1) // 从下一分钟起
  const limit = new Date(cursor)
  limit.setDate(limit.getDate() + 366)

  while (results.length < count && cursor <= limit) {
    const dow = cursor.getDay() // 0=周日..6=周六
    const dowMatch = dows.has(dow) || (dow === 0 && dows.has(7))
    const domMatch = doms.has(cursor.getDate())
    // 标准 cron：日与周都受限时取「或」，否则取「与」。
    const dayOk =
      domRestricted && dowRestricted
        ? domMatch || dowMatch
        : domRestricted
          ? domMatch
          : dowRestricted
            ? dowMatch
            : true
    if (
      mins.has(cursor.getMinutes()) &&
      hours.has(cursor.getHours()) &&
      mons.has(cursor.getMonth() + 1) &&
      dayOk
    ) {
      results.push(new Date(cursor))
    }
    cursor.setMinutes(cursor.getMinutes() + 1)
  }
  return results
}

/** 人类可读描述：i18n key + 参数；无法识别的复杂表达式返回 null（UI 退回下次执行预览）。 */
export interface CronDescription {
  key: string
  params?: Record<string, string | number>
}

const pad2 = (s: string | number) => String(s).padStart(2, '0')

/**
 * 把最常见的几类 cron 翻成可读描述（FR-153）：每 N 分钟 / 每小时第 M 分 / 每天 HH:MM。
 * 复杂表达式不强行翻译，返回 null。
 */
export function describeCron(expr: string): CronDescription | null {
  const fields = expr.trim().split(/\s+/)
  if (fields.length !== 5 || !validateCron(expr).valid) return null
  const [min, hour, dom, mon, dow] = fields
  const isNum = (s: string) => /^\d+$/.test(s)
  const allStar = dom === '*' && mon === '*' && dow === '*'
  if (allStar && /^\*\/\d+$/.test(min) && hour === '*') {
    return { key: 'schedules.descEveryNMin', params: { n: Number(min.slice(2)) } }
  }
  if (allStar && isNum(min) && hour === '*') {
    return { key: 'schedules.descHourlyAt', params: { m: Number(min) } }
  }
  if (allStar && isNum(min) && isNum(hour)) {
    return { key: 'schedules.descDailyAt', params: { time: `${pad2(hour)}:${pad2(min)}` } }
  }
  return null
}

/** 常用 cron 预设（FR-153）：快捷填充常见调度。 */
export const CRON_PRESETS: { labelKey: string; expr: string }[] = [
  { labelKey: 'schedules.presetEvery5Min', expr: '*/5 * * * *' },
  { labelKey: 'schedules.presetHourly', expr: '0 * * * *' },
  { labelKey: 'schedules.presetDaily', expr: '0 4 * * *' },
  { labelKey: 'schedules.presetWeekly', expr: '0 4 * * 1' },
  { labelKey: 'schedules.presetMonthly', expr: '0 4 1 * *' },
]
