import type { BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/api/botLoad'
import {
  COMMAND_ORCHESTRATION_V1,
  DEFAULT_STABLE_PROFILE,
  DEFAULT_STRICT_THRESHOLDS,
  targetBotsFromProfile,
} from './presets'

export type WizardStep = 'target' | 'connection' | 'commands' | 'profile' | 'preflight'

export const WIZARD_STEPS: WizardStep[] = ['target', 'connection', 'commands', 'profile', 'preflight']

/** 向导草稿状态（局部 store，不入全局持久化）。 */
export interface BotLoadWizardDraft {
  step: WizardStep
  instanceId: number | null
  name: string
  namePrefix: string
  config: { server: string; port: number; auth: 'offline'; version?: string }
  executorMode: 'auto' | 'manual'
  executorNodeIds: number[]
  commandSchedule: BotLoadCommandSchedule
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  /** 预检成功后的 planToken；任一字段变化须清空。 */
  planToken: string | null
  planExpiresAt: string | null
  runId: number | null
  templateId: number | null
  dirty: boolean
}

export type WizardAction =
  | { type: 'setStep'; step: WizardStep }
  | { type: 'patch'; patch: Partial<BotLoadWizardDraft> }
  | { type: 'setCommandSchedule'; schedule: BotLoadCommandSchedule }
  | { type: 'setLoadProfile'; profile: BotLoadProfile }
  | { type: 'setThresholds'; thresholds: BotLoadThresholds }
  | { type: 'setPreflight'; planToken: string; expiresAt: string; runId: number }
  | { type: 'invalidatePlan' }
  | { type: 'reset'; draft?: Partial<BotLoadWizardDraft> }
  | { type: 'loadTemplate'; templateId: number; name: string; schedule: BotLoadCommandSchedule; profile: BotLoadProfile; thresholds: BotLoadThresholds }

/** 创建默认草稿。 */
export function createDefaultDraft(partial?: Partial<BotLoadWizardDraft>): BotLoadWizardDraft {
  const loadProfile = partial?.loadProfile ?? { ...DEFAULT_STABLE_PROFILE }
  return {
    step: 'target',
    instanceId: null,
    name: '',
    namePrefix: 'load',
    config: { server: '127.0.0.1', port: 25565, auth: 'offline' },
    executorMode: 'auto',
    executorNodeIds: [],
    commandSchedule: structuredClone(COMMAND_ORCHESTRATION_V1),
    loadProfile,
    thresholds: { ...DEFAULT_STRICT_THRESHOLDS },
    planToken: null,
    planExpiresAt: null,
    runId: null,
    templateId: null,
    dirty: false,
    ...partial,
  }
}

/** 草稿 reducer：会让 plan 失效的字段变更自动 invalidatePlan。 */
export function wizardReducer(state: BotLoadWizardDraft, action: WizardAction): BotLoadWizardDraft {
  switch (action.type) {
    case 'setStep':
      return { ...state, step: action.step }
    case 'patch': {
      const next = { ...state, ...action.patch, dirty: true }
      if (shouldInvalidate(state, action.patch)) {
        next.planToken = null
        next.planExpiresAt = null
      }
      return next
    }
    case 'setCommandSchedule':
      return {
        ...state,
        commandSchedule: action.schedule,
        planToken: null,
        planExpiresAt: null,
        dirty: true,
      }
    case 'setLoadProfile':
      return {
        ...state,
        loadProfile: action.profile,
        planToken: null,
        planExpiresAt: null,
        dirty: true,
      }
    case 'setThresholds':
      return {
        ...state,
        thresholds: action.thresholds,
        planToken: null,
        planExpiresAt: null,
        dirty: true,
      }
    case 'setPreflight':
      return {
        ...state,
        planToken: action.planToken,
        planExpiresAt: action.expiresAt,
        runId: action.runId,
      }
    case 'invalidatePlan':
      return { ...state, planToken: null, planExpiresAt: null }
    case 'reset':
      return createDefaultDraft(action.draft)
    case 'loadTemplate':
      return {
        ...state,
        templateId: action.templateId,
        name: action.name,
        commandSchedule: action.schedule,
        loadProfile: action.profile,
        thresholds: action.thresholds,
        planToken: null,
        planExpiresAt: null,
        dirty: false,
      }
    default:
      return state
  }
}

const INVALIDATE_KEYS: Array<keyof BotLoadWizardDraft> = [
  'instanceId',
  'name',
  'namePrefix',
  'config',
  'executorMode',
  'executorNodeIds',
  'commandSchedule',
  'loadProfile',
  'thresholds',
]

function shouldInvalidate(state: BotLoadWizardDraft, patch: Partial<BotLoadWizardDraft>): boolean {
  return INVALIDATE_KEYS.some((k) => k in patch && patch[k] !== state[k])
}

/** 从草稿派生创建运行请求的 count。 */
export function draftTargetBots(draft: BotLoadWizardDraft): number {
  return targetBotsFromProfile(draft.loadProfile)
}

const DRAFT_PREFIX = 'jm.bot-load.wizard.draft.'

/** 将草稿存入 sessionStorage（不含 planToken）。 */
export function saveDraftToSession(key: string, draft: BotLoadWizardDraft): void {
  try {
    const rest = { ...draft, planToken: null, planExpiresAt: null }
    sessionStorage.setItem(DRAFT_PREFIX + key, JSON.stringify(rest))
  } catch {
    // 忽略配额错误
  }
}

/** 读取 sessionStorage 草稿。 */
export function loadDraftFromSession(key: string): BotLoadWizardDraft | null {
  try {
    const raw = sessionStorage.getItem(DRAFT_PREFIX + key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<BotLoadWizardDraft>
    return createDefaultDraft({ ...parsed, planToken: null, planExpiresAt: null })
  } catch {
    return null
  }
}

/** 清除 sessionStorage 草稿。 */
export function clearDraftSession(key: string): void {
  try {
    sessionStorage.removeItem(DRAFT_PREFIX + key)
  } catch {
    // ignore
  }
}

/** planToken 是否仍有效（未过期）。 */
export function isPlanTokenFresh(expiresAt: string | null, now = Date.now()): boolean {
  if (!expiresAt) return false
  const t = Date.parse(expiresAt)
  return Number.isFinite(t) && t > now
}
