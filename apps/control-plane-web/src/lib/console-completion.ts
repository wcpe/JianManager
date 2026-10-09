/**
 * 控制台命令补全（FR-416）。
 *
 * MC 服务端控制台不是 PTY（ADR-086），服务端不会回送补全候选——补全表只能放在前端。
 *
 * **命令表的来源**：这是**协议事实**而非业务数据——Minecraft Java 版 / Paper 服务端
 * 内置的控制台命令名（vanilla 1.21 `/help` 列表 + Paper 附加的
 * `tps` / `timings` / `plugins` / `version` / `jfr` / `perf`）。与 FR-415 之前
 * `components/Terminal.tsx` 里的同名常量同源；该文件的补全随旧 xterm 输入路径一并移除，
 * 此处补回（ADR-086 影响一节记的「已知回退」）。
 *
 * **玩家名不在此处硬编码**：玩家是运行时数据，由调用方从真实来源传入
 * （`/players` 在线聚合 + 控制台输出里的 join/quit/list 行），本模块只做匹配。
 */

/** Minecraft / Paper 服务端控制台命令名（协议事实，见文件头注释）。 */
export const MC_COMMANDS: readonly string[] = [
  'advancement', 'attribute', 'ban', 'ban-ip', 'banlist', 'bossbar', 'clear', 'clone', 'damage',
  'data', 'datapack', 'debug', 'defaultgamemode', 'deop', 'difficulty', 'effect', 'enchant',
  'execute', 'experience', 'fill', 'fillbiome', 'forceload', 'function', 'gamemode', 'gamerule',
  'give', 'help', 'item', 'jfr', 'kick', 'kill', 'list', 'locate', 'loot', 'me', 'msg', 'op',
  'pardon', 'pardon-ip', 'particle', 'perf', 'place', 'playsound', 'plugins', 'random', 'recipe',
  'reload', 'return', 'ride', 'rotate', 'save-all', 'save-off', 'save-on', 'say', 'schedule',
  'scoreboard', 'seed', 'setblock', 'setidletimeout', 'setworldspawn', 'spawnpoint', 'spectate',
  'spreadplayers', 'stop', 'stopsound', 'summon', 'tag', 'team', 'teammsg', 'teleport', 'tell',
  'tellraw', 'tick', 'time', 'timings', 'title', 'tp', 'tps', 'transfer', 'trigger', 'version',
  'w', 'weather', 'whitelist', 'worldborder', 'xp',
]

/** 第二段参数为玩家（或目标选择器）的命令——补全时给在线玩家名 / 选择器。 */
export const PLAYER_ARG_COMMANDS: ReadonlySet<string> = new Set([
  'kick', 'ban', 'pardon', 'op', 'deop', 'tp', 'teleport', 'gamemode', 'give', 'tell', 'msg', 'w',
  'kill', 'spectate', 'whitelist', 'clear', 'effect', 'enchant', 'experience', 'xp', 'title',
  'spawnpoint', 'teammsg',
])

/** 目标选择器。只在用户已敲出 `@` 时才作为候选，否则会淹没玩家名。 */
export const TARGET_SELECTORS: readonly string[] = ['@a', '@p', '@r', '@e', '@s']

/** 被补全的 token 属于哪一类（供 UI 区分图标/分组）。 */
export type CompletionKind = 'command' | 'player' | 'selector'

export interface CompletionState {
  kind: CompletionKind
  /** 按前缀过滤后的候选，保持来源顺序（命令表已按字典序）。 */
  candidates: string[]
  /** 被补 token 在原值中的起止下标（替换区间）。 */
  tokenStart: number
  tokenEnd: number
  /** token 原文（用户已敲的部分）。 */
  token: string
  /**
   * 全部候选的最长公共前缀（**候选自身的大小写**）。
   *
   * Tab 只把输入推进到这里就停手——这是「不盲补第一个候选」的技术含义：
   * 公共前缀是所有候选都认的部分，补上去不含任何猜测；再往后必须由用户选。
   */
  commonPrefix: string
  /**
   * 公共前缀中超出 token 长度的那一段（ghost 预览用）。
   *
   * 与 {@link commonPrefix} 分开存是因为大小写：用户敲 `st` 而候选是 `Steve`/`Stevie` 时，
   * 推进必须**替换**成 `Stev`（否则送出 `stev` 这个服务端不认的名字），而 ghost 只能
   * 显示后缀。
   */
  commonSuffix: string
}

