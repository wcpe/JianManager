import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Coins, Loader2, RefreshCw, Search, ShieldAlert } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@jianmanager/ui/components/tabs'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import type { BusinessResult } from '@/lib/console/business'
import type { EconomyLeaderboardRow, EconomyMirrorRow } from '@/lib/instances/economy'
import { aggregateByCurrency, fmtEpochMillis, isValidAmount } from '@/lib/instances/economy-view'
import type { EconomyLedgerRow } from '@/lib/instances/economy-view'

/**
 * 经济定制页（JBIS，FR-123，见 ADR-026/028/029）。
 *
 * 区别于 FR-119 通用 manifest 驱动的 {@link BusinessSegment}：本页为经济域定制四块——余额 / 排行 / 转账 / 流水。
 * 读走 FR-122 平台级镜像端点 + FR-123 旁路排行端点；转账 / 加扣复用 FR-121 写路径（dispatchBusiness +
 * DangerConfirm 二次确认 + 稳定 operationId 幂等键）。经济能力不可用时优雅降级（复用 business manifest 发现）。
 * 视觉为靛蓝圆角范式（FR-163）：余额含多区聚合 {@link StatCard} 概览，明细 / 写动作走统一 {@link Panel} 原语。
 *
 * 受控视图（ADR-097 a 范式）：不取数——manifest 探测态、三份查询结果与写动作都由外壳注入。
 * 四块子视图的呈现、多区聚合、金额校验、二次确认留在视图内。
 */
export interface EconomySegmentProps {
  /** manifest 探测中。 */
  manifestLoading?: boolean
  /** manifest 刷新中。 */
  manifestRefreshing?: boolean
  /** 经济能力可用（外壳按 manifest 的 economy 域判定）。 */
  economyAvailable: boolean
  /** 能力不可用时的原因文案。 */
  manifestError?: string
  /** 刷新 manifest。 */
  onRefreshManifest: () => void

  /** 余额镜像行。 */
  balanceRows: EconomyMirrorRow[]
  balanceLoading?: boolean
  balanceError?: boolean
  /** 查询余额（外壳发镜像端点请求）。 */
  onQueryBalance: (params: { player: string; currency: string }) => void

  /** 排行行。 */
  leaderboardRows: EconomyLeaderboardRow[]
  leaderboardLoading?: boolean
  leaderboardError?: boolean
  /** 查询排行（货币必填）。 */
  onQueryLeaderboard: (params: { currency: string; zone: string; node: string }) => void

  /** 经济流水行（外壳取事件流并解析）。 */
  ledgerRows: EconomyLedgerRow[]
  ledgerLoading?: boolean
  ledgerError?: boolean
  /** 查询流水。 */
  onQueryLedger: (params: { player: string; currency: string }) => void

  /** 下发写动作（转账/加扣），返回业务结果。 */
  onDispatch: (payload: {
    action: 'transfer' | 'deposit' | 'withdraw'
    payload: string
    reason?: string
    operationId: string
  }) => Promise<BusinessResult>
}

