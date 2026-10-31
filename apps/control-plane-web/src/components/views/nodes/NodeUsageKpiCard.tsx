import type { LucideIcon } from 'lucide-react'

import { ResourceGauge } from '@jianmanager/ui/components/gauge'

/**
 * 节点页顶部的资源占用 KPI：环形仪表直接裸露，不套卡片。
 *
 * 【为什么不再包一张卡】状态类 KPI 用 StatCard 承载「图标 + 数字」是合适的；
 * 但资源占用本身已经是一个完整图形，再给它加边框、底色和阴影，等于在圆球外面
 * 又套一层矩形，视觉上抢走圆球的重心。这里只保留环 + 图标标签，让占用率自己说话，
 * 同时与左侧状态卡在同一行里保持高度对齐。
 *
 * 缺数时传 `null`：环走中性色、中心显示「—」，不会把「没采到」画成 0%。
 */
export function NodeUsageKpiCard({
  label,
  icon: Icon,
  value,
  hint,
}: {
  label: string
  icon: LucideIcon
  /** 占用百分比（0~100）；null 表示暂无数据。 */
  value: number | null
  hint: string
}) {
  return (
    <div
      className="flex h-full min-w-0 flex-col items-center justify-center gap-1"
      title={hint}
    >
      <ResourceGauge value={value ?? Number.NaN} size={48} thickness={5} unit="" />
      <span className="flex min-w-0 items-center gap-1 text-[11px] font-medium leading-tight text-foreground">
        <Icon className="size-3 shrink-0 text-primary" aria-hidden="true" />
        <span className="truncate">{label}</span>
      </span>
    </div>
  )
}
