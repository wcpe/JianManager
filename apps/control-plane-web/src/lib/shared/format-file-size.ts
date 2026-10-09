/**
 * 「文件体积 / 存储占用」字节格式化的**唯一实现**（全产品口径）。
 *
 * ## 为什么立这一份
 * 同一个语义此前在仓库里散落 26 份同义实现（`formatBytes` / `formatSize` / `fmtBytes` /
 * `formatCacheBytes` / `formatTotalSize` 等），且已经分叉出三类可见不一致：
 * 1. 12 处**没有 GB 档**，导致 2 GiB 被显示成 `2048.0 MB`（explorer / file-browser / client-dist 等）；
 * 2. 4 处用 `KiB/MiB/GiB`、其余用 `KB/MB/GB`，同一产品里两种后缀并存；
 * 3. 精度各不相同（`toFixed(1)` / `toFixed(0)` / `toFixed(2)` / 「≥10 不保留小数」/「≥100 取整」）。
 * 本模块把上述三类统一到一套规则，调用方不再各自实现。
 *
 * ## 统一口径（选定理由）
 * - **后缀取 `B/KB/MB/GB/TB`（不是 `KiB/MiB/GiB`）**：仓库里 22/26 处既有实现用这套后缀，
 *   是事实上的产品惯例；且本模块服务的是「文件体积」（文件管理器、制品、插件包、客户端分发包），
 *   这些界面周边文案与用户预期都按 `KB/MB/GB` 走。`KiB/MiB/GiB` 的 4 处据此收敛。
 * - **除数是 1024**：即此处 `KB` 表示 1024 字节。这是本产品的既有约定（所有被收敛的实现都用
 *   1024 而非 1000），保持二进制除数的实际行为不变，只统一后缀写法。
 * - **非字节档统一保留 1 位小数**：`toFixed(1)` 是 19/26 处既有实现的口径，改动面最小；
 *   代价是整数值会显示 `1.0 KB` 而非 `1 KB`（原 storage-view / runtime-assets 的写法），
 *   为求全仓一致接受这一写法。
 * - **`< 1024` 直接输出字节数**：与绝大多数被收敛实现一致（不做 0~999 的 K 缩写）。
 *
 * ## 与另外几份「不合并」的字节实现的分工
 * - `lib/metrics/metrics-format.ts` 的 `fmtBytes`、`packages/ui/src/lib/monitor-metrics.ts` 的
 *   `formatBytes`：那是**图表 Y 轴 / 排行榜**的紧凑口径（`G/M/K`、无空格、阈值按 1e3），
 *   服务于「窄轴里把大数压短」，与文件体积的 `1.0 KB` 写法不是同一语义，**保留不合并**。
 *   本模块不调用它们，它们也不调用本模块。
 * - `lib/backups/backup.ts` 的 `formatSizeMb`：入参单位是 **MB**（后端 DTO 就是 MB 字段，
 *   不是字节），且空值口径是 `0.0 MB`，语义不同，保留。
 * - `components/views/console/QuotaPanel.tsx` 的 `formatMiB`：配额用量列的口径是
 *   `MiB/GiB`（该界面以 MiB 为最小单位、0 值显示 `—`），属配额域，另计。
 */

/** 1024 进制单位阶梯；索引 i 对应 1024^i 字节。末档 TB 之后不再进位。 */
const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const

export interface FormatFileSizeOptions {
  /**
   * 0 / 负值 / `null` / `undefined` / 非有限数时的占位文案，缺省 `'0 B'`。
   * 少数界面需要自己的占位（如进程页 `'--'`、Bot 会话页 `'—'`、JVM 卡片 `'0'`），
   * 用本项保留其原有对外表现，避免为占位文案再抄一份格式化逻辑。
   */
  fallback?: string
}

/**
 * 字节 → 人类可读体积，如 `1.0 KB` / `512 B` / `2.0 GB`。
 *
 * 边界行为（全仓统一，取代各旧实现的分歧）：
 * - `0` / 负值 / `null` / `undefined` / `NaN` / `±Infinity` → `options.fallback`（缺省 `'0 B'`）；
 * - `< 1024` → 原始字节数（整数不加小数，非整数保留 1 位）；
 * - 其余按 1024 逐档进位到 `KB/MB/GB/TB`，每档保留 1 位小数；
 * - 超过 TB 仍在 `TB` 档输出（如 `2048.0 TB`），不引入 PB 或退回裸数字。
 */
export function formatFileSize(bytes: number | null | undefined, options: FormatFileSizeOptions = {}): string {
  const { fallback = '0 B' } = options
  const n = Number(bytes)
  if (!Number.isFinite(n) || n <= 0) return fallback
  if (n < 1024) return `${Number.isInteger(n) ? n : n.toFixed(1)} B`

  let value = n
  let unit = 0
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(1)} ${UNITS[unit]}`
}
