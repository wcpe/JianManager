import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Backpack, Heart, Drumstick, Sparkles, Gamepad2, Loader2, RefreshCw, Search, ShieldAlert, WifiOff } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@jianmanager/ui/components/tabs'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/DangerConfirm'
import type { BusinessResult } from '@/api/business'
import { dispatchBusiness, fetchBusinessManifest } from '@/api/business'
import {
  parseInventoryView,
  buildSlotGrid,
  INVENTORY_SLOTS,
  ENDER_CHEST_SLOTS,
  SLOTS_PER_ROW,
  type BasicAttrs,
  type InventoryView,
  type RawItemSlot,
} from './inventory-view'

/**
 * 背包定制页（JBIS，FR-127，见 JM ADR-026 + ServerProbe ADR-0016）。
 *
 * 玩家背包快照查看：经 FR-125 探针背包 Provider 的只读 `inventory.view` 动作（dispatchBusiness 透传）取结构化视图，
 * 解析为格子视图（背包格子）按槽位渲染（设计 §3「背包格子」）。区别于经济页：背包域无金额、以 nbtBase64 为写真源。
 * 本轮只开放基础属性写：物品发放/回收等结构化写仍待 AllinInventorySync 导出可消费门面。
 * 背包能力不可用时优雅降级（复用 business manifest 发现 inventory 域）。视觉为靛蓝圆角范式（FR-163）。
 */
interface InventorySegmentProps {
  /** 实例 ID（业务命令 POST /instances/:id/business 需要）。 */
  instanceId: number
}

export default function InventorySegment({ instanceId }: InventorySegmentProps) {
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

          {view && view.exists && (
            <InventoryDetail
              instanceId={instanceId}
              view={view}
              canWriteBasicAttrs={canWriteBasicAttrs}
              onWritten={() => void viewQuery.refetch()}
            />
          )}
        </div>
      )}
    </div>
  )
}

function hasInventoryAction(output: unknown, action: string): boolean {
  if (typeof output !== 'object' || output === null) return false
  const domains = (output as { domains?: Record<string, unknown> }).domains
  const inventory = domains?.inventory
  if (typeof inventory !== 'object' || inventory === null) return false
  const actions = (inventory as { actions?: Array<{ action?: unknown }> }).actions
  return Array.isArray(actions) && actions.some((item) => item.action === action)
}

