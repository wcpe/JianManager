import type { BusinessEvent } from '@/api/economy'

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
