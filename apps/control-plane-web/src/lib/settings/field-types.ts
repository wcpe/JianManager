/**
 * 设置字段的**渲染类型**判定。
 *
 * 【为什么要抽出来】改造前这段逻辑是 SettingsPageView 里的一串 `item.key === 'xxx' ? … : …`
 * 硬编码：`debug.mode` 渲染 Switch、`log.level` 渲染 Select、其余一律 Input。
 * 三个问题：
 *  ① 想在别的页面复用这套「按类型渲染 + 即时校验 + 保存」时，只能复制那段 JSX；
 *  ② 新增一种字段要改组件内部，而不是"给一份字段描述"；
 *  ③ 像 `log.acq.path_overrides` 这种需要**自定义编辑器**的字段，只能再塞一个三元分支，
 *     越堆越长。
 *
 * 现在改成：字段类型由一份**声明**决定（keys → 类型），组件按类型分派渲染器。
 * 纯函数放这里，可脱离 UI 单测。
 */

/** 字段渲染类型。新增类型时同步补 FieldRenderers 映射（类型系统会强制）。 */
export type FieldType =
  /** 单行文本（默认）。 */
  | 'text'
  /** 密码/令牌：遮显 + 关闭自动填充。 */
  | 'secret'
  /** 单行文本但只收数字（移动端弹数字键盘）。 */
  | 'numeric'
  /** 二值开关（Switch），提交值仍为 "true"/"false"。 */
  | 'switch'
  /** 有限选项下拉。 */
  | 'select'
  /**
   * 键值对编辑器（一组可增删的「键 → 值」行）。
   *
   * 用于后端契约是 JSON 对象文本、但语义是"一组映射"的字段（如
   * `log.acq.path_overrides`）。这类字段绝不能让用户手写 JSON。
   */
  | 'kv-pairs'

/** 一个字段的渲染声明。 */
export interface FieldSpec {
  type: FieldType
  /** select 的候选值。 */
  options?: readonly { value: string; label: string }[]
  /** select 是否允许提交空串（用于可选枚举如 health.probe_kind）。 */
  allowEmpty?: boolean
  /** switch 的开启/关闭文案 i18n 键（缺省用 common.enabled / common.disabled）。 */
  onLabel?: string
  offLabel?: string
  /** 是否即时生效（false 时界面标注"Worker 侧生效"）。 */
  effectiveImmediately?: boolean
  /** 额外的输入提示（字面量，如 ${ENV_VAR}）。 */
  placeholder?: string
  /** 额外的输入提示 i18n 键（经 t() 翻译）。 */
  placeholderKey?: string
}

/**
 * 已知字段的渲染声明表。
 *
 * 【为什么用表而不是 if-else】表可以被别处读取（例如"哪些字段需要 Worker 侧生效标注"），
 * 而 if-else 只能活在渲染函数里。新增字段只需加一行，不用改组件。
 *
 * 未登记的 key 落回默认类型（text），所以这张表可以逐步补全，不必一次穷举。
 */
