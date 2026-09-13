/* eslint-disable react-refresh/only-export-components -- 安全侧共享展示件与查询读写同文件导出（仅影响 Fast Refresh） */
import { useSearchParams } from 'react-router'
import type { SecurityLevel } from '@/api/clientDistSecurity'
import { readClientDistQuery, updateClientDistQuery, type ClientDistQueryKey } from '@/lib/client-dist-query'

/**
 * 页面 B「客户端分发运维」安全侧共享件（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，供安全侧各 Tab 复用，行为不变。
 */

/** 空值占位符。 */
export const SECURITY_EMPTY = '—'

type SecurityQueryPatch = Partial<Record<ClientDistQueryKey, string | null>>

/** 安全侧查询读写（channelId/ip/machineId/errCode/from/to 等冻结键）。 */
export function useSecurityQuery() {
  const [searchParams, setSearchParams] = useSearchParams()
  return {
    query: readClientDistQuery(searchParams),
    updateQuery: (patch: SecurityQueryPatch) => {
      setSearchParams(updateClientDistQuery(searchParams, patch), { replace: true })
    },
  }
}

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
