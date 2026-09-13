import { Navigate, useSearchParams } from 'react-router'
import { buildClientDistHref } from '@/lib/client-dist-query'
import { normalizeOpsTab, type OpsSource } from '@/lib/client-dist-ops-tab'

/**
 * 旧分发路由（`/client-dist-security` · `/client-dist-monitor`）→ `/client-dist-ops`
 * 的参数翻译重定向（FR-430 / ADR-088）。
 *
 * - 读原始 `tab` → {@link normalizeOpsTab} 归一化为新 7 Tab（+ 派生 `seg`/`type`）。
 * - 用 `buildClientDistHref` **原样透传**冻结 query（`channelId`/`ip`/`machineId`/
 *   `errCode`/`version`/`from`/`to`）并写入 canonical `tab`（及必要的 `seg`/`type`）。
 * - `<Navigate replace />` 不堆历史；该路由**不包**权限守卫（仅做跳转，由目标路由守卫）。
 */
export default function ClientDistRedirect({ source }: { source: OpsSource }) {
  const [searchParams] = useSearchParams()
  const { tab, seg, type } = normalizeOpsTab(searchParams.get('tab'), source)
  const target = buildClientDistHref('/client-dist-ops', searchParams, { tab, seg, type })
  return <Navigate to={target} replace />
}
