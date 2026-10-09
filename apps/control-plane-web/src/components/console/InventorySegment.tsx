import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { dispatchBusiness, fetchBusinessManifest, type BusinessResult } from '@/api/business'
import { parseInventoryView, type InventoryView } from './inventory-view'
import InventorySegmentView from '@/components/views/instances/InventorySegment'

/** manifest 里是否存在指定动作（按 action 精确判断，探针未连/无背包插件则降级）。 */
function hasInventoryAction(output: unknown, action: string): boolean {
  if (typeof output !== 'object' || output === null) return false
  const domains = (output as { domains?: Record<string, unknown> }).domains
  const inventory = domains?.inventory
  if (typeof inventory !== 'object' || inventory === null) return false
  const actions = (inventory as { actions?: Array<{ action?: unknown }> }).actions
  return Array.isArray(actions) && actions.some((item) => item.action === action)
}

/**
 * 背包定制页的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层做三件事：按 manifest 发现背包能力（view / writeBasicAttrs）、
 * 查询并解析背包视图、下发基础属性写。保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InventorySegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()

  // 背包能力发现：复用 business manifest，按 action 精确判断可用能力（探针未连 / 无背包插件则降级）。
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })
  const inventoryAvailable = useMemo(() => {
    return hasInventoryAction(manifestQuery.data?.output, 'view') && !!manifestQuery.data?.available
  }, [manifestQuery.data])
  const canWriteBasicAttrs = useMemo(() => {
    return hasInventoryAction(manifestQuery.data?.output, 'writeBasicAttrs') && !!manifestQuery.data?.available
  }, [manifestQuery.data])

  const [submitted, setSubmitted] = useState<string | null>(null)

  // 只读 view 动作：透传玩家 UUID，解析探针 InventoryEnvelope.encodeView 输出。
  const viewQuery = useQuery({
    queryKey: ['inventory-view', instanceId, submitted],
    queryFn: async (): Promise<InventoryView | null> => {
      const res = await dispatchBusiness(instanceId, 'inventory', 'view', JSON.stringify({ player: submitted }))
      if (!res.available) throw new Error(res.error || t('inventory.queryFailed'))
      return parseInventoryView(res.output)
    },
    enabled: submitted !== null && inventoryAvailable,
  })

  const writeBasicAttrs = useCallback(
    async (payload: {
      player: string
      dataVersion: number
      base: unknown
      next: unknown
      reason?: string
      operationId: string
    }): Promise<BusinessResult> => {
      return dispatchBusiness(
        instanceId,
        'inventory',
        'writeBasicAttrs',
        JSON.stringify({
          player: payload.player,
          base: { dataVersion: payload.dataVersion, basicAttrs: payload.base },
          edited: { dataVersion: payload.dataVersion, basicAttrs: payload.next },
        }),
        { write: true, operationId: payload.operationId, reason: payload.reason },
      )
    },
    [instanceId],
  )

  return (
    <InventorySegmentView
      manifestLoading={manifestQuery.isLoading}
      manifestRefreshing={manifestQuery.isFetching}
      inventoryAvailable={inventoryAvailable}
      canWriteBasicAttrs={canWriteBasicAttrs}
      manifestError={manifestQuery.data?.error}
      onRefreshManifest={() => void manifestQuery.refetch()}
      view={viewQuery.data ?? null}
      viewLoading={viewQuery.isFetching}
      viewError={viewQuery.isError ? ((viewQuery.error as Error)?.message || t('inventory.queryFailed')) : undefined}
      onQueryView={setSubmitted}
      onWriteBasicAttrs={writeBasicAttrs}
      onWritten={() => void viewQuery.refetch()}
    />
  )
}
