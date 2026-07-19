import type {
  ActionStartResult,
  ActionTickResult,
  ScenarioAction,
  ScenarioActionContext,
  ScenarioActionSignal,
  ScenarioStep,
} from './types.js'

const TEMPLATE_PATTERN = /{{\s*([A-Za-z][A-Za-z0-9]*)\s*}}/g
const TEMPLATE_VARIABLES = new Set([
  'botName', 'botUuid', 'runId', 'cohortKey', 'correlationToken', 'roomKey',
])

abstract class BaseScenarioAction implements ScenarioAction {
  abstract start(ctx: ScenarioActionContext): Promise<ActionStartResult>
  abstract tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult>
  async cancel(): Promise<void> {}
  async dispose(): Promise<void> {}
}

class WaitSpawnAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    return this.result(ctx)
  }

  async tick(ctx: ScenarioActionContext): Promise<ActionTickResult> {
    return this.result(ctx)
  }

  private result(ctx: ScenarioActionContext): ActionTickResult {
    if (ctx.capabilities.isSpawned()) {
      return { state: 'succeeded', result: { spawned: true } }
    }
    const reason = ctx.capabilities.connectionEndReason()
    if (reason) {
      return { state: 'failed', errorCode: 'CONNECT_ENDED', message: `连接已结束: ${reason}` }
    }
    return { state: 'running', result: { type: 'waiting', wait: 'spawn' } }
  }
}

class WaitAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    return this.result(ctx, ctx.startedAt)
  }

  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    return this.result(ctx, now)
  }

  private result(ctx: ScenarioActionContext, now: number): ActionTickResult {
    const durationMs = numberField(ctx.step.durationMs)
    if (durationMs === undefined) return invalidStep('wait 缺少 durationMs')
    if (now - ctx.startedAt >= durationMs) {
      return { state: 'succeeded', result: { durationMs } }
    }
    return { state: 'running', result: { type: 'waiting', wait: 'duration', durationMs } }
  }
}

class SendCommandAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.command !== 'string') return invalidStep('send_command 缺少 command')
    ctx.newCorrelationToken()
    const expanded = expandTemplate(ctx.step.command, ctx.templateVariables())
    if (expanded.error) return invalidStep(expanded.error)
    ctx.capabilities.chat(expanded.value)
    return {
      state: 'succeeded',
      correlationToken: ctx.currentCorrelationToken(),
      result: { sent: true },
    }
  }

  async tick(): Promise<ActionTickResult> {
    return { state: 'succeeded', result: { sent: true } }
  }
}

class WaitProbeEventAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.event !== 'string' || ctx.step.event === '') {
      return invalidStep('wait_probe_event 缺少 event')
    }
    return {
      state: 'running',
      correlationToken: ctx.ensureCorrelationToken(),
      result: { type: 'waiting', wait: 'external', eventType: ctx.step.event },
    }
  }

  async tick(): Promise<ActionTickResult> {
    return { state: 'running' }
  }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const eventType = probeEventType(signal)
    if (eventType !== ctx.step.event) {
      return { state: 'running', message: '探针事件类型不匹配' }
    }
    return {
      state: 'succeeded',
      signalAccepted: true,
      result: { eventType, payload: signal.payload ?? {} },
    }
  }
}

class BarrierAction extends BaseScenarioAction {
  private releaseAtUnixMs: number | undefined

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.key !== 'string' || !isBarrierRelease(ctx.step.release)) {
      return invalidStep('barrier 缺少 key 或 release')
    }
    return {
      state: 'running',
      correlationToken: ctx.ensureCorrelationToken(),
      result: barrierArrivedResult(ctx),
    }
  }

  async tick(_ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (this.releaseAtUnixMs !== undefined && now >= this.releaseAtUnixMs) {
      return { state: 'succeeded', result: { releaseAtUnixMs: this.releaseAtUnixMs } }
    }
    return { state: 'running' }
  }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const payload = recordValue(signal.payload)
    if (signal.type !== 'barrier-release' || payload?.round !== ctx.attempt) {
      return { state: 'running', message: '屏障释放类型或 round 不匹配' }
    }
    const releaseAt = numberField(payload.releaseAtUnixMs)
    if (releaseAt === undefined) return { state: 'running', message: '屏障释放时间无效' }
    this.releaseAtUnixMs = releaseAt
    return { state: 'running', signalAccepted: true, result: { releaseAtUnixMs: releaseAt } }
  }
}

class UnsupportedAction extends BaseScenarioAction {
  constructor(private readonly type: string) {
    super()
  }

  async start(): Promise<ActionStartResult> {
    return invalidStep(`本段未实现动作类型 ${this.type}`)
  }

  async tick(): Promise<ActionTickResult> {
    return invalidStep(`本段未实现动作类型 ${this.type}`)
  }
}

export function createScenarioAction(step: ScenarioStep): ScenarioAction {
  switch (step.type) {
    case 'wait_spawn': return new WaitSpawnAction()
    case 'wait': return new WaitAction()
    case 'send_command': return new SendCommandAction()
    case 'wait_probe_event': return new WaitProbeEventAction()
    case 'barrier': return new BarrierAction()
    default: return new UnsupportedAction(step.type)
  }
}

function barrierArrivedResult(ctx: ScenarioActionContext): object {
  return {
    type: 'barrier-arrived',
    stageIndex: ctx.stageIndex,
    cohortKey: ctx.cohortKey,
    barrierKey: ctx.step.key,
    round: ctx.attempt,
    release: ctx.step.release,
    timeoutPolicy: ctx.step.timeoutPolicy ?? 'fail',
    deadlineUnixMs: ctx.deadline,
  }
}

function expandTemplate(
  template: string,
  variables: Readonly<Record<string, string | undefined>>
): { value: string; error?: string } {
  let error: string | undefined
  const value = template.replace(TEMPLATE_PATTERN, (_match, name: string) => {
    if (!TEMPLATE_VARIABLES.has(name)) {
      error = `未知模板变量 ${name}`
      return ''
    }
    const replacement = variables[name]
    if (replacement === undefined || replacement === '') {
      error = `模板变量 ${name} 缺少值`
      return ''
    }
    return replacement
  })
  if (!error && (value.includes('{{') || value.includes('}}'))) error = '模板表达式格式无效'
  return { value, error }
}

function probeEventType(signal: ScenarioActionSignal): string | undefined {
  if (signal.type !== 'probe') return signal.type
  return recordValue(signal.payload)?.eventType as string | undefined
}

function invalidStep(message: string): ScenarioActionResultInternal {
  return { state: 'failed', errorCode: 'ACTION_INTERNAL_ERROR', message }
}

type ScenarioActionResultInternal = ActionStartResult & ActionTickResult

function numberField(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function isBarrierRelease(value: unknown): boolean {
  const release = recordValue(value)
  return release?.type === 'all' || release?.type === 'count' || release?.type === 'percent'
}
