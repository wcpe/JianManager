import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { SecurityActionsTabView } from '@/components/views/client-dist/SecurityActionsTabView'
import type {
  BlockIPRequest,
  ClientProtectionAction,
  ProtectionActionStatus,
  SecurityTargetType,
  SetChannelProtectionRequest,
  SetKeyStateRequest,
} from '@/lib/client-dist-security-contracts'
import {
  useBlockClientDistIP,
  useCancelClientDistIPBlock,
  useClearClientDistChannelProtection,
  useClientChannelSecuritySummary,
  useClientDistSecurityActions,
  useClientDistSecurityEvents,
  useSetClientDistChannelProtection,
  useSetClientDistKeyState,
} from '@/api/clientDistSecurity'
import { useClientChannel, useClientChannels } from '@/api/clientChannels'
import { useSecurityQuery } from './security-shared'

/**
 * 安全侧「封禁与降级」：顶部动作按钮 + 三模态 + 全宽动作流水（处置入口统一，候选可下拉）。
 *
 * 展示层已回迁应用侧（`SecurityActionsTabView`），此处只保留取数、候选汇总与五个写 mutation：
 * 处置流水与三个筛选条件（触发重新取数）、四个候选来源（频道列表 / IP 建议 /
 * 密钥列表 / 频道安全摘要）都在容器侧；两个模态的频道选择由视图上报后镜像，仅用于取候选。
 * 成功/失败 toast 与在途状态由容器提供，模态关闭时机由视图按 onXxx 返回值处理。
 */

export function ActionsTab() {
  const { t } = useTranslation()
  const { query } = useSecurityQuery()
  const [filterTarget, setFilterTarget] = useState<'' | SecurityTargetType>('')
  const [filterStatus, setFilterStatus] = useState<'' | ProtectionActionStatus>('')
  const [keyword, setKeyword] = useState('')

  // 两个模态的频道选择：真源在视图（表单草稿），此处只镜像以驱动候选取数。
  const [keyChannelId, setKeyChannelId] = useState('')
  const [protectionChannelId, setProtectionChannelId] = useState('')

  const { data, isError, isLoading } = useClientDistSecurityActions({
    limit: 200,
    targetType: filterTarget || undefined,
    status: filterStatus || undefined,
    q: keyword.trim() || undefined,
    channelId: query.channelId || undefined,
  })

  const channels = useClientChannels().data ?? []
  const channelDetail = useClientChannel(keyChannelId || null)
  const protectionSummary = useClientChannelSecuritySummary(protectionChannelId)

  // 近窗事件 + 动作流水中出现过的 IP，供封禁模态下拉候选（可手输）。
  const events = useClientDistSecurityEvents({ limit: 50 })
  const recentActions = useClientDistSecurityActions({ limit: 100 })
  const ipSuggestions = useMemo(() => {
    const set = new Set<string>()
    for (const e of events.data ?? []) if (e.ip) set.add(e.ip)
    for (const a of recentActions.data ?? []) if (a.targetType === 'ip' && a.targetValue) set.add(a.targetValue)
    return [...set]
  }, [events.data, recentActions.data])

  const blockIP = useBlockClientDistIP()
  const setKeyState = useSetClientDistKeyState()
  const setProtection = useSetClientDistChannelProtection()
  const clearProtection = useClearClientDistChannelProtection()
  const cancel = useCancelClientDistIPBlock()

  const blockIp = useCallback(
    async (body: BlockIPRequest): Promise<boolean> => {
      try {
        await blockIP.mutateAsync(body)
        toast.success(t('clientDistOps.actions.toastIpBlocked'))
        return true
      } catch {
        toast.error(t('clientDistOps.actions.toastIpBlockFailed'))
        return false
      }
    },
    [blockIP, t],
  )

  const keyState = useCallback(
    async (keyId: string, body: SetKeyStateRequest): Promise<boolean> => {
      try {
        await setKeyState.mutateAsync({ keyId, body })
        toast.success(t('clientDistOps.actions.toastKeyOk'))
        return true
      } catch {
        toast.error(t('clientDistOps.actions.toastKeyFailed'))
        return false
      }
    },
    [setKeyState, t],
  )

  const protection = useCallback(
    async (channelId: string, body: SetChannelProtectionRequest): Promise<boolean> => {
      try {
        await setProtection.mutateAsync({ channelId, body })
        toast.success(t('clientDistOps.actions.toastProtOk'))
        return true
      } catch {
        toast.error(t('clientDistOps.actions.toastProtFailed'))
        return false
      }
    },
    [setProtection, t],
  )

  // 清除保护不关闭模态，返回值仅用于契约一致性（视图不据此改开合）。
  const clearChannelProtection = useCallback(
    async (channelId: string): Promise<boolean> => {
      try {
        await clearProtection.mutateAsync(channelId)
        toast.success(t('clientDistOps.actions.toastProtCleared'))
        return true
      } catch {
        toast.error(t('clientDistOps.actions.toastProtClearFailed'))
        return false
      }
    },
    [clearProtection, t],
  )

  const unblock = useCallback(
    async (action: ClientProtectionAction): Promise<boolean> => {
      try {
        await cancel.mutateAsync(action.id)
        toast.success(t('clientDistOps.actions.toastUnblocked'))
        return true
      } catch {
        toast.error(t('clientDistOps.actions.toastUnblockFailed'))
        return false
      }
    },
    [cancel, t],
  )

  return (
    <SecurityActionsTabView
      actions={data ?? []}
      isLoading={isLoading}
      isError={isError}
      filterTarget={filterTarget}
      onFilterTargetChange={setFilterTarget}
      filterStatus={filterStatus}
      onFilterStatusChange={setFilterStatus}
      keyword={keyword}
      onKeywordChange={setKeyword}
      defaultChannelId={query.channelId}
      channels={channels}
      ipSuggestions={ipSuggestions}
      onKeyChannelChange={setKeyChannelId}
      keys={channelDetail.data?.keys ?? []}
      onProtectionChannelChange={setProtectionChannelId}
      protectionSummary={protectionSummary.data}
      blockIpPending={blockIP.isPending}
      onBlockIp={blockIp}
      keyStatePending={setKeyState.isPending}
      onSetKeyState={keyState}
      protectionPending={setProtection.isPending}
      onSetProtection={protection}
      clearProtectionPending={clearProtection.isPending}
      onClearProtection={clearChannelProtection}
      unblockPending={cancel.isPending}
      onUnblock={unblock}
    />
  )
}