export default function EconomySegment({
  manifestLoading = false,
  manifestRefreshing = false,
  economyAvailable,
  manifestError,
  onRefreshManifest,
  balanceRows,
  balanceLoading = false,
  balanceError = false,
  onQueryBalance,
  leaderboardRows,
  leaderboardLoading = false,
  leaderboardError = false,
  onQueryLeaderboard,
  ledgerRows,
  ledgerLoading = false,
  ledgerError = false,
  onQueryLedger,
  onDispatch,
}: EconomySegmentProps) {
  const { t } = useTranslation()

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 p-4">
      <div className="flex items-center gap-2.5">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-accent text-primary">
          <Coins className="size-4" />
        </span>
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{t('economy.title')}</h3>
          <p className="truncate text-xs text-muted-foreground">{t('economy.subtitle')}</p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 rounded-full px-3 text-xs"
          onClick={onRefreshManifest}
          disabled={manifestRefreshing}
        >
          <RefreshCw className={cn('mr-1 size-3.5', manifestRefreshing && 'animate-spin')} />
          {t('economy.refresh')}
        </Button>
      </div>

      {manifestLoading && (
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          {t('common.loading')}
        </div>
      )}

      {!manifestLoading && !economyAvailable && (
        <div className="rounded-xl border bg-muted/30 p-4 text-xs text-muted-foreground shadow-soft">
          {manifestError || t('economy.unavailable')}
        </div>
      )}

      {!manifestLoading && economyAvailable && (
        <Tabs defaultValue="balance" className="flex min-h-0 flex-1 flex-col">
          <TabsList className="self-start rounded-full">
            <TabsTrigger value="balance" className="rounded-full text-xs">{t('economy.subTabBalance')}</TabsTrigger>
            <TabsTrigger value="leaderboard" className="rounded-full text-xs">{t('economy.subTabLeaderboard')}</TabsTrigger>
            <TabsTrigger value="transfer" className="rounded-full text-xs">{t('economy.subTabTransfer')}</TabsTrigger>
            <TabsTrigger value="ledger" className="rounded-full text-xs">{t('economy.subTabLedger')}</TabsTrigger>
          </TabsList>
          <div className="mt-3 min-h-0 flex-1 overflow-auto">
            <TabsContent value="balance">
              <BalanceView rows={balanceRows} loading={balanceLoading} error={balanceError} onQuery={onQueryBalance} />
            </TabsContent>
            <TabsContent value="leaderboard">
              <LeaderboardView
                rows={leaderboardRows}
                loading={leaderboardLoading}
                error={leaderboardError}
                onQuery={onQueryLeaderboard}
              />
            </TabsContent>
            <TabsContent value="transfer">
              <TransferView onDispatch={onDispatch} />
            </TabsContent>
            <TabsContent value="ledger">
              <LedgerView rows={ledgerRows} loading={ledgerLoading} error={ledgerError} onQuery={onQueryLedger} />
            </TabsContent>
          </div>
        </Tabs>
      )}
    </div>
  )
}

/** 小标签输入：紧凑表单字段。 */
function FieldInput({
  label,
  value,
  onChange,
  placeholder,
  className,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  className?: string
}) {
  return (
    <label className={cn('flex flex-col gap-0.5 text-xs', className)}>
      <span className="text-muted-foreground">{label}</span>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-8 text-xs"
      />
    </label>
  )
}

/** 提示条 + 错误/空态包装。 */
function Hint({ text }: { text: string }) {
  return <p className="text-xs text-muted-foreground">{text}</p>
}

// ======================== 余额视图 ========================

