import type { BusinessEvent, EconomyMirrorRow } from '@/api/economy'

/**
 * 经济定制页纯逻辑（FR-123）。抽成独立模块便于单测，且不污染组件文件的 fast-refresh
 * （react-refresh/only-export-components）。与 business-actions.ts / audit-filters.ts 同范式。
 */

/** 一条经济流水行（由 [BusinessEvent] 的 envelope payload 解析而来，供流水表展示）。 */
export interface EconomyLedgerRow {
  /** envelope 主键，作 React key（同 (domain,dedupKey) 去重后稳定）。 */
  id: number
  playerName: string
  currency: string
  zoneId: string
  /** 入账类型（DEPOSIT/WITHDRAW/TRANSFER_IN/…）；缺失为空串。 */
  entryType: string
  /** 带符号变更额（字符串承载 BigDecimal，正入负出）；缺失为空串。 */
  signedAmount: string
  /** 变更后余额（字符串承载 BigDecimal）；缺失为空串。 */
  balanceAfter: string
  /** mce 总账流水号（dedupKey）；缺失为空串。 */
  ledgerId: string
  nodeUuid: string
  /** 账务发生时间（epoch 毫秒，0 表示未携带）。 */
  occurredAt: number
}

/** 经济 envelope payload 的 data 段（探针折算时已全部字符串化，金额禁浮点）。 */
interface EconomyEnvelopeData {
  playerName?: string
  currency?: string
  zoneId?: string
  entryType?: string
  signedAmount?: string
  balanceAfter?: string
  ledgerId?: string
  occurredAt?: string
}

/**
 * 把一条业务事件 envelope 解析为经济流水行；payload 非法或缺关键字段时返回 null（坏事件降级，不渲染坏行）。
 * payload 形如 `{type,event,domain,dedupKey,data:{...经济字段...}}`（探针 BridgeClient 产物，CP 原样留存）。
 */
export function toLedgerRow(evt: BusinessEvent): EconomyLedgerRow | null {
  const data = parseEnvelopeData(evt.payloadJson)
  // 关键字段（玩家 + 货币）缺失视为坏事件，不入流水表（与后端 parseEconomyData 同口径从严）。
  if (!data || !data.playerName || !data.currency) return null
  return {
    id: evt.id,
    playerName: data.playerName,
    currency: data.currency,
    zoneId: data.zoneId ?? '',
    entryType: data.entryType ?? evt.action ?? '',
    signedAmount: data.signedAmount ?? '',
    balanceAfter: data.balanceAfter ?? '',
    ledgerId: data.ledgerId ?? evt.dedupKey ?? '',
    nodeUuid: data.nodeUuid ?? evt.nodeUuid ?? '',
    occurredAt: data.occurredAt != null ? Number(data.occurredAt) || 0 : evt.occurredAt || 0,
  }
}

/** 批量解析事件流为流水行，丢弃解析失败的坏事件（保持顺序）。 */
export function toLedgerRows(events: BusinessEvent[]): EconomyLedgerRow[] {
  const rows: EconomyLedgerRow[] = []
  for (const e of events) {
    const r = toLedgerRow(e)
    if (r) rows.push(r)
  }
  return rows
}

/** 从 envelope payload JSON 提取 data 段；非 JSON / 无 data 返回 null。 */
function parseEnvelopeData(payloadJson: string | undefined): (EconomyEnvelopeData & { nodeUuid?: string }) | null {
  if (!payloadJson) return null
  let frame: unknown
  try {
    frame = JSON.parse(payloadJson)
  } catch {
    return null
  }
  if (typeof frame !== 'object' || frame === null) return null
  const data = (frame as { data?: unknown }).data
  if (typeof data !== 'object' || data === null) return null
  return data as EconomyEnvelopeData
}

/**
 * 校验金额输入：非空、可解析为有限数、> 0。**不**返回解析后的数值——金额一律按原字符串下发（禁浮点，
 * 防多币种精度失真，FR-122）；此处只做「能否提交」的合法性判定。
 */