/** 背包详情：在线态 + 基础属性卡 + 背包/末影箱格子视图。 */
function InventoryDetail({
  instanceId,
  view,
  canWriteBasicAttrs,
  onWritten,
}: {
  instanceId: number
  view: InventoryView
  canWriteBasicAttrs: boolean
  onWritten: () => void
}) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
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
        {view.basicAttrs && canWriteBasicAttrs && (
          <Button
            size="sm"
            variant="outline"
            className="ml-auto h-7 rounded-full px-3 text-xs"
            onClick={() => setEditing(true)}
          >
            <ShieldAlert className="mr-1 size-3.5" />
            {t('inventory.editBasicAttrs')}
          </Button>
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

      {view.basicAttrs && canWriteBasicAttrs && (
        <BasicAttrsDialog
          open={editing}
          instanceId={instanceId}
          view={view}
          onOpenChange={setEditing}
          onWritten={onWritten}
        />
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

interface PendingBasicAttrsWrite {
  next: BasicAttrs
  reason: string
  operationId: string
}

/** 基础属性写入弹窗：表单在 Dialog 内，真实写动作再经 DangerConfirm 二次确认。 */
function BasicAttrsDialog({
  open,
  instanceId,
  view,
  onOpenChange,
  onWritten,
}: {
  open: boolean
  instanceId: number
  view: InventoryView
  onOpenChange: (open: boolean) => void
  onWritten: () => void
}) {
  const { t } = useTranslation()
  const [form, setForm] = useState(() => attrsToForm(view.basicAttrs))
  const [reason, setReason] = useState('')
  const [pending, setPending] = useState<PendingBasicAttrsWrite | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<BusinessResult | null>(null)
  const [error, setError] = useState('')
  const openRef = useRef(false)
  const playerRef = useRef(view.player)

  useEffect(() => {
    const shouldReset = open && (!openRef.current || playerRef.current !== view.player)
    openRef.current = open
    if (!shouldReset) return
    playerRef.current = view.player
    setForm(attrsToForm(view.basicAttrs))
    setReason('')
    setPending(null)
    setResult(null)
    setError('')
  }, [open, view.basicAttrs, view.player])

  const nextAttrs = useMemo(() => formToAttrs(form, view.basicAttrs), [form, view.basicAttrs])
  const invalid = nextAttrs === null

  const requestWrite = () => {
    if (!nextAttrs) {
      setError(t('inventory.basicAttrsInvalid'))
      return
    }
    setError('')
    setPending({
      next: nextAttrs,
      reason: reason.trim(),
      operationId: crypto.randomUUID(),
    })
  }

  const dispatchWrite = useCallback(
    async (write: PendingBasicAttrsWrite) => {
      setSubmitting(true)
      setError('')
      setResult(null)
      try {
        const payload = JSON.stringify({
          player: view.player,
          base: { dataVersion: view.dataVersion, basicAttrs: view.basicAttrs },
          edited: { dataVersion: view.dataVersion, basicAttrs: write.next },
        })
        const res = await dispatchBusiness(instanceId, 'inventory', 'writeBasicAttrs', payload, {
          write: true,
          operationId: write.operationId,
          reason: write.reason || undefined,
        })
        setResult(res)
        if (res.available) onWritten()
      } catch (err: unknown) {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        setError(msg || t('inventory.writeFailed'))
      } finally {
        setSubmitting(false)
      }
    },
    [instanceId, onWritten, t, view.basicAttrs, view.dataVersion, view.player],
  )

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
          <DialogHeader>
            <DialogTitle>{t('inventory.editBasicAttrs')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-3">
            <p className="text-xs text-muted-foreground">{t('inventory.basicAttrsWriteHint')}</p>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <NumberField label={t('inventory.health')} value={form.health} onChange={(v) => setForm((f) => ({ ...f, health: v }))} />
              <NumberField label={t('inventory.foodLevel')} value={form.foodLevel} onChange={(v) => setForm((f) => ({ ...f, foodLevel: v }))} />
              <NumberField label={t('inventory.xpLevel')} value={form.xpLevel} onChange={(v) => setForm((f) => ({ ...f, xpLevel: v }))} />
              <NumberField label={t('inventory.xpProgress')} value={form.xpProgress} onChange={(v) => setForm((f) => ({ ...f, xpProgress: v }))} />
              <NumberField label={t('inventory.xpTotal')} value={form.xpTotal} onChange={(v) => setForm((f) => ({ ...f, xpTotal: v }))} />
              <TextField label={t('inventory.gameMode')} value={form.gameMode} onChange={(v) => setForm((f) => ({ ...f, gameMode: v }))} />
            </div>
            <TextField
              label={t('inventory.reason')}
              value={reason}
              onChange={setReason}
              placeholder={t('inventory.reasonPlaceholder')}
            />
            {invalid && <p className="text-xs text-status-danger">{t('inventory.basicAttrsInvalid')}</p>}
            {error && <p className="text-xs text-status-danger">{error}</p>}
            {result && (
              <div className="rounded-lg border bg-muted/30 p-3 text-xs">
                {!result.available ? (
                  <p className="text-status-danger">{result.error || t('inventory.writeFailed')}</p>
                ) : (
                  <p className="text-status-success">{resultMessage(result.output, t)}</p>
                )}
              </div>
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={requestWrite} disabled={invalid || submitting}>
              {submitting && <Loader2 className="mr-1 size-3.5 animate-spin" />}
              {t('inventory.submitBasicAttrs')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <DangerConfirm
        open={pending !== null}
        title={t('inventory.confirmBasicAttrsTitle')}
        description={t('inventory.confirmBasicAttrsDesc', { player: view.player })}
        confirmLabel={t('inventory.confirmBasicAttrs')}
        scope="group"
        onConfirm={() => {
          const write = pending
          setPending(null)
          if (write) void dispatchWrite(write)
        }}
        onCancel={() => setPending(null)}
      />
    </>
  )
}

function NumberField({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return (
    <label className="flex flex-col gap-1 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <Input value={value} onChange={(e) => onChange(e.target.value)} inputMode="decimal" className="h-8 text-xs" />
    </label>
  )
}

function TextField({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  return (
    <label className="flex flex-col gap-1 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className="h-8 text-xs" />
    </label>
  )
}

function attrsToForm(attrs: BasicAttrs | null) {
  return {
    health: String(attrs?.health ?? 0),
    foodLevel: String(attrs?.foodLevel ?? 0),
    xpLevel: String(attrs?.xpLevel ?? 0),
    xpProgress: String(attrs?.xpProgress ?? 0),
    xpTotal: String(attrs?.xpTotal ?? 0),
    gameMode: attrs?.gameMode ?? '',
  }
}

function formToAttrs(form: ReturnType<typeof attrsToForm>, fallback: BasicAttrs | null): BasicAttrs | null {
  const health = toFiniteNumber(form.health)
  const foodLevel = toFiniteNumber(form.foodLevel)
  const xpLevel = toFiniteNumber(form.xpLevel)
  const xpProgress = toFiniteNumber(form.xpProgress)
  const xpTotal = toFiniteNumber(form.xpTotal)
  if (health === null || foodLevel === null || xpLevel === null || xpProgress === null || xpTotal === null) return null
  return {
    health,
    foodLevel,
    xpLevel,
    xpProgress,
    xpTotal,
    gameMode: form.gameMode.trim() || fallback?.gameMode || '',
  }
}

function toFiniteNumber(value: string): number | null {
  const n = Number(value)
  return Number.isFinite(n) && n >= 0 ? n : null
}

function resultMessage(output: unknown, t: (key: string, opts?: Record<string, unknown>) => string): string {
  if (typeof output !== 'object' || output === null) return t('inventory.writeSuccess')
  const o = output as Record<string, unknown>
  if (o.success === false) return String(o.errorCode || t('inventory.writeFailed'))
  if (o.online === false) return t('inventory.writePendingLogin')
  return t('inventory.writeSuccess')
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
        'transition-colors duration-200 ease-ios hover:bg-accent',
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
