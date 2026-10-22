import { useTranslation } from 'react-i18next'
import { ToggleGroup, ToggleGroupItem } from '../components/toggle-group'
import { MonitorChart } from './MonitorChart'
import {
  buildCompareSeries,
  catalogFor,
  compactFormatter,
  formatterFor,
  type RawSeries,
} from '../lib/monitor-metrics'

/**
 * 多指标对比/叠加（FR-221）：用户从该 target 的指标目录里勾选多条指标，叠加到同一图对比趋势。
 * 跨指标量纲不同（TPS vs 字节 vs %），故对比图 Y 轴不约束（auto），以形状/趋势对比为主，
 * 绝对值看 hover 浮窗。Y 轴刻度：选中序列格式一致时用该单位格式器，混合量纲退紧凑 SI 缩写
 * （避免字节级原始数字截断）。选中集合受控由父级持有（随 target 切换重置）。
 */
export function MetricComparePanel({
  kind,
  raw,
  selected,
  onToggle,
  height = 220,
}: {
  kind: 'platform' | 'node' | 'instance'
  raw: RawSeries[]
  selected: string[]
  onToggle: (metricKey: string) => void
  height?: number
}) {
  const { t } = useTranslation()
  const catalog = catalogFor(kind)
  const plot = buildCompareSeries(selected, catalog, raw, (k) => t(k))
  // 全部序列同 format → 用该单位格式器；混合量纲 → 紧凑缩写（hover 已按各序列 format 渲染）。
  const formats = [...new Set(plot.map((s) => s.format))]
  const valueFormatter = formats.length === 1 && formats[0] ? formatterFor(formats[0]) : compactFormatter

  /**
   * ToggleGroup 交回的是**整个选中集合**，而本组件的对外契约是「切换某一条」
   * （`onToggle(metricKey)`，父级据此维护选中集合）。这里把整集差分成单键事件，
   * 从而不必改动父级的接口：一次点击只会新增或移除一项，故取首个差异即可。
   */
  const handleValueChange = (next: string[]) => {
    const changed = next.find((k) => !selected.includes(k)) ?? selected.find((k) => !next.includes(k))
    if (changed) onToggle(changed)
  }

  return (
    <div className="space-y-3">
      <p className="text-[11px] text-muted-foreground">{t('monitor.compare.hint')}</p>
      {/*
        多选指标开关 → ToggleGroup type="multiple"：这正是 Segments 表达不了的那一半
        （Segments 是 role=tablist 的单选；此处每个指标独立开合、可同时选多条叠加对比）。
        换原语的实质收益：分组语义（role=group + aria-label）与「哪个开着」由 Radix 用
        aria-pressed 维护，方向键在项间移动、空格切换，不必每项各写一遍 onClick/aria。
        视觉仍是本站既有的 pill（rounded-full 描边）：toggleVariants 默认给的按钮尺寸/矩形圆角
        与 data-[state=on] 底色在此显式覆盖（twMerge 后写胜出），以免与「同一份数据的几种
        呈现就地切换」的 segment 外观混淆——那套留给 Segments。
      */}
      <ToggleGroup
        type="multiple"
        value={selected}
        onValueChange={handleValueChange}
        aria-label={t('monitor.compare.pick')}
        className="flex flex-wrap gap-1.5"
      >
        {catalog.map((item) => {
          const on = selected.includes(item.metricKey)
          return (
            <ToggleGroupItem
              key={item.metricKey}
              value={item.metricKey}
              className={
                // h-auto/min-w-0 抵掉 toggleVariants 的按钮盒；data-[state=on] 覆盖其默认 on 底色。
                'h-auto min-w-0 rounded-full border px-2.5 py-0.5 text-xs font-medium transition-colors ' +
                'data-[state=on]:bg-primary data-[state=on]:text-primary-foreground ' +
                (on
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-transparent hover:text-foreground')
              }
            >
              {t(item.nameKey)}
            </ToggleGroupItem>
          )
        })}
      </ToggleGroup>
      {selected.length === 0 ? (
        <div className="flex items-center justify-center text-xs text-muted-foreground" style={{ height }}>
          {t('monitor.compare.empty')}
        </div>
      ) : (
        <MonitorChart
          series={plot}
          height={height}
          valueFormatter={valueFormatter}
          yAxisWidth={56}
          emptyHint={t('common.noData')}
        />
      )}
    </div>
  )
}
