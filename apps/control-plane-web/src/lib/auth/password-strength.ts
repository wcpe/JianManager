/**
 * 密码强度评估（FR-157）：基于长度与字符种类的轻量打分，纯函数便于测试。
 * 仅用于前端即时提示，不替代后端密码策略。
 */

/** 单条规则的命中情况：i18n key + 是否满足。 */
export interface PasswordRule {
  key: string
  met: boolean
}

/** 强度评估结果。 */
export interface PasswordStrength {
  /** 0=空 / 1=弱 / 2=中 / 3=强 / 4=很强。 */
  score: 0 | 1 | 2 | 3 | 4
  /** 强度档位 i18n key（空密码时为 undefined）。 */
  labelKey?: string
  /** 各规则命中情况，供 checklist 展示。 */
  rules: PasswordRule[]
}

const RULE_DEFS: { key: string; test: (pw: string) => boolean }[] = [
  { key: 'setup.pwRuleLength', test: (pw) => pw.length >= 8 },
  { key: 'setup.pwRuleLower', test: (pw) => /[a-z]/.test(pw) },
  { key: 'setup.pwRuleUpper', test: (pw) => /[A-Z]/.test(pw) },
  { key: 'setup.pwRuleDigit', test: (pw) => /\d/.test(pw) },
  { key: 'setup.pwRuleSymbol', test: (pw) => /[^A-Za-z0-9]/.test(pw) },
]

const SCORE_LABEL: Record<number, string | undefined> = {
  0: undefined,
  1: 'setup.pwWeak',
  2: 'setup.pwFair',
  3: 'setup.pwGood',
  4: 'setup.pwStrong',
}

/** 评估密码强度：长度 ≥8 为硬门槛，未达标最多算「弱」；其余按命中规则数升档。 */
export function passwordStrength(pw: string): PasswordStrength {
  const rules = RULE_DEFS.map((r) => ({ key: r.key, met: r.test(pw) }))
  if (pw.length === 0) return { score: 0, rules }
  const met = rules.filter((r) => r.met).length
  let score: 0 | 1 | 2 | 3 | 4
  if (pw.length < 8) score = 1
  else if (met <= 2) score = 2
  else if (met === 3) score = 3
  else score = 4
  return { score, labelKey: SCORE_LABEL[score], rules }
}