function BalanceView({
  rows,
  loading,
  error,
  onQuery,
}: {
  rows: EconomyMirrorRow[]
  loading: boolean
  error: boolean
  onQuery: (params: { player: string; currency: string }) => void
}) {
  const { t } = useTranslation()
  const [player, setPlayer] = useState('')
  const [currency, setCurrency] = useState('')
  // 已提交标记：区分「还没查」与「查了但空」（空态不该在首屏就出现）。
  const [submitted, setSubmitted] = useState(false)

  // 多区聚合概览：同币种跨节点/区合并为一张卡（总额 + 分布区数），呈现设计 §3「经济多区聚合」。
  const aggregates = useMemo(() => aggregateByCurrency(rows), [rows])

  const runQuery = () => {
    setSubmitted(true)
    onQuery({ player: player.trim(), currency: currency.trim() })
  }

  return (
    <div className="flex flex-col gap-3">
      <Hint text={t('economy.balanceHint')} />
      <div className="flex flex-wrap items-end gap-2">
        <FieldInput
          label={t('economy.player')}
          value={player}
          onChange={setPlayer}
          placeholder={t('economy.playerPlaceholder')}
          className="w-48"
        />
        <FieldInput
          label={t('economy.currency')}
          value={currency}
          onChange={setCurrency}
          placeholder={t('economy.currencyPlaceholder')}
          className="w-40"
        />
        <Button
          size="sm"
          className="h-8 rounded-full px-4 text-xs"
          onClick={runQuery}
          disabled={loading}
        >
          {loading ? (
            <Loader2 className="mr-1 size-3.5 animate-spin" />
          ) : (
            <Search className="mr-1 size-3.5" />
          )}
          {t('economy.query')}
        </Button>
      </div>

      {error && <p className="text-xs text-status-danger">{t('economy.queryFailed')}</p>}
      {submitted && !loading && rows.length === 0 && !error && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}

      {/* 多区聚合概览卡片：每币种总额 + 分布区数 */}
      {aggregates.length > 0 && (
        <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-4">
          {aggregates.map((a) => (
            <StatCard
              key={a.currency}
              icon={<Coins className="size-3.5" />}
              label={a.currency}
              value={a.total}
              sub={t('economy.distributedInZones', { count: a.sources })}
            />
          ))}
        </div>
      )}

      {rows.length > 0 && (
        <Panel title={t('economy.balanceDetail')} icon={<Coins className="size-3.5" />} bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('economy.player')}</TableHead>
                <TableHead>{t('economy.currency')}</TableHead>
                <TableHead>{t('economy.colNode')}</TableHead>
                <TableHead>{t('economy.colZone')}</TableHead>
                <TableHead className="text-right">{t('economy.balance')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="font-medium">{r.playerName}</TableCell>
                  <TableCell>{r.currency}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{r.nodeUuid || '—'}</TableCell>
                  <TableCell>{r.zoneId || '—'}</TableCell>
                  <TableCell className="text-right font-medium tabular-nums">{r.balance}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}
    </div>
  )
}

// ======================== 排行视图 ========================

function LeaderboardView({
  rows,
  loading,
  error,
  onQuery,
}: {
  rows: EconomyLeaderboardRow[]
  loading: boolean
  error: boolean
  onQuery: (params: { currency: string; zone: string; node: string }) => void
}) {
  const { t } = useTranslation()
  const [currency, setCurrency] = useState('')
  const [zone, setZone] = useState('')
  const [node, setNode] = useState('')
  const [submitted, setSubmitted] = useState(false)

  const canQuery = currency.trim() !== ''

  const runQuery = () => {
    setSubmitted(true)
    onQuery({ currency: currency.trim(), zone: zone.trim(), node: node.trim() })
  }

  return (
    <div className="flex flex-col gap-3">
      <Hint text={t('economy.leaderboardHint')} />
      <div className="flex flex-wrap items-end gap-2">
        <FieldInput
          label={t('economy.currency')}
          value={currency}
          onChange={setCurrency}
          placeholder={t('economy.currencyPlaceholder')}
          className="w-40"
        />
        <FieldInput
          label={t('economy.zone')}
          value={zone}
          onChange={setZone}
          placeholder={t('economy.zonePlaceholder')}
          className="w-40"
        />
        <FieldInput
          label={t('economy.node')}
          value={node}
          onChange={setNode}
          placeholder={t('economy.nodePlaceholder')}
          className="w-48"
        />
        <Button
          size="sm"
          className="h-8 rounded-full px-4 text-xs"
          onClick={runQuery}
          disabled={!canQuery || loading}
        >
          {loading ? (
            <Loader2 className="mr-1 size-3.5 animate-spin" />
          ) : (
            <Search className="mr-1 size-3.5" />
          )}
          {t('economy.query')}
        </Button>
      </div>
      {!canQuery && <p className="text-xs text-muted-foreground">{t('economy.leaderboardNeedCurrency')}</p>}

      {error && <p className="text-xs text-status-danger">{t('economy.queryFailed')}</p>}
      {submitted && !loading && rows.length === 0 && !error && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}
      {rows.length > 0 && (
        <Panel title={t('economy.subTabLeaderboard')} icon={<Coins className="size-3.5" />} bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-12">{t('economy.rank')}</TableHead>
                <TableHead>{t('economy.player')}</TableHead>
                <TableHead>{t('economy.colNode')}</TableHead>
                <TableHead>{t('economy.colZone')}</TableHead>
                <TableHead className="text-right">{t('economy.balance')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={`${r.nodeUuid}/${r.zoneId}/${r.playerName}`}>
                  <TableCell className="tabular-nums">
                    <RankBadge rank={r.rank} />
                  </TableCell>
                  <TableCell className="font-medium">{r.playerName}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{r.nodeUuid || '—'}</TableCell>
                  <TableCell>{r.zoneId || '—'}</TableCell>
                  <TableCell className="text-right font-medium tabular-nums">{r.balance}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}
    </div>
  )
}

/** 名次徽标：前三名走主色实心 pill，其余朴素灰。 */
function RankBadge({ rank }: { rank: number }) {
  const top = rank <= 3
  return (
    <span
      className={cn(
        'inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[11px] font-semibold tabular-nums',
        top ? 'bg-primary text-primary-foreground' : 'text-muted-foreground',
      )}
    >
      {rank}
    </span>
  )
}

// ======================== 转账 / 加扣视图 ========================

/** 待确认的写动作（转账或加扣）；点按钮后置入，DangerConfirm 确认才下发。 */
type PendingWrite =
  | { kind: 'transfer'; from: string; to: string; currency: string; amount: string }
  | { kind: 'deposit' | 'withdraw'; player: string; currency: string; amount: string }

function TransferView({ onDispatch }: { onDispatch: EconomySegmentProps['onDispatch'] }) {
  const { t } = useTranslation()
  // 转账表单
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [transferCurrency, setTransferCurrency] = useState('')
  const [transferAmount, setTransferAmount] = useState('')
  // 加扣表单
  const [adjPlayer, setAdjPlayer] = useState('')
  const [adjCurrency, setAdjCurrency] = useState('')
  const [adjAmount, setAdjAmount] = useState('')
  const [reason, setReason] = useState('')

  const [pending, setPending] = useState<PendingWrite | null>(null)
  const [dispatching, setDispatching] = useState(false)
  const [result, setResult] = useState<BusinessResult | null>(null)
  const [error, setError] = useState('')

  // 真正下发：payload 仅含业务字段（不含 taskId——由 CP 注入）；顶层带稳定 operationId 作幂等键 + reason。
  const doDispatch = useCallback(
    async (w: PendingWrite) => {
      setDispatching(true)
      setError('')
      setResult(null)
      const payload =
        w.kind === 'transfer'
          ? JSON.stringify({ from: w.from, to: w.to, currency: w.currency, amount: w.amount })
          : JSON.stringify({ player: w.player, currency: w.currency, amount: w.amount })
      try {
        const res = await onDispatch({
          action: w.kind,
          payload,
          reason: reason.trim() || undefined,
          operationId: crypto.randomUUID(),
        })
        setResult(res)
      } catch (err: unknown) {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        setError(msg || t('economy.dispatchFailed'))
      } finally {
        setDispatching(false)
      }
    },
    [onDispatch, reason, t],
  )

  const transferValid = from.trim() !== '' && to.trim() !== '' && transferCurrency.trim() !== '' && isValidAmount(transferAmount)
  const adjValid = adjPlayer.trim() !== '' && adjCurrency.trim() !== '' && isValidAmount(adjAmount)

  return (
    <div className="flex flex-col gap-4">
      <Hint text={t('economy.transferHint')} />

      {/* 转账块 */}
      <Panel
        title={t('economy.transfer')}
        icon={<ShieldAlert className="size-3.5" />}
        tone="danger"
        bodyClassName="p-4"
      >
        <div className="flex flex-wrap items-end gap-2">
          <FieldInput label={t('economy.from')} value={from} onChange={setFrom} placeholder={t('economy.fromPlaceholder')} className="w-40" />
          <FieldInput label={t('economy.to')} value={to} onChange={setTo} placeholder={t('economy.toPlaceholder')} className="w-40" />
          <FieldInput label={t('economy.currency')} value={transferCurrency} onChange={setTransferCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-32" />
          <FieldInput label={t('economy.amount')} value={transferAmount} onChange={setTransferAmount} placeholder={t('economy.amountPlaceholder')} className="w-40" />
          <Button
            size="sm"
            variant="destructive"
            className="h-8 rounded-full px-4 text-xs"
            disabled={!transferValid || dispatching}
            onClick={() =>
              setPending({ kind: 'transfer', from: from.trim(), to: to.trim(), currency: transferCurrency.trim(), amount: transferAmount.trim() })
            }
          >
            {t('economy.transfer')}
          </Button>
        </div>
        {transferAmount.trim() !== '' && !isValidAmount(transferAmount) && (
          <p className="mt-1.5 text-xs text-status-danger">{t('economy.amountInvalid')}</p>
        )}
      </Panel>

      {/* 加扣块 */}
      <Panel
        title={t('economy.quickAdjust')}
        icon={<ShieldAlert className="size-3.5" />}
        tone="warning"
        bodyClassName="p-4"
      >
        <div className="flex flex-wrap items-end gap-2">
          <FieldInput label={t('economy.player')} value={adjPlayer} onChange={setAdjPlayer} placeholder={t('economy.playerPlaceholder')} className="w-44" />
          <FieldInput label={t('economy.currency')} value={adjCurrency} onChange={setAdjCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-32" />
          <FieldInput label={t('economy.amount')} value={adjAmount} onChange={setAdjAmount} placeholder={t('economy.amountPlaceholder')} className="w-40" />
          <Button
            size="sm"
            variant="outline"
            className="h-8 rounded-full px-4 text-xs"
            disabled={!adjValid || dispatching}
            onClick={() => setPending({ kind: 'deposit', player: adjPlayer.trim(), currency: adjCurrency.trim(), amount: adjAmount.trim() })}
          >
            {t('economy.deposit')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-8 rounded-full px-4 text-xs"
            disabled={!adjValid || dispatching}
            onClick={() => setPending({ kind: 'withdraw', player: adjPlayer.trim(), currency: adjCurrency.trim(), amount: adjAmount.trim() })}
          >
            {t('economy.withdraw')}
          </Button>
        </div>
        {adjAmount.trim() !== '' && !isValidAmount(adjAmount) && (
          <p className="mt-1.5 text-xs text-status-danger">{t('economy.amountInvalid')}</p>
        )}
      </Panel>

      {/* 原因（透传进插件流水 + JM 审计，FR-121） */}
      <FieldInput label={t('economy.reason')} value={reason} onChange={setReason} placeholder={t('economy.reasonPlaceholder')} className="max-w-xl" />

      {error && <p className="text-xs text-status-danger">{error}</p>}
      {result && (
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-muted-foreground">{t('economy.writeResult')}</span>
          {!result.available ? (
            <p className="text-xs text-status-danger">{result.error || t('economy.dispatchFailed')}</p>
          ) : (
            <pre className="overflow-auto rounded-lg bg-muted/50 p-2.5 text-[11px]">{JSON.stringify(result.output, null, 2)}</pre>
          )}
        </div>
      )}

      {/* 写动作二次确认（FR-121）：高危经济写须确认后下发，避免误操作刷钱/转错。 */}
      <DangerConfirm
        open={pending !== null}
        title={pending?.kind === 'transfer' ? t('economy.confirmTransferTitle') : t('economy.confirmAdjustTitle')}
        description={
          pending?.kind === 'transfer'
            ? t('economy.confirmTransferDesc', { from: pending.from, to: pending.to, amount: pending.amount, currency: pending.currency })
            : pending
              ? t('economy.confirmAdjustDesc', {
                  player: pending.player,
                  action: pending.kind === 'deposit' ? t('economy.deposit') : t('economy.withdraw'),
                  amount: pending.amount,
                  currency: pending.currency,
                })
              : ''
        }
        confirmLabel={t('economy.confirm')}
        scope="group"
        onConfirm={() => {
          const w = pending
          setPending(null)
          if (w) void doDispatch(w)
        }}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}

// ======================== 流水视图 ========================

function LedgerView({
  rows: allRows,
  loading,
  error,
  onQuery,
}: {
  rows: EconomyLedgerRow[]
  loading: boolean
  error: boolean
  onQuery: (params: { player: string; currency: string }) => void
}) {
  const { t } = useTranslation()
  const [player, setPlayer] = useState('')
  const [currency, setCurrency] = useState('')
  const [submitted, setSubmitted] = useState<{ player: string; currency: string } | null>(null)

  // 经济事件流（domain=economy）由外壳取；视图按玩家/货币前端过滤。
  const rows = useMemo(() => {
    const p = submitted?.player ?? ''
    const c = submitted?.currency ?? ''
    return allRows.filter((r) => (p === '' || r.playerName === p) && (c === '' || r.currency === c))
  }, [allRows, submitted])

  return (
    <div className="flex flex-col gap-3">
      <Hint text={t('economy.ledgerHint')} />
      <div className="flex flex-wrap items-end gap-2">
        <FieldInput label={t('economy.player')} value={player} onChange={setPlayer} placeholder={t('economy.playerPlaceholder')} className="w-48" />
        <FieldInput label={t('economy.currency')} value={currency} onChange={setCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-40" />
        <Button
          size="sm"
          className="h-8 rounded-full px-4 text-xs"
          onClick={() => {
            setSubmitted({ player: player.trim(), currency: currency.trim() })
            onQuery({ player: player.trim(), currency: currency.trim() })
          }}
          disabled={loading}
        >
          {loading ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <Search className="mr-1 size-3.5" />}
          {t('economy.query')}
        </Button>
      </div>

      {error && <p className="text-xs text-status-danger">{t('economy.queryFailed')}</p>}
      {submitted !== null && !loading && rows.length === 0 && !error && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}
      {rows.length > 0 && (
        <Panel title={t('economy.subTabLedger')} icon={<Coins className="size-3.5" />} bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('economy.colTime')}</TableHead>
                <TableHead>{t('economy.player')}</TableHead>
                <TableHead>{t('economy.currency')}</TableHead>
                <TableHead>{t('economy.colEntryType')}</TableHead>
                <TableHead className="text-right">{t('economy.colSignedAmount')}</TableHead>
                <TableHead className="text-right">{t('economy.colBalanceAfter')}</TableHead>
                <TableHead>{t('economy.colZone')}</TableHead>
                <TableHead>{t('economy.colLedgerId')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="text-[11px] text-muted-foreground">{fmtEpochMillis(r.occurredAt)}</TableCell>
                  <TableCell className="font-medium">{r.playerName}</TableCell>
                  <TableCell>{r.currency}</TableCell>
                  <TableCell>{r.entryType || '—'}</TableCell>
                  <TableCell className={cn('text-right font-medium tabular-nums', signedAmountClass(r.signedAmount))}>
                    {r.signedAmount || '—'}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{r.balanceAfter || '—'}</TableCell>
                  <TableCell>{r.zoneId || '—'}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{r.ledgerId || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}
    </div>
  )
}

/** 流水变更额着色：入账（正/无符号）绿、扣账（负）红；空串无色。 */
function signedAmountClass(signed: string): string {
  const s = signed.trim()
  if (s === '') return ''
  if (s.startsWith('-')) return 'text-status-danger'
  return 'text-status-success'
}
