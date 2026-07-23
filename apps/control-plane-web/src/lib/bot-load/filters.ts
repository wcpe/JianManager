/** URL 查询读写：Bot / 失败筛选。 */

export interface BotListFilter {
  q?: string
  status?: string
  node?: string
  step?: string
  error?: string
  page: number
}

export interface FailureListFilter {
  category?: string
  errorCode?: string
  botUuid?: string
  node?: string
  step?: string
  page: number
}

export function readBotFilter(params: URLSearchParams): BotListFilter {
  return {
    q: params.get('q') || undefined,
    status: params.get('status') || undefined,
    node: params.get('node') || undefined,
    step: params.get('step') || undefined,
    error: params.get('error') || undefined,
    page: Math.max(1, Number(params.get('page') ?? 1) || 1),
  }
}

export function writeBotFilter(base: URLSearchParams, f: Partial<BotListFilter>): URLSearchParams {
  const next = new URLSearchParams(base)
  setOrDelete(next, 'q', f.q)
  setOrDelete(next, 'status', f.status)
  setOrDelete(next, 'node', f.node)
  setOrDelete(next, 'step', f.step)
  setOrDelete(next, 'error', f.error)
  if (f.page != null && f.page > 1) next.set('page', String(f.page))
  else next.delete('page')
  return next
}

export function readFailureFilter(params: URLSearchParams): FailureListFilter {
  return {
    category: params.get('category') || undefined,
    errorCode: params.get('errorCode') || undefined,
    botUuid: params.get('botUuid') || undefined,
    node: params.get('node') || undefined,
    step: params.get('step') || undefined,
    page: Math.max(1, Number(params.get('page') ?? 1) || 1),
  }
}

export function writeFailureFilter(
  base: URLSearchParams,
  f: Partial<FailureListFilter>,
): URLSearchParams {
  const next = new URLSearchParams(base)
  setOrDelete(next, 'category', f.category)
  setOrDelete(next, 'errorCode', f.errorCode)
  setOrDelete(next, 'botUuid', f.botUuid)
  setOrDelete(next, 'node', f.node)
  setOrDelete(next, 'step', f.step)
  if (f.page != null && f.page > 1) next.set('page', String(f.page))
  else next.delete('page')
  return next
}

function setOrDelete(params: URLSearchParams, key: string, value: string | undefined) {
  if (value) params.set(key, value)
  else params.delete(key)
}

export const FAILURE_CATEGORIES = [
  'target',
  'executor',
  'network',
  'scenario',
  'internal',
] as const
