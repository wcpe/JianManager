import { useState } from 'react'
import { SecurityProfilesTabView } from '@/components/views/client-dist/SecurityProfilesTabView'
import type { ProfileMaskers } from '@/components/views/client-dist/SecurityProfilesTabView'
import { maskInstallId, maskMachineId, maskPlayerName } from '@/lib/shared/privacy-mask'
import { useClientDistSecurityProfile, useClientDistSecurityProfiles } from '@/api/clientDistSecurity'
import { useSecurityQuery } from './security-shared'

/**
 * 安全侧「客户端画像」Tab + 详情弹窗（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 *
 * 展示层已回迁应用侧（`SecurityProfilesTabView`），此处只保留取数与查询读写：
 * 列表（`useClientDistSecurityProfiles`，`playerName` 为空时不带该筛选）与详情
 * （`useClientDistSecurityProfile`，按视图上报的 id 取）两个 hook、路由查询串读写
 * （`useSecurityQuery`）、以及脱敏策略（`privacy-mask`，经 `maskers` 注入视图）都在这里。
 * 详情弹窗开合真源在视图，容器只镜像 id 以驱动取数。
 */

/** 脱敏函数（FR-360 应用侧隐私策略）常量注入，避免每次渲染新建对象。 */
const MASKERS: ProfileMaskers = {
  playerName: maskPlayerName,
  machineId: maskMachineId,
  installId: maskInstallId,
}

export function ProfilesTab() {
  const { query, updateQuery } = useSecurityQuery()
  const [playerName, setPlayerName] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data, isError, isLoading } = useClientDistSecurityProfiles({
    playerName: playerName || undefined,
    channelId: query.channelId,
    machineId: query.machineId,
    ip: query.ip,
    limit: 200,
  })
  const detail = useClientDistSecurityProfile(detailId)

  return (
    <SecurityProfilesTabView
      profiles={data ?? []}
      isLoading={isLoading}
      isError={isError}
      query={query}
      onQueryChange={(patch) => updateQuery(patch)}
      playerName={playerName}
      onPlayerNameChange={setPlayerName}
      maskers={MASKERS}
      detail={detail.data}
      detailLoading={detail.isLoading}
      detailError={detail.isError}
      onDetailChange={setDetailId}
    />
  )
}
