/**
 * 创建/编辑模态框统一字段校验（FR-072）。
 *
 * 纯函数，无 DOM/React 依赖，便于单测且可在任意表单复用。每个校验返回
 * i18n key（validation 命名空间）或 undefined（合法）。调用方据此做提交前阻断与内联提示。
 */

/** 单字段校验结果：合法时为 undefined，否则为 i18n 提示 key。 */
export type FieldError = string | undefined

/** 必填：去空白后非空。 */
export function validateRequired(value: string): FieldError {
  return value.trim() === '' ? 'validation.required' : undefined
}

/**
 * 最小长度校验工厂。空串放行（交由 required 把关）；非空但短于 min 时判错。
 * 返回的错误 key 为 `validation.minLength`（调用方用 i18n 插值 {{min}} 提示具体值）。
 */
export function minLength(min: number): (value: string) => FieldError {
  return (value: string) => {
    if (value === '') return undefined
    return value.length < min ? 'validation.minLength' : undefined
  }
}

/**
 * 端口号：1–65535 的整数。空串视为合法（由 required 单独把关），
 * 这样选填端口字段不会因留空而误报。
 */
export function validatePort(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  if (!/^\d+$/.test(trimmed)) return 'validation.portInteger'
  const n = Number(trimmed)
  if (n < 1 || n > 65535) return 'validation.portRange'
  return undefined
}

/**
 * 正整数（如内存 MB、大版本号）。空串合法（交由 required 把关）。
 * 0 不算正整数。
 */
export function validatePositiveInt(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  if (!/^\d+$/.test(trimmed)) return 'validation.integer'
  if (Number(trimmed) <= 0) return 'validation.positive'
  return undefined
}

/**
 * 绝对路径（工作目录/JDK 路径）。空串合法（交由 required 把关）。
 * 接受 Unix 绝对路径（`/...`）或 Windows 盘符路径（`C:\...` 或 `C:/...`），
 * 不做存在性校验（最终以 Worker 落地为准）。
 */
export function validateAbsPath(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  const unix = trimmed.startsWith('/')
  const win = /^[A-Za-z]:[\\/]/.test(trimmed)
  return unix || win ? undefined : 'validation.absPath'
}

/**
 * URL（下载地址等）。空串合法（交由 required 把关）。
 * 仅接受 http/https，避免误填非法 scheme。
 */
export function validateUrl(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  try {
    const u = new URL(trimmed)
    return u.protocol === 'http:' || u.protocol === 'https:' ? undefined : 'validation.urlScheme'
  } catch {
    return 'validation.url'
  }
}

/**
 * 环境变量引用（备份凭证等，config-files.md 要求 `${ENV_VAR}` 形式）。
 * 空串合法（选填）；非空时必须形如 `${NAME}`，NAME 由字母/数字/下划线组成且不以数字开头。
 */
export function validateEnvRef(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  return /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(trimmed) ? undefined : 'validation.envRef'
}

/** 主机名/IP（Bot 连接地址等）。空串合法（交由 required 把关）。 */
export function validateHost(value: string): FieldError {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  // 允许域名、IPv4、localhost；不强校验 IPv6（用 includes(':') 放行带端口的极端情况由端口字段单独处理）
  const hostname = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$/
  const ipv4 = /^(\d{1,3}\.){3}\d{1,3}$/
  return hostname.test(trimmed) || ipv4.test(trimmed) ? undefined : 'validation.host'
}

/** 校验规则集合：字段名 → 校验函数链（按序短路，返回首个错误）。 */
export type FieldRules<T extends string> = Partial<Record<T, Array<(v: string) => FieldError>>>

/**
 * 对一组字段值跑校验规则，返回 字段名 → 首个错误 的映射（仅含有错字段）。
 * 提交前调用：结果为空对象即可提交。
 */
export function validateFields<T extends string>(
  values: Record<T, string>,
  rules: FieldRules<T>,
): Partial<Record<T, string>> {
  const errors: Partial<Record<T, string>> = {}
  for (const key of Object.keys(rules) as T[]) {
    const chain = rules[key]
    if (!chain) continue
    for (const rule of chain) {
      const err = rule(values[key] ?? '')
      if (err) {
        errors[key] = err
        break
      }
    }
  }
  return errors
}

/** 表单是否有任一字段错误（提交按钮可据此禁用）。 */
export function hasErrors(errors: Record<string, string | undefined>): boolean {
  return Object.values(errors).some((e) => e != null)
}
