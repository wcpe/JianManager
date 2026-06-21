/**
 * Cron 表达式的轻量前端校验（FR-012）。
 *
 * 仅做「基本格式」校验用于即时提示，不追求与后端 cron 库逐字段语义等价——
 * 最终合法性以 Control Plane 解析为准。接受标准 5 字段（分 时 日 月 周）或
 * 含秒的 6 字段；每个字段允许 `* / , -` 与数字（覆盖常见用法）。
 */

/** 单个 cron 字段允许的字符：数字、`*`、`/`、`,`、`-`。 */
const FIELD_RE = /^[\d*/,-]+$/

/** 校验结果：是否合法 + 不合法时的提示 key（i18n schedules 命名空间）。 */
export interface CronValidation {
  valid: boolean
  /** 不合法时的 i18n key；合法时为 undefined。 */
  messageKey?: string
}

/**
 * 校验 cron 表达式基本格式。
 * 空串、字段数不为 5/6、或字段含非法字符均判为不合法并给出提示 key。
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
  if (!fields.every((f) => FIELD_RE.test(f))) {
    return { valid: false, messageKey: 'schedules.cronInvalidChar' }
  }
  return { valid: true }
}
