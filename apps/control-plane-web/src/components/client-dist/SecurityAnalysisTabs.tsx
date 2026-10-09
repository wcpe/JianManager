import { useClientDistIpAnalysis, useClientDistPlayerAnalysis } from '@/api/clientDistSecurity'
import { IpAnalysisTabView, PlayerAnalysisTabView } from '@/components/views/client-dist/SecurityAnalysisTabsView'

/**
 * 安全侧「IP 剖析 / 玩家名剖析」两个只读聚合 Tab（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 *
 * 展示层已回迁应用侧（`IpAnalysisTabView` / `PlayerAnalysisTabView`），此处只保留取数：
 * 两个 hook 的 `limit: 200` 与失败即报错（`retry: false`）语义仍在 hook 内，
 * 调用点（`ProtectionCenterPage`）继续按原名使用。
 */

export function IpAnalysisTab() {
  const { data, isError, isLoading } = useClientDistIpAnalysis({ limit: 200 })
  return <IpAnalysisTabView rows={data ?? []} isLoading={isLoading} isError={isError} />
}

export function PlayerAnalysisTab() {
  const { data, isError, isLoading } = useClientDistPlayerAnalysis({ limit: 200 })
  return <PlayerAnalysisTabView rows={data ?? []} isLoading={isLoading} isError={isError} />
}
