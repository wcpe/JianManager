/**
 * 假后端运行时控制面（FR-496 阶段 6 补丁）：接口延迟 + 数据量规模 + 轮询间隔 + 请求日志，
 * 开着 mock 就能改。
 *
 * 为什么要单独立一个模块：这些旋钮必须**运行时**可变，才能用同一份 mock 复现多种环境
 * （弱网看骨架屏/loading、大数据量看虚拟滚动与分页、关掉轮询安静地看一次性请求）。
 *  - 延迟：`domainRoute` 在每个请求进入时读 `getMockLatency()`，改完下一个请求即刻生效；
 *  - 数据量：各域 seedFn 在**播种时**读 `mockDataScaleProfile()`，改档位后由 `resetMockData()`
 *    重播种即生效；
 *  - 轮询间隔：主控台侧 `mock-polling.ts` 在每个查询解析选项时读 `getMockPollInterval()`；
 *  - 请求日志：`browser.ts` 的 worker `start({ quiet })` 在启动时读 `isMockRequestLogEnabled()`。
 * 反过来，若把它们写成模块级常量（改造前的 `INSTANCE_SEED_SIZE` / `MOCK_FEED_COUNT` 那样），
 * handler 在 import 时就捕获了值，运行期再改也不会有任何效果——这正是本模块存在的理由。
 *
 * 状态放内存 + localStorage（键见下）：刷新页面后由 main.tsx 调 `applyPersistedMockRuntime()`
 * 还原，否则调完档位一刷新就悄悄回到默认、让人误以为 mock 坏了。
 * 本模块只在 mock 模式（VITE_MOCK）下被引用，不进生产构建。
 */

import { delay } from 'msw'
import { db, resetDb } from './db'

/** 延迟预设档位（ms）：覆盖「无延迟 → 弱网」的常用取值，0 表示不注入任何延迟。 */
export const MOCK_LATENCY_PRESETS = [0, 100, 300, 800, 1500] as const

/** 延迟自由输入上限（ms）：再高会让首屏等太久，失去调试价值。 */
export const MOCK_LATENCY_MAX = 3000

/** 数据量档位标识。`large` = 改造前的默认规模，保持默认档即不改变既有 e2e / 契约测试基线。 */
export type MockDataScale = 'small' | 'medium' | 'large'

/** 一个档位对应的各类种子行数。 */
export interface MockDataScaleProfile {
  /** 实例行数（instance 域种子 + observ 域生成行里的实例引用基数）。 */
  instanceCount: number
  /** 日志行数（日志中心）。 */
  logCount: number
  /** 批量任务行数。 */
  taskCount: number
  /** 告警事件 / 通知行数。 */
  feedCount: number
}

/**
 * 三档数据量。`small` 的实例数（60）刻意大于手工种子实例的最大 id（30），
 * 否则那些专门造出来演示特定状态的实例（survival-1 / lobby-proxy 等）会被裁掉。
 */
export const MOCK_DATA_SCALES: Record<MockDataScale, MockDataScaleProfile> = {
  small: { instanceCount: 60, logCount: 600, taskCount: 80, feedCount: 60 },
  medium: { instanceCount: 300, logCount: 3000, taskCount: 400, feedCount: 300 },
  large: { instanceCount: 1200, logCount: 12000, taskCount: 1500, feedCount: 1200 },
}

/** 档位中文标签（面板按钮文案，与档位标识分开：标识要稳定、文案可能会改）。 */
export const MOCK_DATA_SCALE_LABELS: Record<MockDataScale, string> = {
  small: '小',
  medium: '中',
  large: '大',
}

/** 默认档位 = 改造前的规模，保证不调档位时 mock 行为与既有测试基线完全一致。 */
export const MOCK_DEFAULT_DATA_SCALE: MockDataScale = 'large'

/**
 * 轮询间隔档位哨兵值：**跟随原始间隔**——不接管任何查询，完全按 `src/api/**` 各自定义的值走。
 * 这是默认档，理由：mock 的默认状态必须等于「改造前行为」，否则「用同一份 mock 复现真机现象」
 * 这件事就失效了（把原本 30s 的 alerts 统一拉到 1s，是在制造新负载，不是在复现）。
 */