/** 一组字符串的最长公共前缀（空集/单元素退化正确）。 */
export function longestCommonPrefix(items: readonly string[]): string {
  if (items.length === 0) return ''
  let prefix = items[0]
  for (const item of items.slice(1)) {
    let i = 0
    while (i < prefix.length && i < item.length && prefix[i] === item[i]) i++
    prefix = prefix.slice(0, i)
    if (!prefix) break
  }
  return prefix
}

/** 光标所在（或紧邻其左）的 token 边界。光标在空白处即视为一个新的空 token。 */
function tokenAt(value: string, caret: number): { start: number; end: number } {
  let start = caret
  while (start > 0 && !/\s/.test(value[start - 1])) start--
  return { start, end: caret }
}

/**
 * 计算当前光标位置可用的补全候选（FR-416）。
 *
 * 只补两处：**第一段命令名**与**玩家参数命令的第二段**。第三段起（坐标、物品 id、
 * gamerule 名…）不补——那些的候选集依赖服务端状态与版本，前端猜出来的候选给错方向
 * 比不给更糟。
 *
 * @param players 在线玩家名（真实来源由调用方提供，本模块不编造）
 * @returns 无可补时返回 null（含「候选集为空」与「不在可补位置」两种）
 */
export function computeCompletion(
  value: string,
  caret: number,
  players: readonly string[],
): CompletionState | null {
  const at = Math.max(0, Math.min(caret, value.length))
  const { start, end } = tokenAt(value, at)
  const token = value.slice(start, end)
  const before = value.slice(0, start)
  // 光标之前的完整词数：0 表示正在敲第一段（命令名）。
  const priorWords = before.trim() ? before.trim().split(/\s+/).length : 0

  let kind: CompletionKind
  let pool: readonly string[]
  if (priorWords === 0) {
    kind = 'command'
    pool = MC_COMMANDS
  } else if (priorWords === 1 && PLAYER_ARG_COMMANDS.has(before.trim().toLocaleLowerCase())) {
    // 已敲 `@` 才补选择器：否则 5 个选择器会挤在玩家名前面，常用路径反而变慢。
    if (token.startsWith('@')) {
      kind = 'selector'
      pool = TARGET_SELECTORS
    } else {
      kind = 'player'
      pool = players
    }
  } else {
    return null
  }

  const needle = token.toLocaleLowerCase()
  // 前缀不敏感匹配，但候选保留自身大小写：玩家名 `Steve` 敲 `st` 也要能补出 `Steve`。
  const candidates = pool.filter((item) => item.toLocaleLowerCase().startsWith(needle))
  if (candidates.length === 0) return null

  const commonPrefix = longestCommonPrefix(candidates)
  return {
    kind,
    candidates,
    tokenStart: start,
    tokenEnd: end,
    token,
    commonPrefix,
    commonSuffix: commonPrefix.length > token.length ? commonPrefix.slice(token.length) : '',
  }
}

/**
 * 把某个候选写回输入值：替换 token 区间，并补一个空格（下一段可以直接敲）。
 * 返回新值与新光标位。
 */
export function applyCompletion(
  value: string,
  state: CompletionState,
  candidate: string,
): { value: string; caret: number } {
  const head = value.slice(0, state.tokenStart)
  const tail = value.slice(state.tokenEnd)
  // 后面已经跟着空白就不再补空格，免得补出双空格。
  const separator = tail.startsWith(' ') ? '' : ' '
  const next = `${head}${candidate}${separator}${tail}`
  return { value: next, caret: head.length + candidate.length + separator.length }
}

/**
 * 把输入推进到公共前缀（多候选时 Tab 的行为，FR-416「不盲补」）。
 *
 * **替换** token 区间而非在末尾追加：用户敲 `op st` 而候选是 `Steve`/`Stevie` 时，
 * 追加会得到 `op stev`——一个服务端不认的玩家名（MC 玩家名大小写敏感）。替换得到
 * `op Stev`，既纠正了大小写，又仍是所有候选的公共前缀（未做任何选择）。
 *
 * 无可推进（公共前缀已等于所敲内容且大小写一致）时返回 null，调用方据此只开候选列表、不改输入。
 */
export function applyCommonPrefix(
  value: string,
  state: CompletionState,
): { value: string; caret: number } | null {
  if (state.commonPrefix === state.token) return null
  const head = value.slice(0, state.tokenStart)
  const tail = value.slice(state.tokenEnd)
  return { value: `${head}${state.commonPrefix}${tail}`, caret: head.length + state.commonPrefix.length }
}
