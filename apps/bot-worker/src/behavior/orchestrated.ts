/**
 * 编排行为模式。
 * 按阶段配置延迟启动、切换内部行为，并在阶段变化时上报事件。
 */

import type { Bot } from 'mineflayer'
import { Behavior } from './base.js'
import type { CustomBehaviorConfig } from './custom.js'

export type OrchestratedPhaseBehavior = 'idle' | 'follow' | 'patrol' | 'guard' | 'custom'

/** 单个编排阶段配置。 */
export interface OrchestratedBehaviorPhase {
  /** 阶段持续时间（毫秒）。 */
  durationMs: number
  /** 阶段内部行为。 */
  behavior: string
  /** follow/patrol/guard 等行为的目标参数。 */
  target?: string
  /** custom 行为配置。 */
  config?: CustomBehaviorConfig
}

/** 编排行为配置。 */
export interface OrchestratedBehaviorConfig {
  /** 是否循环播放所有阶段。 */
  loop?: boolean
  /** 首次进入阶段前的等待时间（毫秒）。 */
  startDelayMs?: number
  /** 阶段列表。 */
  phases: OrchestratedBehaviorPhase[]
}

/** 规范化后的单个编排阶段。 */
export interface NormalizedOrchestrationPhase {
  durationMs: number
  behavior: OrchestratedPhaseBehavior
  target?: string
  config?: CustomBehaviorConfig
}

/** 规范化后的编排配置。 */
export interface NormalizedOrchestratedConfig {
  loop: boolean
  startDelayMs: number
  totalDurationMs: number
  phases: NormalizedOrchestrationPhase[]
}

/** 阶段选择结果。 */
export interface SelectedOrchestrationPhase {
  index: number
  phase: NormalizedOrchestrationPhase
}

export type BehaviorFactory = (
  botId: string,
  behavior: string,
  targetOrConfig?: string | CustomBehaviorConfig
) => Behavior

export type OrchestrationEvent = {
  evt: 'bot-event'
  botId: string
  type: 'orchestration-phase'
  data: {
    phaseIndex: number
    behavior: OrchestratedPhaseBehavior
  }
}

export type OrchestrationEventSink = (event: OrchestrationEvent) => void

const allowedBehaviors = new Set<string>(['idle', 'follow', 'patrol', 'guard', 'custom'])

/** 将外部编排配置规范化并进行最小校验。 */
export function normalizeOrchestrationConfig(
  config: OrchestratedBehaviorConfig
): NormalizedOrchestratedConfig {
  if (!Array.isArray(config.phases) || config.phases.length === 0) {
    throw new Error('编排 phases 不能为空')
  }

  const startDelayMs = normalizeNonNegative(config.startDelayMs ?? 0, 'startDelayMs')
  const phases = config.phases.map((phase, index) => normalizePhase(phase, index))
  const totalDurationMs = phases.reduce((sum, phase) => sum + phase.durationMs, 0)

  return {
    loop: config.loop === true,
    startDelayMs,
    totalDurationMs,
    phases,
  }
}

/** 按已经过的阶段时间选择当前阶段。 */
export function selectOrchestrationPhase(
  config: NormalizedOrchestratedConfig,
  elapsedMs: number
): SelectedOrchestrationPhase {
  const safeElapsedMs = Math.max(0, elapsedMs)
  const phaseElapsedMs = config.loop
    ? safeElapsedMs % config.totalDurationMs
    : Math.min(safeElapsedMs, config.totalDurationMs - 1)

  let elapsedBeforePhase = 0
  for (let index = 0; index < config.phases.length; index++) {
    const phase = config.phases[index]
    elapsedBeforePhase += phase.durationMs
    if (phaseElapsedMs < elapsedBeforePhase) {
      return { index, phase }
    }
  }

  const lastIndex = config.phases.length - 1
  return { index: lastIndex, phase: config.phases[lastIndex] }
}

/**
 * OrchestratedBehavior 编排行为引擎。
 * 它只负责计时和阶段切换，实际动作委托给既有行为实现。
 */