export const MOCK_POLL_FOLLOW_ORIGINAL = -1

/**
 * 轮询间隔预设（ms）：`跟随原始` / `0 关闭` / 具体毫秒数。
 * 0 与具体值都是**显式覆盖**语义——统一改掉所有「本来在轮询」的查询，用于模拟极端环境；
 * 一次性加载的查询（未配置 refetchInterval）在任何档位下都不受影响。
 */
export const MOCK_POLL_INTERVAL_PRESETS = [MOCK_POLL_FOLLOW_ORIGINAL, 0, 1000, 5000, 15000, 60000] as const

/** 默认档 = 跟随原始（= 改造前行为）。 */
export const MOCK_DEFAULT_POLL_INTERVAL = MOCK_POLL_FOLLOW_ORIGINAL

export const MOCK_LATENCY_STORAGE_KEY = 'jm.devmock.latency'
export const MOCK_DATA_SCALE_STORAGE_KEY = 'jm.devmock.dataScale'
export const MOCK_POLL_INTERVAL_STORAGE_KEY = 'jm.devmock.pollInterval'
export const MOCK_REQUEST_LOG_STORAGE_KEY = 'jm.devmock.requestLog'

let latency = 0
let dataScale: MockDataScale = MOCK_DEFAULT_DATA_SCALE
let pollInterval = MOCK_DEFAULT_POLL_INTERVAL
let requestLog = false

function isBrowser(): boolean {
  return typeof window !== 'undefined' && typeof window.localStorage !== 'undefined'
}

function readStorage(key: string): string | null {
  if (!isBrowser()) return null
  try {
    return window.localStorage.getItem(key)
  } catch {
    // 隐私模式下 localStorage 可能直接抛错；读不到就退回内存默认值，不能让面板把整站带崩。
    return null
  }
}

function writeStorage(key: string, value: string): void {
  if (!isBrowser()) return
  try {
    window.localStorage.setItem(key, value)
  } catch {
    /* 同上：持久化失败不影响本次会话内生效 */
  }
}

function clampLatency(ms: number): number {
  if (!Number.isFinite(ms)) return 0
  return Math.min(MOCK_LATENCY_MAX, Math.max(0, Math.round(ms)))
}

function isMockDataScale(value: unknown): value is MockDataScale {
  return value === 'small' || value === 'medium' || value === 'large'
}

/** 当前生效的接口延迟（ms）。handler 每次请求都实时读，别缓存到模块级变量。 */
export function getMockLatency(): number {
  return latency
}

/** 设置接口延迟；返回实际生效值（越界会被夹到 [0, MOCK_LATENCY_MAX]）。 */
export function setMockLatency(ms: number): number {
  latency = clampLatency(ms)
  writeStorage(MOCK_LATENCY_STORAGE_KEY, String(latency))
  return latency
}

/** 当前数据量档位。 */
export function getMockDataScale(): MockDataScale {
  return dataScale
}

/** 当前档位的各项行数。种子函数在播种时读它——写成模块级常量就再也改不动了。 */
export function mockDataScaleProfile(): MockDataScaleProfile {
  return MOCK_DATA_SCALES[dataScale]
}

/**
 * 切换数据量档位（只改配置，**不**重播种）。单独暴露是为了让调用方明确知道
 * 「改档位」与「重播种」是两步：不重播种则集合里还是旧规模，列表 total 不会变。
 */
export function setMockDataScale(scale: MockDataScale): void {
  dataScale = scale
  writeStorage(MOCK_DATA_SCALE_STORAGE_KEY, scale)
}

/**
 * 按当前档位重播种整个假后端，并补回登录会话。
 *
 * 为什么必须补会话：`sessions` 也在内存集合里，重播种会把它清空，而前端 localStorage
 * 里的 token 还在——不补就是「档位刚调完，整站立刻 401 跳登录」（刷新后同理）。
 * 用户手工造的数据（新增的分组、上传的制品…）会一并丢失，所以面板上要写清这一点。
 */
export function resetMockData(): void {
  resetDb()
  restoreMockSession()
}