export const FIELD_SPECS: Readonly<Record<string, FieldSpec>> = {
  'debug.mode': {
    type: 'switch',
    onLabel: 'settings.debugOn',
    offLabel: 'settings.debugOff',
    effectiveImmediately: true,
  },
  'log.level': {
    type: 'select',
    options: [
      { value: 'debug', label: 'debug' },
      { value: 'info', label: 'info' },
      { value: 'warn', label: 'warn' },
      { value: 'error', label: 'error' },
    ],
    effectiveImmediately: true,
  },
  'invite.smtp.password': { type: 'secret', placeholder: '${ENV_VAR}', effectiveImmediately: true },
  'invite.smtp.port': { type: 'numeric', effectiveImmediately: true },
  'github.token': {
    type: 'secret',
    placeholderKey: 'settings.githubTokenPlaceholder',
    effectiveImmediately: true,
  },
  'backup.retention_days': { type: 'numeric', effectiveImmediately: false },

  // ── 健康巡检 / 额度 / 回收 / 快照（布尔、枚举与整数项） ──
  'health.scan_enabled': { type: 'switch', effectiveImmediately: false },
  'health.probe_kind': {
    type: 'select',
    allowEmpty: true,
    options: [
      { value: 'tcp', label: 'tcp' },
      { value: 'http', label: 'http' },
    ],
    effectiveImmediately: true,
  },
  'health.action': {
    type: 'select',
    options: [
      { value: 'warn', label: 'warn' },
      { value: 'restart', label: 'restart' },
    ],
    effectiveImmediately: true,
  },
  'health.suspicion_threshold': { type: 'numeric', effectiveImmediately: true },
  'health.circuit_breaker_threshold': { type: 'numeric', effectiveImmediately: true },
  'health.self_heal_max_restarts': { type: 'numeric', effectiveImmediately: true },
  'quota.enforce_mode': {
    type: 'select',
    options: [
      { value: 'alert', label: 'alert' },
      { value: 'throttle', label: 'throttle' },
      { value: 'stop', label: 'stop' },
    ],
    effectiveImmediately: true,
  },
  'quota.enforce_streak': { type: 'numeric', effectiveImmediately: true },
  'instance_reverse_reconcile.auto_dispose': { type: 'switch', effectiveImmediately: true },
  'bot_reclaim.auto_reclaim': { type: 'switch', effectiveImmediately: true },
  'snapshot.retention_count': { type: 'numeric', effectiveImmediately: true },
  'snapshot.retention_days': { type: 'numeric', effectiveImmediately: true },
  'snapshot.pre_rollback_keep': { type: 'numeric', effectiveImmediately: true },
  'snapshot.max_per_instance': { type: 'numeric', effectiveImmediately: true },
  'snapshot.max_total_mb': { type: 'numeric', effectiveImmediately: true },
  'crash.stat_retention_days': { type: 'numeric', effectiveImmediately: true },

  // ── 日志采集（Worker 侧心跳生效） ──
  // 这一项就是"不要让我手输 JSON"的那个字段：渲染为成对行编辑器。
  'log.acq.path_overrides': { type: 'kv-pairs', effectiveImmediately: false },
  'log.acq.rules_enabled': { type: 'switch', effectiveImmediately: false },
  'log.acq.realtime_tail': { type: 'switch', effectiveImmediately: false },
  'log.acq.gz_import_enabled': { type: 'switch', effectiveImmediately: false },
  'log.acq.gz_scan_interval': {
    type: 'text',
    effectiveImmediately: false,
    placeholderKey: 'nodeLogAcq.scanIntervalPlaceholder',
  },
}

/** 取某个字段的渲染声明（未登记时返回默认 text）。 */
export function fieldSpecOf(key: string): FieldSpec {
  return FIELD_SPECS[key] ?? { type: 'text' }
}

/**
 * 字段的生效时机。
 *
 * 【为什么是三态而不是布尔】布尔只能表达"即时 / 非即时"两种，于是未登记的字段
 * 只能二选一。上一版把未登记字段当"非即时"，界面上就给 `traffic.rate_limit.*`
 * 这类其实即时生效的配置打了「Worker 侧生效」——**标错比不标更糟**：用户会去重启
 * Worker 找一个根本不存在的开关。
 *
 * 所以未登记 spec 的字段返回 `unknown`，界面据此**不显示生效时机徽标**。
 */
export type EffectTiming = 'immediate' | 'worker' | 'unknown'

export function effectTimingOf(key: string): EffectTiming {
  const spec = FIELD_SPECS[key]
  if (!spec || spec.effectiveImmediately === undefined) return 'unknown'
  return spec.effectiveImmediately ? 'immediate' : 'worker'
}

/**
 * 该字段是否即时生效。
 *
 * 注意：`unknown` 也返回 false。这个布尔只适合"要判断是否立刻生效"的场合
 * （例如决定保存后是否提示刷新）；**要渲染"何时生效"的提示请用 `effectTimingOf`**，
 * 否则会把未知当成"Worker 侧生效"，正是上面说的标错。
 */
export function isEffectiveImmediately(key: string): boolean {
  return effectTimingOf(key) === 'immediate'
}

/**
 * 该字段是否由**自定义编辑器**接管（不是普通输入框）。
 *
 * 【为什么要这个判断】自定义编辑器的字段，其"草稿"是结构化数据而不是字符串，
 * 不能套用"值与原始值比较"的脏检查，也不能在提交前把它当普通字符串校验。
 * 调用方据此走不同的草稿通道。
 */
export function usesCustomEditor(key: string): boolean {
  const t = fieldSpecOf(key).type
  return t === 'kv-pairs'
}
