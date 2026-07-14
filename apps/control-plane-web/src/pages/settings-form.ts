/**
 * 设置表单纯函数助手（FR-158）。
 * 草稿 vs 当前值的脏数据比对——抽为纯函数便于 vitest 覆盖，供切分类未保存拦截与保存按钮态使用。
 */

/** 比对所需的最小配置项形态（键 + 当前生效值）。 */
export interface DraftDiffItem {
  key: string
  value: string
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
    default:
      return undefined
  }
}

/** 当前展示值（草稿优先）中是否存在非法项——保存按钮据此禁用，从源头阻止提交非法值。 */
export function hasInvalidDraft(items: DraftDiffItem[], draft: Record<string, string>): boolean {
  return items.some((it) => validateSettingDraft(it.key, draft[it.key] ?? it.value) !== undefined)
}
