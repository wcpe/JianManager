/* eslint-disable react-refresh/only-export-components -- 安全侧共享展示件与查询读写同文件导出（仅影响 Fast Refresh） */
import { useSearchParams } from 'react-router'
import { readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist/client-dist-query'
import type { ClientDistQueryKey } from '@/lib/client-dist/client-dist-query'

/**
 * 页面 B「客户端分发运维」安全侧共享件（FR-430 / ADR-088）。
 *
 * 展示工具（占位符/时间与字节格式化/徽标变体/空态）已回迁应用侧，
 * 此处保留依赖 router 的查询读写 hook 并转发展示工具，调用点无需改动。
 */
export { SECURITY_EMPTY, fmtTime, fmtBytes, levelVariant, statusVariant, EmptyState } from '@/components/views/client-dist/security-format'

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
