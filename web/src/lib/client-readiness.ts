/**
 * 客户端分发频道「就绪度」步骤推导（FR-187）。
 *
 * 频道工作台顶部常驻就绪度步骤器的纯逻辑：把频道当前状态（密钥数、当前版本）
 * 推导成「创建频道 → 拉取密钥 → 发布版本 → 接入启动器」四步的完成情况，
 * 并选出第一个未完成步骤作为「当前引导步骤」（高亮 + CTA）。
 *
 * 纯函数、无 React/DOM 依赖，便于单测；UI 仅消费其输出渲染。
 */

/** 就绪度四步的稳定标识（决定步骤顺序与 CTA 路由 tab）。 */
export type ReadinessStepId = 'channel' | 'keys' | 'version' | 'integrate'

/** 单个步骤的推导结果。 */
export interface ReadinessStep {
  id: ReadinessStepId
  /** 是否已完成（完成步骤折叠为 ✓）。 */
  done: boolean
  /** 是否为当前应引导用户去做的步骤（第一个未完成步骤）。 */
  current: boolean
}

/** 推导就绪度所需的频道状态快照（来自频道详情接口）。 */
export interface ReadinessInput {
  /** 频道下密钥数量。 */
  keyCount: number
  /** 当前 latest 版本号（0=未发布）。 */
  currentVersion: number
}

/** 步骤固定顺序（创建 → 密钥 → 版本 → 接入）。 */
export const READINESS_ORDER: ReadinessStepId[] = ['channel', 'keys', 'version', 'integrate']

/**
 * 由频道状态推导四步就绪度。
 *
 * 完成判定：
 * - channel：到达工作台即已创建，恒为 true。
 * - keys：keyCount > 0。
 * - version：currentVersion > 0（已发布过 latest）。
 * - integrate：建密钥且已发版后即认为「可接入」（接入指引为只读照做项，
 *   无独立后端状态，故以「密钥+版本都就绪」作为可接入信号）。
 *
 * 「当前步骤」= 顺序上第一个未完成步骤；全部完成时无当前步骤（current 均 false）。
 */
export function deriveReadiness(input: ReadinessInput): ReadinessStep[] {
  const hasKeys = input.keyCount > 0
  const hasVersion = input.currentVersion > 0
  const doneMap: Record<ReadinessStepId, boolean> = {
    channel: true,
    keys: hasKeys,
    version: hasVersion,
    integrate: hasKeys && hasVersion,
  }

  const firstIncomplete = READINESS_ORDER.find((id) => !doneMap[id])

  return READINESS_ORDER.map((id) => ({
    id,
    done: doneMap[id],
    current: id === firstIncomplete,
  }))
}

/** 已完成步骤数（用于进度条/“x/4 已完成”文案）。 */
export function readinessCompletedCount(steps: ReadinessStep[]): number {
  return steps.filter((s) => s.done).length
}

/** 频道是否全部就绪（四步全完成）。 */
export function isChannelReady(steps: ReadinessStep[]): boolean {
  return steps.every((s) => s.done)
}
