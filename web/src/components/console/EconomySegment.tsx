import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Loader2, RefreshCw, Search, ShieldAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'
import { dispatchBusiness, fetchBusinessManifest, type BusinessResult } from '@/api/business'
import {
  fetchEconomyMirror,
  fetchEconomyLeaderboard,
  fetchEconomyEvents,
} from '@/api/economy'
import { toLedgerRows, isValidAmount, fmtEpochMillis } from './economy-view'

/**
 * 经济定制页（JBIS，FR-123，见 ADR-026/028/029）。
 *
 * 区别于 FR-119 通用 manifest 驱动的 {@link BusinessSegment}：本页为经济域定制四块——余额 / 排行 / 转账 / 流水。
 * 读走 FR-122 平台级镜像端点 + FR-123 旁路排行端点；转账 / 加扣复用 FR-121 写路径（dispatchBusiness +
 * DangerConfirm 二次确认 + 稳定 operationId 幂等键）。经济能力不可用时优雅降级（复用 business manifest 发现）。
 */
interface EconomySegmentProps {
  /** 实例 ID（写动作 POST /instances/:id/business 需要）。 */
  instanceId: number
}

export default function EconomySegment({ instanceId }: EconomySegmentProps) {
  const { t } = useTranslation()

  // 经济能力发现：复用 business manifest，检查是否存在 economy 域（探针未连 / 无经济插件则降级）。
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })
  const economyAvailable = useMemo(() => {
    const m = manifestQuery.data
    return !!m?.available && !!m.output?.domains?.economy
  }, [manifestQuery.data])

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 p-4">
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-semibold">{t('economy.title')}</h3>
        <span className="text-xs text-muted-foreground">{t('economy.subtitle')}</span>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 px-2 text-xs"
          onClick={() => void manifestQuery.refetch()}
          disabled={manifestQuery.isFetching}
        >
          <RefreshCw className={`mr-1 size-3.5 ${manifestQuery.isFetching ? 'animate-spin' : ''}`} />
          {t('economy.refresh')}
        </Button>
      </div>

      {manifestQuery.isLoading && (
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          {t('common.loading')}
        </div>
      )}

      {!manifestQuery.isLoading && !economyAvailable && (
        <div className="rounded border bg-muted/30 p-3 text-xs text-muted-foreground">
          {manifestQuery.data?.error || t('economy.unavailable')}
        </div>
      )}

      {!manifestQuery.isLoading && economyAvailable && (
        <Tabs defaultValue="balance" className="flex min-h-0 flex-1 flex-col">
          <TabsList className="self-start">
            <TabsTrigger value="balance">{t('economy.subTabBalance')}</TabsTrigger>
            <TabsTrigger value="leaderboard">{t('economy.subTabLeaderboard')}</TabsTrigger>
            <TabsTrigger value="transfer">{t('economy.subTabTransfer')}</TabsTrigger>
            <TabsTrigger value="ledger">{t('economy.subTabLedger')}</TabsTrigger>
          </TabsList>
          <div className="mt-3 min-h-0 flex-1 overflow-auto">
            <TabsContent value="balance">
              <BalanceView />
            </TabsContent>
            <TabsContent value="leaderboard">
              <LeaderboardView />
            </TabsContent>
            <TabsContent value="transfer">
              <TransferView instanceId={instanceId} />
            </TabsContent>
            <TabsContent value="ledger">
              <LedgerView />
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
    <label className={`flex flex-col gap-0.5 text-xs ${className ?? ''}`}>
      <span className="text-muted-foreground">{label}</span>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-7 text-xs"
      />
    </label>
  )
}

/** 提示条 + 错误/空态包装。 */
function Hint({ text }: { text: string }) {
  return <p className="text-xs text-muted-foreground">{text}</p>
}

// ======================== 余额视图 ========================

