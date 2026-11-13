/**
 * @file 侧栏指标数值的显示格式（纯函数，便于针对性测试）。
 *
 * 【为什么单独成文件】这些规则被两处消费（行尾与悬停卡），而它们曾经**只实现了一份、
 * 另一份漏了**——行尾把人数取整了，悬停卡直接输出原始 `avg`，于是出现
 * `18.208000000000002` 这种双精度尾巴。抽成同一份实现后，两处不可能再分叉。
 *
 * 共同口径：
 * - **人数只能是整数**。`inst_players_online` / `inst_players_max` 是时序库里的浮点均值
 *   （`avg`），原样输出会带出 `18.208000000000002` 这类尾巴；语义上「人在线」是计数。
 * - **TPS 保留一位小数**。先按 1 位量化再 `toFixed(1)`，避免 `Math.round(x*10)/10`
 *   之后的二进制表示再被 `toString` 展开成 `18.200000000000003`。
 * - **缺测不写占位符**（侧栏极窄，`--` 比空白更干扰扫读）；由调用方决定空串如何呈现。
 */

/** 人数：四舍五入到整数（`null` 视为缺测，返回空串）。 */
export function formatPlayers(players: number | null): string {
  return players === null ? '' : String(Math.round(players))
}

/**
 * 人数「当前/最大」。
 *
 * 三种情形：
 * - 都有值 → `18/50`；
 * - 只有当前人数（上限缺测，如代理未提供 maxplayers）→ `18人`；
 * - 当前人数缺测 → 空（上限单独没有展示意义）。
 *
 * 上限取整的理由同 {@link formatPlayers}：`inst_players_max` 也是浮点均值，
 * 「上限 50.4 人」既无意义也不该出现小数。
 */
export function formatPlayersRatio(players: number | null, playersMax: number | null): string {
  if (players === null) return ''
  const current = Math.round(players)
  if (playersMax === null) return `${current}人`
  return `${current}/${Math.round(playersMax)}`
}

/**
 * TPS：保留一位小数。
 *
 * 量化后再 `toFixed(1)` 是必要的两步：直接 `toFixed(1)` 在处理某些浮点值时会先放大误差
 * （例如末点本身就是 `18.208000000000002`，`toFixed(1)` 得到 `18.2` 尚可，但
 * `x*10/10` 的中间结果若不先取整，`toString` 可能展开成 `18.200000000000003`）。
 */
export function formatTps(tps: number | null): string {
  return tps === null ? '' : (Math.round(tps * 10) / 10).toFixed(1)
}

/** 百分比：整数（CPU 占用）。 */
export function formatCpuPct(pct: number | null): string {
  return pct === null ? '' : `${Math.round(pct)}%`
}

/** 字节 → GB，两位小数（堆内存）。 */
export function formatMemGb(bytes: number | null): string {
  return bytes === null ? '' : `${(bytes / 1024 ** 3).toFixed(2)}G`
}