export class OrchestratedBehavior extends Behavior {
  private readonly config: NormalizedOrchestratedConfig
  private readonly factory: BehaviorFactory
  private readonly emitEvent: OrchestrationEventSink
  private currentBehavior: Behavior | null = null
  private currentPhaseIndex = -1
  private phaseStartAtMs = 0

  constructor(
    botId: string,
    config: OrchestratedBehaviorConfig,
    factory: BehaviorFactory,
    emitEvent: OrchestrationEventSink = writeEvent
  ) {
    super(botId)
    this.config = normalizeOrchestrationConfig(config)
    this.factory = factory
    this.emitEvent = emitEvent
  }

  get name() { return 'orchestrated' }

  setMcBot(bot: Bot): void {
    super.setMcBot(bot)
    if (this.currentBehavior) {
      this.currentBehavior.setMcBot(bot)
    }
  }

  start(): void {
    super.start()
    this.currentPhaseIndex = -1
    this.phaseStartAtMs = Date.now() + this.config.startDelayMs
  }

  stop(): void {
    super.stop()
    if (this.currentBehavior) {
      this.currentBehavior.stop()
      this.currentBehavior = null
    }
  }

  async tick(): Promise<void> {
    if (!this.running) return

    const now = Date.now()
    if (now < this.phaseStartAtMs) return

    const selected = selectOrchestrationPhase(this.config, now - this.phaseStartAtMs)
    if (selected.index !== this.currentPhaseIndex) {
      this.switchPhase(selected)
    }

    if (this.currentBehavior) {
      await this.currentBehavior.tick()
    }
  }

  private switchPhase(selected: SelectedOrchestrationPhase): void {
    if (this.currentBehavior) {
      this.currentBehavior.stop()
    }

    this.currentPhaseIndex = selected.index
    this.currentBehavior = this.createInnerBehavior(selected.phase)
    if (this.mcBot) {
      this.currentBehavior.setMcBot(this.mcBot)
    }
    this.currentBehavior.start()
    this.emitPhaseEvent(selected)
  }

  private createInnerBehavior(phase: NormalizedOrchestrationPhase): Behavior {
    if (phase.behavior === 'custom') {
      return this.factory(this.botId, phase.behavior, phase.config ?? { steps: [] })
    }
    return this.factory(this.botId, phase.behavior, phase.target)
  }

  private emitPhaseEvent(selected: SelectedOrchestrationPhase): void {
    this.emitEvent({
      evt: 'bot-event',
      botId: this.botId,
      type: 'orchestration-phase',
      data: {
        phaseIndex: selected.index,
        behavior: selected.phase.behavior,
      },
    })
  }
}

function normalizePhase(
  phase: OrchestratedBehaviorPhase,
  index: number
): NormalizedOrchestrationPhase {
  const behavior = normalizeBehavior(phase.behavior, index)
  const normalized: NormalizedOrchestrationPhase = {
    durationMs: normalizePositive(phase.durationMs, `phases[${index}].durationMs`),
    behavior,
  }

  if (phase.target !== undefined) {
    normalized.target = phase.target
  }
  if (behavior === 'custom') {
    normalized.config = phase.config ?? { steps: [] }
  }

  return normalized
}

function normalizeBehavior(behavior: string, index: number): OrchestratedPhaseBehavior {
  if (!allowedBehaviors.has(behavior)) {
    throw new Error(`phases[${index}].behavior 不合法`)
  }
  return behavior as OrchestratedPhaseBehavior
}

function normalizePositive(value: number, name: string): number {
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`${name} 必须大于 0`)
  }
  return value
}

function normalizeNonNegative(value: number, name: string): number {
  if (!Number.isFinite(value) || value < 0) {
    throw new Error(`${name} 不能为负数`)
  }
  return value
}

function writeEvent(event: OrchestrationEvent): void {
  process.stdout.write(JSON.stringify(event) + '\n')
}