function BalanceView() {
  const { t } = useTranslation()
  const [player, setPlayer] = useState('')
  const [currency, setCurrency] = useState('')
  // 已提交的查询参数（null = 未查询）；点「查询」后落参，useQuery enabled。
  const [submitted, setSubmitted] = useState<{ player: string; currency: string } | null>(null)

  const query = useQuery({
    queryKey: ['economy-mirror', submitted],
    queryFn: () =>
      fetchEconomyMirror({
        player: submitted?.player || undefined,
        currency: submitted?.currency || undefined,
      }),
    enabled: submitted !== null,
  })

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
          className="h-7 px-3 text-xs"
          onClick={() => setSubmitted({ player: player.trim(), currency: currency.trim() })}
          disabled={query.isFetching}
        >
          {query.isFetching ? (
            <Loader2 className="mr-1 size-3.5 animate-spin" />
          ) : (
            <Search className="mr-1 size-3.5" />
          )}
          {t('economy.query')}
        </Button>
      </div>

      {query.isError && <p className="text-xs text-destructive">{t('economy.queryFailed')}</p>}
      {submitted !== null && !query.isFetching && (query.data?.length ?? 0) === 0 && !query.isError && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}
      {(query.data?.length ?? 0) > 0 && (
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
            {query.data!.map((r) => (
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
      )}
    </div>
  )
}

// ======================== 排行视图 ========================

function LeaderboardView() {
  const { t } = useTranslation()
  const [currency, setCurrency] = useState('')
  const [zone, setZone] = useState('')
  const [node, setNode] = useState('')
  const [submitted, setSubmitted] = useState<{ currency: string; zone: string; node: string } | null>(null)

  const query = useQuery({
    queryKey: ['economy-leaderboard', submitted],
    queryFn: () =>
      fetchEconomyLeaderboard({
        currency: submitted!.currency,
        zone: submitted?.zone || undefined,
        node: submitted?.node || undefined,
        limit: 50,
      }),
    // 排行须有货币（后端亦强制），无货币不发请求。
    enabled: submitted !== null && submitted.currency !== '',
  })

  const canQuery = currency.trim() !== ''

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
          className="h-7 px-3 text-xs"
          onClick={() => setSubmitted({ currency: currency.trim(), zone: zone.trim(), node: node.trim() })}
          disabled={!canQuery || query.isFetching}
        >
          {query.isFetching ? (
            <Loader2 className="mr-1 size-3.5 animate-spin" />
          ) : (
            <Search className="mr-1 size-3.5" />
          )}
          {t('economy.query')}
        </Button>
      </div>
      {!canQuery && <p className="text-xs text-muted-foreground">{t('economy.leaderboardNeedCurrency')}</p>}

      {query.isError && <p className="text-xs text-destructive">{t('economy.queryFailed')}</p>}
      {submitted !== null && !query.isFetching && (query.data?.length ?? 0) === 0 && !query.isError && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}
      {(query.data?.length ?? 0) > 0 && (
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
            {query.data!.map((r) => (
              <TableRow key={`${r.nodeUuid}/${r.zoneId}/${r.playerName}`}>
                <TableCell className="tabular-nums text-muted-foreground">{r.rank}</TableCell>
                <TableCell className="font-medium">{r.playerName}</TableCell>
                <TableCell className="font-mono text-[11px] text-muted-foreground">{r.nodeUuid || '—'}</TableCell>
                <TableCell>{r.zoneId || '—'}</TableCell>
                <TableCell className="text-right font-medium tabular-nums">{r.balance}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

// ======================== 转账 / 加扣视图 ========================

/** 待确认的写动作（转账或加扣）；点按钮后置入，DangerConfirm 确认才下发。 */
type PendingWrite =
  | { kind: 'transfer'; from: string; to: string; currency: string; amount: string }
  | { kind: 'deposit' | 'withdraw'; player: string; currency: string; amount: string }

function TransferView({ instanceId }: { instanceId: number }) {
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
      const action = w.kind
      const payload =
        w.kind === 'transfer'
          ? JSON.stringify({ from: w.from, to: w.to, currency: w.currency, amount: w.amount })
          : JSON.stringify({ player: w.player, currency: w.currency, amount: w.amount })
      try {
        const res = await dispatchBusiness(instanceId, 'economy', action, payload, {
          write: true,
          operationId: crypto.randomUUID(),
          reason: reason.trim() || undefined,
        })
        setResult(res)
      } catch (err: unknown) {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        setError(msg || t('economy.dispatchFailed'))
      } finally {
        setDispatching(false)
      }
    },
    [instanceId, reason, t],
  )

  const transferValid = from.trim() !== '' && to.trim() !== '' && transferCurrency.trim() !== '' && isValidAmount(transferAmount)
  const adjValid = adjPlayer.trim() !== '' && adjCurrency.trim() !== '' && isValidAmount(adjAmount)

  return (
    <div className="flex flex-col gap-4">
      <Hint text={t('economy.transferHint')} />

      {/* 转账块 */}
      <div className="rounded border p-3">
        <div className="mb-2 flex items-center gap-1.5 text-xs font-medium">
          <ShieldAlert className="size-3 text-destructive" aria-hidden />
          {t('economy.transfer')}
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <FieldInput label={t('economy.from')} value={from} onChange={setFrom} placeholder={t('economy.fromPlaceholder')} className="w-40" />
          <FieldInput label={t('economy.to')} value={to} onChange={setTo} placeholder={t('economy.toPlaceholder')} className="w-40" />
          <FieldInput label={t('economy.currency')} value={transferCurrency} onChange={setTransferCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-32" />
          <FieldInput label={t('economy.amount')} value={transferAmount} onChange={setTransferAmount} placeholder={t('economy.amountPlaceholder')} className="w-40" />
          <Button
            size="sm"
            variant="destructive"
            className="h-7 px-3 text-xs"
            disabled={!transferValid || dispatching}
            onClick={() =>
              setPending({ kind: 'transfer', from: from.trim(), to: to.trim(), currency: transferCurrency.trim(), amount: transferAmount.trim() })
            }
          >
            {t('economy.transfer')}
          </Button>
        </div>
        {transferAmount.trim() !== '' && !isValidAmount(transferAmount) && (
          <p className="mt-1 text-xs text-destructive">{t('economy.amountInvalid')}</p>
        )}
      </div>

      {/* 加扣块 */}
      <div className="rounded border p-3">
        <div className="mb-2 flex items-center gap-1.5 text-xs font-medium">
          <ShieldAlert className="size-3 text-destructive" aria-hidden />
          {t('economy.quickAdjust')}
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <FieldInput label={t('economy.player')} value={adjPlayer} onChange={setAdjPlayer} placeholder={t('economy.playerPlaceholder')} className="w-44" />
          <FieldInput label={t('economy.currency')} value={adjCurrency} onChange={setAdjCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-32" />
          <FieldInput label={t('economy.amount')} value={adjAmount} onChange={setAdjAmount} placeholder={t('economy.amountPlaceholder')} className="w-40" />
          <Button
            size="sm"
            variant="outline"
            className="h-7 px-3 text-xs"
            disabled={!adjValid || dispatching}
            onClick={() => setPending({ kind: 'deposit', player: adjPlayer.trim(), currency: adjCurrency.trim(), amount: adjAmount.trim() })}
          >
            {t('economy.deposit')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-7 px-3 text-xs"
            disabled={!adjValid || dispatching}
            onClick={() => setPending({ kind: 'withdraw', player: adjPlayer.trim(), currency: adjCurrency.trim(), amount: adjAmount.trim() })}
          >
            {t('economy.withdraw')}
          </Button>
        </div>
        {adjAmount.trim() !== '' && !isValidAmount(adjAmount) && (
          <p className="mt-1 text-xs text-destructive">{t('economy.amountInvalid')}</p>
        )}
      </div>

      {/* 原因（透传进插件流水 + JM 审计，FR-121） */}
      <FieldInput label={t('economy.reason')} value={reason} onChange={setReason} placeholder={t('economy.reasonPlaceholder')} className="max-w-xl" />

      {error && <p className="text-xs text-destructive">{error}</p>}
      {result && (
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-muted-foreground">{t('economy.writeResult')}</span>
          {!result.available ? (
            <p className="text-xs text-destructive">{result.error || t('economy.dispatchFailed')}</p>
          ) : (
            <pre className="overflow-auto rounded bg-muted/40 p-2 text-[11px]">{JSON.stringify(result.output, null, 2)}</pre>
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

function LedgerView() {
  const { t } = useTranslation()
  const [player, setPlayer] = useState('')
  const [currency, setCurrency] = useState('')
  const [submitted, setSubmitted] = useState<{ player: string; currency: string } | null>(null)

  // 经济事件流（domain=economy）；前端解析 envelope payload → 流水行，并按玩家/货币前端过滤。
  const query = useQuery({
    queryKey: ['economy-events', submitted],
    queryFn: () => fetchEconomyEvents({ limit: 200 }),
    enabled: submitted !== null,
  })

  const rows = useMemo(() => {
    const all = toLedgerRows(query.data ?? [])
    const p = submitted?.player ?? ''
    const c = submitted?.currency ?? ''
    return all.filter((r) => (p === '' || r.playerName === p) && (c === '' || r.currency === c))
  }, [query.data, submitted])

  return (
    <div className="flex flex-col gap-3">
      <Hint text={t('economy.ledgerHint')} />
      <div className="flex flex-wrap items-end gap-2">
        <FieldInput label={t('economy.player')} value={player} onChange={setPlayer} placeholder={t('economy.playerPlaceholder')} className="w-48" />
        <FieldInput label={t('economy.currency')} value={currency} onChange={setCurrency} placeholder={t('economy.currencyPlaceholder')} className="w-40" />
        <Button
          size="sm"
          className="h-7 px-3 text-xs"
          onClick={() => setSubmitted({ player: player.trim(), currency: currency.trim() })}
          disabled={query.isFetching}
        >
          {query.isFetching ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <Search className="mr-1 size-3.5" />}
          {t('economy.query')}
        </Button>
      </div>

      {query.isError && <p className="text-xs text-destructive">{t('economy.queryFailed')}</p>}
      {submitted !== null && !query.isFetching && rows.length === 0 && !query.isError && (
        <p className="text-xs text-muted-foreground">{t('economy.empty')}</p>
      )}
      {rows.length > 0 && (
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
                <TableCell className="text-right font-medium tabular-nums">{r.signedAmount || '—'}</TableCell>
                <TableCell className="text-right tabular-nums">{r.balanceAfter || '—'}</TableCell>
                <TableCell>{r.zoneId || '—'}</TableCell>
                <TableCell className="font-mono text-[11px] text-muted-foreground">{r.ledgerId || '—'}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
