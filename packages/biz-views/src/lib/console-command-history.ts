/**
 * 控制台命令历史（FR-416）：按实例持久化 + `^R` 模糊搜索。
 *
 * **为什么按实例分桶**：历史的价值在「上次我在这个服上是怎么弄的」。把 20 个服的历史
 * 混成一条全局列表后，↑ 翻上来的多半是别的服的命令，还可能把「只该在测试服跑的命令」
 * 送进生产服。故存储键带 instanceId，互不污染。
 *
 * **为什么落 localStorage**：FR-415 的历史只活在组件 state 里，刷新即失忆——而排障恰恰
 * 常伴随刷新页面。历史属本机操作习惯而非需要跨设备同步的业务数据，故不入库、不上后端。
 *
 * 读写全部包 try/catch：隐私模式下 localStorage 存取会抛，历史丢了不该把控制台带崩。
 */

/** 每实例历史上限（FR-416）。超限丢最旧。 */
export const COMMAND_HISTORY_LIMIT = 500

const KEY_PREFIX = 'console.cmdHistory.'

function storageKey(instanceId: number): string {
  return `${KEY_PREFIX}${instanceId}`
}

/**
 * 追加一条到历史（纯函数，旧→新）。
 *
 * 相邻重复不入：连按三次 `list` 不该占掉三个坑位，否则 ↑ 要按三次才退到上一条**不同**的命令。
 * 只去相邻而不全局去重——「a b a」里第二个 a 是有意义的时序信息（用户在 b 之后又回到 a）。
 */
export function appendCommandHistory(history: readonly string[], line: string): string[] {
  const value = line.trim()
  if (!value) return history.slice()
  if (history.length > 0 && history[history.length - 1] === value) return history.slice()
  return [...history, value].slice(-COMMAND_HISTORY_LIMIT)
}

/** 读取该实例历史（旧→新）。存储缺失/损坏一律当空历史，不抛。 */
export function loadCommandHistory(instanceId: number): string[] {
  try {
    const raw = localStorage.getItem(storageKey(instanceId))
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    // 逐项校验：外部可写的存储不能假定形状，混进非字符串会让 ↑ 填出 "[object Object]"。
    return parsed.filter((item): item is string => typeof item === 'string' && item.length > 0).slice(-COMMAND_HISTORY_LIMIT)
  } catch {
    return []
  }
}

/** 写回该实例历史。隐私模式写失败即静默放弃（本次会话内的内存历史仍可用）。 */
export function saveCommandHistory(instanceId: number, history: readonly string[]): void {
  try {
    localStorage.setItem(storageKey(instanceId), JSON.stringify(history.slice(-COMMAND_HISTORY_LIMIT)))
  } catch {
    // 隐私模式 / 配额满：忽略。
  }
}

/** 追加并持久化，返回新历史（旧→新）。 */
export function pushCommandHistory(instanceId: number, line: string): string[] {
  const next = appendCommandHistory(loadCommandHistory(instanceId), line)
  saveCommandHistory(instanceId, next)
  return next
}

/** 清空该实例历史（测试隔离 / 用户主动清理）。 */
export function clearCommandHistory(instanceId: number): void {
  try {
    localStorage.removeItem(storageKey(instanceId))
  } catch {
    // 同上。
  }
}

/** 一条 `^R` 命中：`positions` 是 `value` 内被查询字符命中的下标（供高亮）。 */
export interface CommandHistoryMatch {
  value: string
  positions: number[]
}

/**
 * 子序列匹配（fzf 式）：查询的每个字符按顺序出现在候选中即命中，无需连续。
 * 大小写不敏感——`^R` 是找回记忆的工具，不该因为记错大小写就找不到。
 */
function subsequenceMatch(value: string, needle: string): number[] | null {
  const haystack = value.toLocaleLowerCase()
  const positions: number[] = []
  let at = 0
  for (const char of needle) {
    const found = haystack.indexOf(char, at)
    if (found < 0) return null
    positions.push(found)
    at = found + 1
  }
  return positions
}

/**
 * `^R` 模糊搜索历史（FR-416）。
 *
 * 结果**新→旧**排序：readline 的 `^R` 语义是「往回找最近一次」，最近用过的最可能是想找的。
 * 同一条命令的多次出现按值去重（只留最新那次）——`^R` 列表里同一行重复五遍纯属噪音。
 *
 * 空查询返回全部历史（新→旧），使 `^R` 一按下就能当「历史浏览器」用。
 */
export function searchCommandHistory(history: readonly string[], query: string): CommandHistoryMatch[] {
  const needle = query.trim().toLocaleLowerCase()
  const seen = new Set<string>()
  const out: CommandHistoryMatch[] = []
  for (let i = history.length - 1; i >= 0; i--) {
    const value = history[i]
    if (seen.has(value)) continue
    if (!needle) {
      seen.add(value)
      out.push({ value, positions: [] })
      continue
    }
    const positions = subsequenceMatch(value, needle)
    if (!positions) continue
    seen.add(value)
    out.push({ value, positions })
  }
  return out
}
