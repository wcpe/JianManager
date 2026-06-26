import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Backpack, Heart, Drumstick, Sparkles, Gamepad2, Loader2, RefreshCw, Search, WifiOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { StatCard } from '@/components/ui/stat-card'
import { StatusBadge } from '@/components/ui/status-badge'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'
import { dispatchBusiness, fetchBusinessManifest } from '@/api/business'
import {
  parseInventoryView,
  buildSlotGrid,
  INVENTORY_SLOTS,
  ENDER_CHEST_SLOTS,
  SLOTS_PER_ROW,
  type InventoryView,
  type RawItemSlot,
} from './inventory-view'

/**
 * 背包定制页（JBIS，FR-127，见 JM ADR-026 + ServerProbe ADR-0016）。
 *
 * 玩家背包快照查看：经 FR-125 探针背包 Provider 的只读 `inventory.view` 动作（dispatchBusiness 透传）取结构化视图，
 * 解析为格子视图（背包格子）按槽位渲染（设计 §3「背包格子」）。区别于经济页：背包域无金额、以 nbtBase64 为写真源。
 * 远程发/收物品（写动作）须二次确认，属 FR-127 后续；本段聚焦快照查看 + 物品清单展示 + 离线态如实呈现（不谎报）。
 * 背包能力不可用时优雅降级（复用 business manifest 发现 inventory 域）。视觉为靛蓝圆角范式（FR-163）。
 */
interface InventorySegmentProps {
  /** 实例 ID（业务命令 POST /instances/:id/business 需要）。 */
  instanceId: number
}

export default function InventorySegment({ instanceId }: InventorySegmentProps) {
  const { t } = useTranslation()

  // 背包能力发现：复用 business manifest，检查是否存在 inventory 域（探针未连 / 无背包插件则降级）。
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })
  const inventoryAvailable = useMemo(() => {
    const m = manifestQuery.data
    return !!m?.available && !!m.output?.domains?.inventory
  }, [manifestQuery.data])

  const [player, setPlayer] = useState('')
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

  const view = viewQuery.data ?? null
  const runQuery = useCallback(() => {
    const p = player.trim()
    if (p !== '') setSubmitted(p)
  }, [player])

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 p-4">
      <div className="flex items-center gap-2.5">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-accent text-primary">
          <Backpack className="size-4" />
        </span>
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{t('inventory.title')}</h3>
          <p className="truncate text-xs text-muted-foreground">{t('inventory.subtitle')}</p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 rounded-full px-3 text-xs"
          onClick={() => void manifestQuery.refetch()}
          disabled={manifestQuery.isFetching}
        >
          <RefreshCw className={cn('mr-1 size-3.5', manifestQuery.isFetching && 'animate-spin')} />
          {t('inventory.refresh')}
        </Button>
      </div>

      {manifestQuery.isLoading && (
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          {t('common.loading')}
        </div>
      )}

      {!manifestQuery.isLoading && !inventoryAvailable && (
        <div className="rounded-xl border bg-muted/30 p-4 text-xs text-muted-foreground shadow-soft">
          {manifestQuery.data?.error || t('inventory.unavailable')}
        </div>
      )}

      {!manifestQuery.isLoading && inventoryAvailable && (
        <div className="flex min-h-0 flex-1 flex-col gap-3">
          <p className="text-xs text-muted-foreground">{t('inventory.viewHint')}</p>
          <div className="flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-0.5 text-xs">
              <span className="text-muted-foreground">{t('inventory.player')}</span>
              <Input
                value={player}
                onChange={(e) => setPlayer(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') runQuery()
                }}
                placeholder={t('inventory.playerPlaceholder')}
                className="h-8 w-72 text-xs"
              />
            </label>
            <Button size="sm" className="h-8 rounded-full px-4 text-xs" onClick={runQuery} disabled={viewQuery.isFetching}>
              {viewQuery.isFetching ? (
                <Loader2 className="mr-1 size-3.5 animate-spin" />
              ) : (
                <Search className="mr-1 size-3.5" />
              )}
              {t('inventory.query')}
            </Button>
          </div>

          {viewQuery.isError && (
            <p className="text-xs text-status-danger">
              {(viewQuery.error as Error)?.message || t('inventory.queryFailed')}
            </p>
          )}

          {/* 玩家无落盘数据：exists=false（与空背包严格区分，不谎报） */}
          {view && !view.exists && (
            <div className="rounded-xl border bg-muted/30 p-4 text-xs text-muted-foreground shadow-soft">
              {t('inventory.notFound', { player: view.player })}
            </div>
          )}

          {view && view.exists && <InventoryDetail view={view} />}
        </div>
      )}
    </div>
  )
}

