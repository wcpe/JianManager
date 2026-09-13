import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Panel } from '@jianmanager/ui/components/panel'
import type { ObservabilitySeriesPoint } from '@/api/clientDistObservability'

/**
 * 更新活动热力图（FR-427，GitHub 贡献图风格）：
 * 列=周、行=星期（一/三/五标注），每格=一天，颜色深浅=当天更新次数。
 * 纯前端消费既有小时桶 series（updateTotal，本地时区聚合到天）；空窗口显示友好空态。
 */

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

const WEEKDAY_NAMES: Record<string, string[]> = {
  zh: ['日', '一', '二', '三', '四', '五', '六'],
  en: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
}

interface DayCell {
  key: string
  label: string
  count: number
  future: boolean
}

/** 密度 → 5 档色阶（GitHub 口径：0 / 低 / 中 / 高 / 顶）。 */
function level(count: number, max: number): number {
  if (count <= 0 || max <= 0) return 0
  const ratio = count / max
  if (ratio <= 0.25) return 1
  if (ratio <= 0.5) return 2
  if (ratio <= 0.75) return 3
  return 4
}

const LEVEL_OPACITY = [0.07, 0.3, 0.52, 0.75, 1]

export function UpdateHeatmap({ series }: { series: ObservabilitySeriesPoint[] }) {
  const { t, i18n } = useTranslation()

  const weeks = useMemo(() => {
    const dayMap = new Map<string, number>()
    for (const p of series) {
      const d = new Date(p.ts)
      if (Number.isNaN(d.getTime())) continue
      const key = `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
      dayMap.set(key, (dayMap.get(key) ?? 0) + p.updateTotal)
    }
    if (dayMap.size === 0) return { weeks: [] as DayCell[][], max: 0, monthMarks: [] as { col: number; label: string }[] }

    const keys = [...dayMap.keys()].sort()
    const first = new Date(keys[0] + 'T00:00:00')
    const last = new Date(keys[keys.length - 1] + 'T00:00:00')

    // 网格从首日的所在周开头（周日）起，按周分列；一周一行 7 格。
    const gridStart = new Date(first)
    gridStart.setDate(gridStart.getDate() - gridStart.getDay())
    const today = new Date()
    today.setHours(0, 0, 0, 0)

    const cols: DayCell[][] = []
    const monthMarks: { col: number; label: string }[] = []
    let lastMonth = -1
    const cursor = new Date(gridStart)
    for (let col = 0; cursor <= last; col++) {
      const colCells: DayCell[] = []
      const monthOfFirstInCol = cursor.getMonth()
      for (let row = 0; row < 7; row++) {
        const key = `${cursor.getFullYear()}-${pad2(cursor.getMonth() + 1)}-${pad2(cursor.getDate())}`
        const count = dayMap.get(key)
        const inRange = count !== undefined
        const future = cursor > today
        colCells.push({
          key,
          label: `${pad2(cursor.getMonth() + 1)}-${pad2(cursor.getDate())}`,
          count: inRange ? dayMap.get(key) ?? 0 : 0,
          future: future || !inRange ? cursor > today : false,
        })
        cursor.setDate(cursor.getDate() + 1)
      }
      // 月份标注：该列第一天的月份与上一列不同且非首列。
      if (col > 0 && monthOfFirstInCol !== lastMonth) {
        monthMarks.push({ col, label: `${monthOfFirstInCol + 1}${t('clientDistObs.heatmapMonth', '月')}` })
      }
      lastMonth = monthOfFirstInCol
      cols.push(colCells)
    }
    void last
    return { weeks: cols, max: Math.max(1, ...[...dayMap.values()]), monthMarks }
  }, [series, t])

  const weekdayNames = WEEKDAY_NAMES[i18n.language?.startsWith('zh') ? 'zh' : 'en'] ?? WEEKDAY_NAMES.en

  return (
    <Panel title={t('clientDistObs.heatmapTitle', '更新活动热力图')}>
      <p className="mb-3 text-xs text-muted-foreground">
        {t('clientDistObs.heatmapHint', '每格代表一天，颜色越深当天更新次数越多（GitHub 贡献图风格）。')}
      </p>
      {weeks.weeks.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground" data-testid="heatmap-empty">
          {t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
        </p>
      ) : (
        <div className="overflow-x-auto pb-1" data-testid="update-heatmap">
          <div className="inline-block min-w-full">
            {/* 月份标注行（与周列对齐） */}
            <div className="mb-1 flex gap-[3px] pl-8">
              {weeks.weeks.map((_, col) => {
                const mark = weeks.monthMarks.find((m) => m.col === col)
                return (
                  <div key={col} className="w-3 shrink-0 text-[10px] text-muted-foreground">
                    {mark ? mark.label : ''}
                  </div>
                )
              })}
            </div>
            <div className="flex gap-[3px]">
              {/* 星期标注列（仅一/三/五） */}
              <div className="flex w-8 shrink-0 flex-col gap-[3px]">
                {Array.from({ length: 7 }, (_, row) => (
                  <div key={row} className="flex h-3 items-center text-[10px] leading-3 text-muted-foreground">
                    {[1, 3, 5].includes(row) ? weekdayNames[row] : ''}
                  </div>
                ))}
              </div>
              {/* 周列 */}
              {weeks.weeks.map((col, ci) => (
                <div key={ci} className="flex flex-col gap-[3px]">
                  {col.map((cell) => {
                    const lv = cell.future ? -1 : level(cell.count, weeks.max)
                    return (
                      <div
                        key={cell.key}
                        className="h-3 w-3 rounded-[2px] bg-primary"
                        style={{ opacity: lv >= 0 ? LEVEL_OPACITY[lv] : 0.04 }}
                        title={
                          cell.future
                            ? cell.label
                            : `${cell.label} · ${t('clientDistObs.heatmapCellUpdates', { defaultValue: '{{n}} 次更新', n: cell.count })}`
                        }
                        data-level={lv}
                        data-date={cell.key}
                      />
                    )
                  })}
                </div>
              ))}
            </div>
            {/* 图例 */}
            <div className="mt-2 flex items-center justify-end gap-1.5 text-[10px] text-muted-foreground">
              <span>{t('clientDistObs.heatmapLess', '少')}</span>
              {LEVEL_OPACITY.map((o) => (
                <div key={o} className="size-3 rounded-[2px] bg-primary" style={{ opacity: o }} />
              ))}
              <span>{t('clientDistObs.heatmapMore', '多')}</span>
            </div>
          </div>
        </div>
      )}
    </Panel>
  )
}

export default UpdateHeatmap
