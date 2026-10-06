import type { ReactNode } from 'react'

/**
 * `metrics` Tab 分流器（FR-448 §2.1）：同一 Tab 内按探针有无呈现**两套样式**——
 * - 有探针 → ServerProbe 全量指标（TPS/MSPT/世界/区块）；
 * - 无探针 → 轻量进程/直探视图（ProcessPanel + 直探摘要）。
 *
 * 受控视图（ADR-097）：**分流判定上移外壳**——它要读能力画像与探针可用性两处，
 * 都是应用侧知识（判定函数 `resolveMetricsTabStyle` 也留在应用侧）。视图只按结果渲染，
 * 两个分支均由外壳注入：全量指标视图尚未迁包，轻量视图虽已迁包但需外壳取数。
 */
export interface MetricsTabSegmentProps {
  /** 分流结果（外壳按能力画像 + 探针可用性判定；'lightweight' = 无探针的轻量视图）。 */
  style: 'probe' | 'lightweight'
  /** 有探针时的全量指标视图（外壳注入，自带取数）。 */
  probeSlot?: ReactNode
  /** 无探针时的轻量视图（外壳注入）。 */
  fallbackSlot?: ReactNode
}

export default function MetricsTabSegment({ style, probeSlot, fallbackSlot }: MetricsTabSegmentProps) {
  return <>{style === 'probe' ? probeSlot : fallbackSlot}</>
}