/**
 * 当前生效的轮询档位。`MOCK_POLL_FOLLOW_ORIGINAL` = 不接管，其它值 = 统一覆盖（0 表示关闭轮询）。
 * 主控台侧在每个查询**排期时**实时读——不要缓存到模块级常量（缓存了就改不动了）。
 */
export function getMockPollInterval(): number {
  return pollInterval
}

/** 设置轮询档位；返回实际生效值（-1 = 跟随原始；≥0 见 MOCK_POLL_INTERVAL_PRESETS）。 */
export function setMockPollInterval(ms: number): number {
  if (ms === MOCK_POLL_FOLLOW_ORIGINAL) pollInterval = MOCK_POLL_FOLLOW_ORIGINAL
  else pollInterval = Number.isFinite(ms) ? Math.max(0, Math.round(ms)) : MOCK_DEFAULT_POLL_INTERVAL
  writeStorage(MOCK_POLL_INTERVAL_STORAGE_KEY, String(pollInterval))
  return pollInterval
}

/**
 * 是否把 MSW 的请求日志打进控制台。默认关闭：
 * 一次首屏就有几十个请求，MSW 每个请求打一组 Request/Handler/Response（响应体整段进 console，
 * `/instances` 一行就是 1200 条实例），业务日志会被彻底埋掉。
 */
export function isMockRequestLogEnabled(): boolean {
  return requestLog
}

/** 设置请求日志开关（只改状态与持久化，真正生效要由 browser.ts 重启 worker，见 setMockRequestLog）。 */
export function setMockRequestLogEnabled(enabled: boolean): void {
  requestLog = enabled
  writeStorage(MOCK_REQUEST_LOG_STORAGE_KEY, enabled ? '1' : '0')
}

/**
 * 启动期还原：读 localStorage 的延迟 / 档位 / 轮询间隔 / 请求日志 → 按档位重播种。
 * main.tsx 在 `worker.start()` 之前调用，否则刷新后档位看着还在、数据规模却已是默认值
 * （档位是 import 时播种的，晚于它的修改才对得上）。
 */
export function applyPersistedMockRuntime(): void {
  const persistedLatency = readStorage(MOCK_LATENCY_STORAGE_KEY)
  if (persistedLatency !== null) latency = clampLatency(Number(persistedLatency))
  const persistedScale = readStorage(MOCK_DATA_SCALE_STORAGE_KEY)
  if (isMockDataScale(persistedScale)) dataScale = persistedScale
  // 存量键照读：改默认档不该让「上次显式选过档位」的人以为坏了
  // （只有没存过键时才落到新默认 = 跟随原始）
  const persistedPoll = readStorage(MOCK_POLL_INTERVAL_STORAGE_KEY)
  if (persistedPoll !== null) setMockPollInterval(Number(persistedPoll))
  requestLog = readStorage(MOCK_REQUEST_LOG_STORAGE_KEY) === '1'
  resetMockData()
}

/** 为已有 token 补一条 mock 会话，使刷新 / 换档位后保持登录（仅 mock 模式会产生 token）。 */
function restoreMockSession(): void {
  const token = readStorage('accessToken')
  if (!token || !isBrowser()) return
  // 动态取集合：本模块被面板在 React 渲染树之外引用，静态 import 会把 auth 域连带拉进面板 chunk。
  const sessions = db<Sess>('sessions')
  if (sessions.find((s) => s.accessToken === token)) return
  sessions.insert({
    accessToken: token,
    refreshToken: readStorage('refreshToken') ?? `mock-refresh-${token}`,
    userId: 1,
  })
}

/** 与 auth 域 `Session` 同构；此处本地声明只为避免 import 整个 auth handler 模块（见 restoreMockSession）。 */
interface Sess {
  id: number
  accessToken: string
  refreshToken: string
  userId: number
}

/**
 * handler 统一 `await` 的全局延迟。0 时直接返回、不排队任何宏任务——
 * 默认档下不给既有测试和交互增加哪怕一个 tick。
 */
export async function mockLatencyDelay(): Promise<void> {
  if (latency <= 0) return
  await delay(latency)
}
