import { stripAnsi } from './console-ansi'

/**
 * 从控制台输出推断在线玩家（FR-416 Tab 补全的数据源之一）。
 *
 * **为什么不只用 `/players` 接口**：那条聚合接口依赖 ServerProbe 探针在位（FR-067），
 * 探针没装/没连时返回空——而补全恰恰在「刚开服、还没装探针、要 op 一个人」时最有用。
 * 控制台输出是**始终存在**的真实来源：玩家进出服务端一定会打 join/quit 行。
 *
 * 两个来源取并集（调用方负责合并）：接口给权威名册，输出给零依赖兜底。
 * 两者都不是编造——本模块不内置任何玩家名。
 */

/** MC 玩家名字符集与长度上限（1.7+ 为 3-16 位字母数字下划线）。 */
const JOINED = /([A-Za-z0-9_]{1,16}) joined the game/
const LEFT = /([A-Za-z0-9_]{1,16}) left the game/

/** `/list` 输出尾部的名单：`There are 2 of a max of 20 players online: a, b`。 */
const LIST_TAIL = /players online:\s*(.+)$/

/**
 * 按一行输出更新玩家集合（**原地改**，返回是否有变更）。
 *
 * `/list` 的输出被当作**权威快照**整体替换集合：它比累加的 join/quit 更可靠
 * （面板可能在服务器已运行一段时间后才连上终端，错过了先前的 join 行）。
 */
export function applyPlayerLine(players: Set<string>, rawLine: string): boolean {
  // 复用 FR-415 的 ANSI 剥离器（含控制字符吞除）：Paper 给玩家名套色，
  // 不剥掉的话玩家名被转义码包裹，正则匹配不到。
  const line = stripAnsi(rawLine)

  const list = LIST_TAIL.exec(line)
  if (list) {
    const names = list[1]
      .split(/,\s*/)
      .map((name) => name.trim())
      .filter((name) => /^[A-Za-z0-9_]{1,16}$/.test(name))
    const changed = names.length !== players.size || names.some((name) => !players.has(name))
    if (!changed) return false
    players.clear()
    for (const name of names) players.add(name)
    return true
  }

  const joined = JOINED.exec(line)
  if (joined && !players.has(joined[1])) {
    players.add(joined[1])
    return true
  }

  const left = LEFT.exec(line)
  if (left && players.has(left[1])) {
    players.delete(left[1])
    return true
  }
  return false
}

/**
 * 按整段输出（可能含多行、可能以半行结尾）更新玩家集合。
 *
 * 返回未消费的尾段：WS 分包会把一行切两半，不留着拼下一段会漏掉跨包的 join 行。
 */
export function applyPlayerChunk(
  players: Set<string>,
  pending: string,
  chunk: string,
): { pending: string; changed: boolean } {
  const parts = (pending + chunk).split(/\r\n|\n|\r/)
  const tail = parts.pop() ?? ''
  let changed = false
  for (const part of parts) {
    if (applyPlayerLine(players, part)) changed = true
  }
  return { pending: tail, changed }
}