export function isValidAmount(raw: string): boolean {
  const s = raw.trim()
  if (s === '') return false
  // 仅允许十进制数字串（可带一个小数点，无符号——金额恒正，符号语义由 deposit/withdraw 动作区分）。
  if (!/^\d+(\.\d+)?$/.test(s)) return false
  const n = Number(s)
  return Number.isFinite(n) && n > 0
}

/** 经济排行/镜像查询的入参合法性（货币必填用于排行）。 */
export function canQueryLeaderboard(currency: string): boolean {
  return currency.trim() !== ''
}

/** epoch 毫秒 → 本地可读时间；0/非法显示破折号。 */
export function fmtEpochMillis(ms: number): string {
  if (!ms || !Number.isFinite(ms) || ms <= 0) return '—'
  return new Date(ms).toLocaleString()
}

/** 某货币的多区聚合余额（跨节点/区合并后的一行，供「多区聚合」概览卡片）。 */
export interface CurrencyAggregate {
  /** 货币标识。 */
  currency: string
  /** 跨所有来源的余额合计（字符串承载，禁浮点，精确十进制相加）。 */
  total: string
  /** 贡献来源数（不同 (node,zone) 行数），呈现「分布在 N 区」。 */
  sources: number
}

/**
 * 精确十进制求和（禁浮点）：以小数位最多者对齐，按整数（BigInt）相加，避免 0.1+0.2 之类浮点误差与大数精度丢失（FR-122）。
 * 忽略空串与非法项（不形如 `[-]\d+(.\d+)?`）。空列表返回 "0"。
 */
export function sumDecimalStrings(values: string[]): string {
  // 先归一为合法十进制串，计算最大小数位。
  const parsed: { neg: boolean; intPart: string; fracPart: string }[] = []
  let maxFrac = 0
  for (const raw of values) {
    const s = raw.trim()
    const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(s)
    if (!m) continue
    const neg = m[1] === '-'
    const intPart = m[2]
    const fracPart = m[3] ?? ''
    if (fracPart.length > maxFrac) maxFrac = fracPart.length
    parsed.push({ neg, intPart, fracPart })
  }
  let acc = 0n
  for (const p of parsed) {
    const scaled = BigInt(p.intPart + p.fracPart.padEnd(maxFrac, '0'))
    acc += p.neg ? -scaled : scaled
  }
  return formatScaled(acc, maxFrac)
}

/** 把放大 scale 位的 BigInt 还原为十进制串，并去掉多余的尾随 0（如 4.00→4）。 */
function formatScaled(scaled: bigint, scale: number): string {
  if (scale === 0) return scaled.toString()
  const neg = scaled < 0n
  const digits = (neg ? -scaled : scaled).toString().padStart(scale + 1, '0')
  const intPart = digits.slice(0, digits.length - scale)
  const fracPart = digits.slice(digits.length - scale).replace(/0+$/, '')
  const body = fracPart ? `${intPart}.${fracPart}` : intPart
  return neg && body !== '0' ? `-${body}` : body
}

/**
 * 多区聚合：把镜像行按货币合并，得到每币种的总额 + 来源区数（按货币字典序稳定排序）。
 * 用于经济页「余额」子页的聚合概览，区别于逐 (node,zone) 明细行（设计 §3「经济多区聚合」）。
 */
export function aggregateByCurrency(rows: EconomyMirrorRow[]): CurrencyAggregate[] {
  const byCurrency = new Map<string, string[]>()
  for (const r of rows) {
    const list = byCurrency.get(r.currency)
    if (list) list.push(r.balance)
    else byCurrency.set(r.currency, [r.balance])
  }
  return [...byCurrency.entries()]
    .map(([currency, balances]) => ({ currency, total: sumDecimalStrings(balances), sources: balances.length }))
    .sort((a, b) => a.currency.localeCompare(b.currency))
}
