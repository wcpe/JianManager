import type { SecurityLevel } from '@/lib/client-dist-security-contracts'

/**
 * 客户端分发安全侧的展示工具（FR-430 / ADR-088）。
 * 从应用侧 `security-shared` 原样提取的纯展示部分（查询读写 hook 仍留应用侧）。
 */

/** 空值占位符。 */
export const SECURITY_EMPTY = '—'

/** ISO 时间 → 本地化字符串（空值返回占位符，非法值原样返回）。 */
export function fmtTime(iso?: string | null): string {
  if (!iso) return SECURITY_EMPTY
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** 字节数 → 人类可读（KiB/MiB/GiB）。 */
export function fmtBytes(bytes?: number): string {
  const b = Number(bytes ?? 0)
  if (!Number.isFinite(b) || b <= 0) return '0 B'
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(1)} GiB`
  if (b >= 1024 ** 2) return `${(b / 1024 ** 2).toFixed(1)} MiB`
  if (b >= 1024) return `${(b / 1024).toFixed(1)} KiB`
  return `${b} B`
}

/** 风险等级 → 徽标变体。 */
export function levelVariant(level?: SecurityLevel): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (level === 'critical' || level === 'high') return 'destructive'
  if (level === 'warn') return 'default'
  return 'secondary'
}

/** 处置动作状态 → 徽标变体。 */
export function statusVariant(status?: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'active' || status === 'suspended' || status === 'revoked') return 'destructive'
  if (status === 'throttled' || status === 'observe') return 'default'
  if (status === 'canceled' || status === 'expired') return 'outline'
  return 'secondary'
}

/** 居中空态文案。 */
export function EmptyState({ text }: { text: string }) {
  return <p className="py-10 text-center text-sm text-muted-foreground">{text}</p>
}
