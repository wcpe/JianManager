/**
 * 告警 UI 纯函数助手（FR-085 / FR-149）。级别着色、通道类型展示、静默窗口格式化、
 * 触发类型字段可见性判定、级别→状态等级映射、规则汇总——抽为纯函数便于 vitest 覆盖，
 * UI 组件只做渲染。
 */
import type { StatusLevel } from '@jianmanager/ui'

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
  | 'qq'
  | 'inapp'

/**
 * QQ 通道投递目标类型：仅单聊（FR-494）。
 *
 * 群聊（group）路径**保留在后端但不对用户暴露**：QQ 平台侧拒绝群主动消息（40034105，
 * 官方维护者定性「群内主动消息平台侧不支持」），前端因此彻底收敛到单聊——既不给选项，
 * 也不承认存量 group 值（见 {@link isQQTargetType}），避免配出一条永远收不到告警的通道。
 */
export type QQTargetType = 'c2c'

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

/**
 * 告警级别 → 状态等级（FR-149 / FR-163 StatusBadge 复用）：严重=危险、警告=警告、提示=信息。
 * 配置行的级别 pill 与状态徽章统一走 StatusLevel，避免硬编码品牌色。
 */
export function levelStatusLevel(level: string): StatusLevel {
  switch (level) {
    case 'critical':
      return 'danger'
    case 'warn':
      return 'warning'
    case 'info':
    default:
      return 'info'
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

/** 触发类型对应的默认目标类型（多维度触发取「节点」为默认，用户可在对话框切换）。 */
export function targetTypeForTrigger(triggerType: string): 'node' | 'instance' {
  return triggerType === 'metric' || triggerType === 'node_offline' ? 'node' : 'instance'
}

/**
 * 该触发类型是否允许用户切换目标维度（node ↔ instance）。
 *
 * `metric` 在 node 与 instance 两个维度都有评估器（`evaluateNodeMetricRule` /
 * `evaluateInstanceMetricRule`），故两种目标都合法、可切换；`node_offline` 的判定
 * 只在节点维度（心跳来自 Worker），仍锁定 node，不提供切换。
 */
export function triggerAllowsTargetSwitch(triggerType: string): boolean {
  return triggerType === 'metric'
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

/** 通道类型为 QQ 机器人（需 appId/appSecret/targetType/targetId，FR-494）。 */
export function channelIsQQ(channelType: string): boolean {
  return channelType === 'qq'
}

/**
 * 校验 QQ 目标类型字面量：**只认** `c2c`（单聊）。
 *
 * 群聊已被平台拒绝（40034105），后端虽保留 group 分支，但前端不承认——存量通道里的
 * `group` 脏值会在这里被判非法，从而在保存前被必填校验拦下（用户必须改成单聊）。
 */
export function isQQTargetType(value: string): value is QQTargetType {
  return value === 'c2c'
}

/**
 * QQ 网关连接状态 → 状态等级（FR-495 只读观测区）。
 *
 * connected=成功、connecting=过渡（黄）、error=危险（人工介入态，重连无意义）、
 * disconnected 与未知取值一律中性：`disconnected` 在「还没配 qq 通道」时同样成立，
 * 属正常待机而非故障，染红会制造假告警。
 */
export function qqGatewayLevel(status: string | undefined): StatusLevel {
  switch (status) {
    case 'connected':
      return 'success'
    case 'connecting':
      return 'warning'
    case 'error':
      return 'danger'
    case 'disconnected':
    default:
      return 'neutral'
  }
}

/** QQ 网关状态 → i18n key 后缀。 */
export type QQGatewayStatusKey = 'connected' | 'connecting' | 'disconnected' | 'error' | 'unknown'

/**
 * 网关状态串 → i18n key 后缀，未知取值回落 `unknown`。
 *
 * 不直接把后端串拼进 key（`alerts.qqGateway_${status}`）：状态串来自 DB/内存，历史值或
 * 未来新增态会拼出缺失 key，i18next 届时把 key 本身显示给用户。收敛成枚举再查表，
 * 未知态还能显示「未知」而不是一片空白。
 */
export function qqGatewayStatusKey(status: string | undefined): QQGatewayStatusKey {
  switch (status) {
    case 'connected':
    case 'connecting':
    case 'disconnected':
    case 'error':
      return status
    default:
      return 'unknown'
  }
}

/**
 * 网关 `lastError` 里「申请一下权限就能修好」的那类，翻译成可操作提示的 i18n key；
 * 无需额外提示返回 null。
 *
 * 只认 4014（intent 无权限）：它是唯一「去开放平台申请即修复」的常见失败，而且是后端
 * `last_error` 里唯一值得复制到别处行动的诊断。后端文案是中文、不能直接塞进英文界面，
 * 故此处按内容识别再换成本地化文案。匹配同时接受关闭码 `4014`、intent 字面量 `1<<25`
 * 与官方常量名 `GROUP_AND_C2C_EVENT`——三者出现任一即认定是该故障（后端 closeErrorText
 * 与 persistSecret/ResolveCredentials 的失败文案都可能带其中之一）。
 *
 * 4914（下架）/4915（封禁）刻意不在此列：没有「自己动手就能修好」的动作，原样展示后端
 * 文案比给一句空泛的「请联系平台」更诚实。
 */
export function qqGatewayFatalHintKey(lastError: string | undefined): string | null {
  const text = (lastError ?? '').trim()
  if (text === '') return null
  if (text.includes('4014') || text.includes('1<<25') || text.includes('GROUP_AND_C2C_EVENT')) {
    return 'alerts.qqGatewayHint4014'
  }
  return null
}

/**
 * 群 openid 短码：列表里只展示前 8 位。
 *
 * `group_openid` 是平台标识（不是凭证），但完整值又长又没有辨识度；前 8 位足以在几条
 * 入口间区分并与人核对。不足 8 位原样返回——不补位、不截断成空串，避免造出假标识。
 */
export function shortGroupOpenid(openid: string): string {
  const v = (openid ?? '').trim()
  return v.length <= 8 ? v : `${v.slice(0, 8)}…`
}

/** 通道类型为站内（无外部配置）。 */
export function channelIsInApp(channelType: string): boolean {
  return channelType === 'inapp'
}

/**
 * 校验形如 ${ENV_VAR} 的环境变量引用（凭证字段强制）。
 *
 * 字符集与后端 `resolveEnvRef` 完全对齐：`${` + 首字符为字母/下划线 + 其后为
 * 字母/数字/下划线/**连字符** + `}`。连字符是必需的——QQ 扫码绑定返回的引用名形如
 * `${QQ-1020001}`（由 appId 直接拼出），旧正则不含 `-` 会把后端给的合法引用判为非法，
 * 导致扫码成功后表单永远保存不了。
 */
export function isEnvRef(value: string): boolean {
  return /^\$\{[A-Za-z_][A-Za-z0-9_-]*\}$/.test(value.trim())
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

/** 规则汇总统计（FR-149 配置行汇总条）：总数 / 启用数。 */
export interface RuleSummary {
  total: number
  enabled: number
}

/** 统计规则总数与启用数，用于列表顶部汇总 chip（FR-149）。 */
export function summarizeRules(rules: readonly { enabled: boolean }[]): RuleSummary {
  let enabled = 0
  for (const r of rules) if (r.enabled) enabled += 1
  return { total: rules.length, enabled }
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
