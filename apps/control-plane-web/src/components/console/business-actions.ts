import type { BusinessAction } from '@/api/business'

/**
 * 业务动作的纯函数判定（FR-121，见 ADR-029）。抽成独立模块便于单测，
 * 且不污染 BusinessSegment 组件文件的 fast-refresh（react-refresh/only-export-components）。
 */

/** 判定一个业务动作是否为写动作（manifest readOnly 缺省视为写，从严）。 */
export function isWriteAction(action: BusinessAction): boolean {
  return action.readOnly !== true
}

/**
 * 单个入参的智能取值（FR-119）。
 *
 * 泛化业务台对每个 arg 只有一个文本框，但契约里既有**标量**入参（economy 的 player/currency/amount，
 * 探针侧按字符串取用），也有**对象 / 数组**入参（inventory.writeBasicAttrs 的 base/edited，契约为
 * `{dataVersion,basicAttrs:{...}}` 嵌套对象，探针侧 `req.getObject("base")` 需真对象）。
 *
 * 策略：文本 trim 后**以 `{` 或 `[` 起头**才尝试 `JSON.parse`——解析成对象/数组则用解析值（还原嵌套结构），
 * 解析失败则原样当字符串下发（不吞错，交由探针按契约报错）。其余一律保持原字符串，
 * 保证标量入参（如 `amount="100"`）不被误转成数字而破坏既有下发。
 */
export function coerceBusinessArg(raw: string): unknown {
  const trimmed = raw.trim()
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (parsed !== null && typeof parsed === 'object') return parsed
    } catch {
      // 非法 JSON：保持原字符串下发，让探针侧按契约给出错误，而非前端静默改写。
    }
  }
  return raw
}

/**
 * 把泛化业务台的文本入参构造成下发 payload 的 JSON 字符串（FR-119）。
 * 逐个入参走 {@link coerceBusinessArg}：对象/数组入参还原为真嵌套结构，标量保持字符串。
 */
export function buildBusinessPayload(args: Record<string, string>): string {
  const payload: Record<string, unknown> = {}
  for (const [key, raw] of Object.entries(args)) {
    payload[key] = coerceBusinessArg(raw)
  }
  return JSON.stringify(payload)
}
