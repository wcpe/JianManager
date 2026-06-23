/**
 * 告警 UI 纯函数助手（FR-085）。级别着色、通道类型展示、静默窗口格式化、
 * 触发类型字段可见性判定——抽为纯函数便于 vitest 覆盖，UI 组件只做渲染。
 */

/** 告警级别。 */
export type AlertLevel = 'info' | 'warn' | 'critical'

/** 触发类型。 */
export type TriggerType =
  | 'metric'
  | 'instance_crash'
  | 'node_offline'
  | 'log_keyword'
  | 'player_event'
  | 'backup_failed'

/** 通道类型。 */
export type ChannelType =
  | 'webhook'
  | 'email'
  | 'dingtalk'
  | 'wecom'
  | 'feishu'
  | 'discord'
  | 'telegram'
  | 'inapp'

/** 级别对应的 Tailwind 着色类（徽章背景 + 文字）。 */
export function levelBadgeClass(level: string): string {
  switch (level) {
    case 'critical':
      return 'bg-destructive/15 text-destructive'
    case 'warn':
      return 'bg-amber-500/15 text-amber-600 dark:text-amber-400'
    case 'info':
    default:
      return 'bg-sky-500/15 text-sky-600 dark:text-sky-400'
  }
}

/** 触发类型需要展示「指标/运算符/阈值/持续」字段。 */
export function triggerUsesMetric(triggerType: string): boolean {
  return triggerType === 'metric'
}

/** 触发类型需要展示「关键字」字段。 */
export function triggerUsesKeyword(triggerType: string): boolean {
  return triggerType === 'log_keyword'
}

/** 触发类型需要展示「玩家事件子类型」字段。 */
export function triggerUsesEventMatch(triggerType: string): boolean {
  return triggerType === 'player_event'
}

/** 通道类型需要「URL」配置字段（webhook 及各 IM 群机器人）。 */
export function channelUsesURL(channelType: string): boolean {
  return (
    channelType === 'webhook' ||
    channelType === 'dingtalk' ||
    channelType === 'wecom' ||
    channelType === 'feishu' ||
    channelType === 'discord'
  )
}

/** 通道类型为 Telegram（需 token + chatId）。 */
export function channelIsTelegram(channelType: string): boolean {
  return channelType === 'telegram'
}

/** 通道类型为邮件（SMTP 字段集）。 */
export function channelIsEmail(channelType: string): boolean {
  return channelType === 'email'
}

/** 通道类型为站内（无外部配置）。 */
export function channelIsInApp(channelType: string): boolean {
  return channelType === 'inapp'
}

/** 校验形如 ${ENV_VAR} 的环境变量引用（凭证字段强制）。 */
export function isEnvRef(value: string): boolean {
  return /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(value.trim())
}

/**
 * 格式化静默窗口为可读区间；任一端为空返回空串（表示未设静默）。
 * 跨午夜（start > end）追加「(次日)」提示。
 */
export function formatSilenceWindow(start: string, end: string): string {
  const s = start.trim()
  const e = end.trim()
  if (!s || !e) return ''
  const crossMidnight = s > e
  return crossMidnight ? `${s} → ${e}(次日)` : `${s} → ${e}`
}

/** 校验 "HH:MM" 24 小时格式（空串视为合法——未设静默）。 */
export function isValidHHMM(value: string): boolean {
  const v = value.trim()
  if (v === '') return true
  return /^([01]\d|2[0-3]):[0-5]\d$/.test(v)
}

/** 解析后端返回的 channelIds JSON 串（"[1,2]"）为数组，容错为空数组。 */
export function parseChannelIds(raw: string | null | undefined): number[] {
  if (!raw) return []
  try {
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr.filter((x) => typeof x === 'number') : []
  } catch {
    return []
  }
}