/** 背包详情：在线态 + 基础属性卡 + 背包/末影箱格子视图。 */
function InventoryDetail({ view }: { view: InventoryView }) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto">
      {/* 在线态：在线即时生效；离线写下次登录生效（如实呈现，FR-126/127） */}
      <div className="flex items-center gap-2 text-xs">
        {view.online ? (
          <StatusBadge level="success" label={t('inventory.online')} dot pulse />
        ) : (
          <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-muted-foreground">
            <WifiOff className="size-3" aria-hidden />
            {t('inventory.offline')}
          </span>
        )}
        {view.dataVersion > 0 && (
          <span className="text-muted-foreground">
            {t('inventory.dataVersion')}: <span className="tabular-nums">{view.dataVersion}</span>
          </span>
        )}
      </div>

      {/* 基础属性 KPI 卡 */}
      {view.basicAttrs && (
        <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
          <StatCard
            icon={<Heart className="size-3.5" />}
            tone="danger"
            label={t('inventory.health')}
            value={view.basicAttrs.health}
          />
          <StatCard
            icon={<Drumstick className="size-3.5" />}
            tone="warning"
            label={t('inventory.foodLevel')}
            value={view.basicAttrs.foodLevel}
          />
          <StatCard
            icon={<Sparkles className="size-3.5" />}
            tone="info"
            label={t('inventory.xpLevel')}
            value={view.basicAttrs.xpLevel}
          />
          <StatCard
            icon={<Gamepad2 className="size-3.5" />}
            label={t('inventory.gameMode')}
            value={<span className="text-base">{view.basicAttrs.gameMode || '—'}</span>}
          />
        </div>
      )}

      {/* 背包 / 末影箱格子 */}
      <Tabs defaultValue="inventory" className="flex flex-col gap-3">
        <TabsList className="self-start rounded-full">
          <TabsTrigger value="inventory" className="rounded-full text-xs">
            {t('inventory.tabInventory')}
            <span className="ml-1.5 tabular-nums text-muted-foreground">{view.inventory.length}</span>
          </TabsTrigger>
          <TabsTrigger value="ender" className="rounded-full text-xs">
            {t('inventory.tabEnderChest')}
            <span className="ml-1.5 tabular-nums text-muted-foreground">{view.enderChest.length}</span>
          </TabsTrigger>
        </TabsList>
        <TabsContent value="inventory">
          <SlotGrid items={view.inventory} size={INVENTORY_SLOTS} />
        </TabsContent>
        <TabsContent value="ender">
          <SlotGrid items={view.enderChest} size={ENDER_CHEST_SLOTS} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

/** 物品格子网格（背包格子）：定长槽位、每行 9 格、空槽虚线占位、有物品显示材质简称 + 数量角标 + hover 详情。 */
function SlotGrid({ items, size }: { items: RawItemSlot[]; size: number }) {
  const { t } = useTranslation()
  const grid = useMemo(() => buildSlotGrid(items, size), [items, size])
  if (items.length === 0) {
    return <p className="text-xs text-muted-foreground">{t('inventory.empty')}</p>
  }
  return (
    <div
      className="grid w-fit gap-1 rounded-xl border bg-card p-2 shadow-soft"
      style={{ gridTemplateColumns: `repeat(${SLOTS_PER_ROW}, minmax(0, 1fr))` }}
    >
      {grid.map((cell, idx) => (
        <Slot key={idx} cell={cell} slot={idx} />
      ))}
    </div>
  )
}

/** 单格：空槽为虚线占位；有物品显示材质简称 + 数量角标，title 给完整信息（material / displayName / 数量 / 槽位）。 */
function Slot({ cell, slot }: { cell: RawItemSlot | null; slot: number }) {
  const { t } = useTranslation()
  if (!cell) {
    return <div className="size-12 rounded-md border border-dashed border-border/60 bg-muted/20" aria-hidden />
  }
  const title = [
    cell.displayName || cell.material,
    `${t('inventory.amount')}: ${cell.amount}`,
    `${t('inventory.slot')}: ${slot}`,
  ].join('\n')
  return (
    <div
      title={title}
      className={cn(
        'relative flex size-12 flex-col items-center justify-center overflow-hidden rounded-md border bg-accent/40 p-0.5',
        'transition-transform duration-200 ease-ios hover:-translate-y-0.5 hover:bg-accent',
      )}
    >
      <span className="line-clamp-2 break-all text-center text-[9px] font-medium leading-tight text-foreground">
        {materialShort(cell.material)}
      </span>
      {cell.amount > 1 && (
        <span className="absolute bottom-0 right-0.5 text-[10px] font-semibold tabular-nums text-primary">
          {cell.amount}
        </span>
      )}
    </div>
  )
}

/** 材质短名：去掉命名空间前缀 + 取末段，便于格子内显示（如 minecraft:diamond_sword → DIAMOND_SWORD）。 */
function materialShort(material: string): string {
  if (!material) return '?'
  const noNs = material.includes(':') ? material.slice(material.lastIndexOf(':') + 1) : material
  return noNs.toUpperCase()
}
