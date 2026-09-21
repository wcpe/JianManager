/**
 * 设置表单纯函数助手（FR-158）。
 * 草稿 vs 当前值的脏数据比对——抽为纯函数便于 vitest 覆盖，供切分类未保存拦截与保存按钮态使用。
 */

/** 比对所需的最小配置项形态（键 + 当前生效值）。 */
export interface DraftDiffItem {
  key: string
  value: string
}

/** 设置分类：appearance 为客户端偏好，其余为平台配置。 */
export type SettingCategory = 'appearance' | 'logging' | 'runtime' | 'network' | 'backup' | 'email' | 'security'

/**
 * MC 直探（SLP/Query）超时上界，毫秒。
 *
 * 与后端共享契约 `internal/platform/directprobe.MaxTimeout` 同值，且**不得独立改动**：
 * 该上界是由「单拍采集预算 < 心跳节拍(30s)」反推出来的（余量 1s + 探针 5s + slp + query ≤ 30s − 4s
 * → slp + query ≤ 20s → 单来源 ≤ 10s）。前端只做「先拦」，后端 validateSettingValue 仍会 422 兜底；
 * 若两端漂移，运营会在前端放行、后端报错（或反之），体验与护栏都会失真。
 */
export const MAX_DIRECT_PROBE_TIMEOUT_MS = 10_000

/** 把平台配置键映射到分类：可编辑项落 logging/runtime/network/backup/email，只读项落 security。 */
export function keyCategory(key: string): SettingCategory {
  if (key.startsWith('log.') || key.startsWith('debug.')) return 'logging'
  if (key.startsWith('jdk.') || key.startsWith('graceful_stop.') || key.startsWith('direct_probe.')) return 'runtime'
  if (key.startsWith('proxy.') || key === 'github.token') return 'network'
  if (key.startsWith('backup.')) return 'backup'
  if (key === 'platform.public_base_url' || key.startsWith('invite.')) return 'email'
  return 'security'
}

/**
 * 计算草稿相对当前值的有效改动集（仅含真正不同的键）。
 * 草稿里等于当前值、未定义、或不在可编辑项集合内的键一律剔除。
 */
export function diffSettings(
  items: DraftDiffItem[],
  draft: Record<string, string>,
): Record<string, string> {
  const changed: Record<string, string> = {}
  for (const it of items) {
    const v = draft[it.key]
    if (v !== undefined && v !== it.value) changed[it.key] = v
  }
  return changed
}

/** 是否存在未保存改动（切分类前据此决定是否拦截）。 */
export function hasUnsavedChanges(items: DraftDiffItem[], draft: Record<string, string>): boolean {
  return Object.keys(diffSettings(items, draft)).length > 0
}

/**
 * 把 Go duration 文本折算为毫秒，仅用于上界校验；无任何可识别片段时返回 undefined。
 * 与 validateSettingDraft 的 duration 正则同源（先后顺序保证 ms/m/s 不被前缀吞掉）。
 */
export function durationToMillis(value: string): number | undefined {
  const unitFactor: Record<string, number> = {
    ns: 1e-6,
    us: 1e-3,
    'µs': 1e-3,
    'μs': 1e-3,
    ms: 1,
    s: 1000,
    m: 60_000,
    h: 3_600_000,
  }
  const re = /([0-9]+(?:\.[0-9]+)?)(ns|us|µs|μs|ms|s|m|h)/g
  let total = 0
  let matched = false
  let m: RegExpExecArray | null
  while ((m = re.exec(value)) !== null) {
    const factor = unitFactor[m[2]]
    if (factor === undefined) return undefined
    total += Number(m[1]) * factor
    matched = true
  }
  return matched ? total : undefined
}

/**
 * 按键校验草稿值，返回 i18n key 或 undefined（合法）。
 * 规则与后端 service/settings.go validateSettingValue 一致（前端先拦、后端 422 兜底），
 * 两侧规则如有变更须同步，避免前后端漂移。
 */
export function validateSettingDraft(key: string, value: string): string | undefined {
  const v = value.trim()
  switch (key) {
    case 'graceful_stop.timeout': {
      // Go duration，支持多段组合（如 1h30m）；须为正（全零如 0s 拒绝）。
      if (!/^([0-9]+(\.[0-9]+)?(ns|us|µs|μs|ms|s|m|h))+$/.test(v)) return 'settings.invalidDuration'
      const digits = v.replace(/ns|us|µs|μs|ms|s|m|h/g, ' ')
      return /[1-9]/.test(digits) ? undefined : 'settings.invalidDuration'
    }
    case 'direct_probe.slp_timeout':
    case 'direct_probe.query_timeout': {
      // 同 graceful_stop.timeout 的 Go duration 规则；后端另限上界（由心跳节拍护栏反推，见
      // MAX_DIRECT_PROBE_TIMEOUT_MS），超出会拖慢心跳采集。
      if (!/^([0-9]+(\.[0-9]+)?(ns|us|µs|μs|ms|s|m|h))+$/.test(v)) return 'settings.invalidDuration'
      const digits = v.replace(/ns|us|µs|μs|ms|s|m|h/g, ' ')
      if (!/[1-9]/.test(digits)) return 'settings.invalidDuration'
      const ms = durationToMillis(v)
      return ms !== undefined && ms > MAX_DIRECT_PROBE_TIMEOUT_MS ? 'settings.invalidDirectProbeTimeout' : undefined
    }
    case 'backup.retention_days':
      return /^\d+$/.test(v) ? undefined : 'settings.invalidNonNegativeInt'
    case 'jdk.mirror.temurin':
    case 'jdk.mirror.corretto':
    case 'jdk.mirror.zulu':
    case 'runtime.mirror.nodejs':
      return v === '' ? 'validation.required' : undefined
    case 'proxy.url': {
      if (v === '') return undefined // 空=清除代理覆盖，合法
      try {
        const u = new URL(v)
        return ['http:', 'https:', 'socks5:', 'socks5h:'].includes(u.protocol)
          ? undefined
          : 'settings.invalidProxyUrl'
      } catch {
        return 'settings.invalidProxyUrl'
      }
    }
    case 'platform.public_base_url': {
      if (v === '') return undefined
      try {
        const u = new URL(v)
        return ['http:', 'https:'].includes(u.protocol) && u.host !== '' && u.username === '' && u.password === '' && u.search === '' && u.hash === ''
          ? undefined
          : 'settings.invalidPlatformPublicBaseUrl'
      } catch {
        return 'settings.invalidPlatformPublicBaseUrl'
      }
    }
    case 'invite.smtp.port': {
      if (v === '') return undefined
      const port = Number(v)
      return /^\d+$/.test(v) && port >= 1 && port <= 65535
        ? undefined
        : 'settings.invalidInviteSmtpPort'
    }
    case 'invite.smtp.password':
      if (v === '' || v === '(已配置)') return undefined
      return /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(v)
        ? undefined
        : 'settings.invalidInviteSmtpPassword'
    default:
      return undefined
  }
}

/** 当前展示值（草稿优先）中是否存在非法项——保存按钮据此禁用，从源头阻止提交非法值。 */
export function hasInvalidDraft(items: DraftDiffItem[], draft: Record<string, string>): boolean {
  return items.some((it) => validateSettingDraft(it.key, draft[it.key] ?? it.value) !== undefined)
}
