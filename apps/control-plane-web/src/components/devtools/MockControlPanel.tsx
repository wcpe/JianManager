import { useCallback, useMemo, useState, useSyncExternalStore, type ReactElement } from 'react'
import { Bug, Database, Gauge, Minimize2, RefreshCw, RotateCcw, ScrollText, Timer } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { setMockRequestLog } from '@jianmanager/devmock/browser'
import {
  MOCK_DATA_SCALES,
  MOCK_DATA_SCALE_LABELS,
  MOCK_DEFAULT_DATA_SCALE,
  MOCK_DEFAULT_POLL_INTERVAL,
  MOCK_LATENCY_MAX,
  MOCK_LATENCY_PRESETS,
  MOCK_POLL_FOLLOW_ORIGINAL,
  MOCK_POLL_INTERVAL_PRESETS,
  getMockDataScale,
  getMockLatency,
  getMockPollInterval,
  isMockRequestLogEnabled,
  mockDataScaleProfile,
  resetMockData,
  setMockDataScale,
  setMockLatency,
  setMockPollInterval,
  type MockDataScale,
} from '@jianmanager/devmock/runtime-control'
import {
  listPollingQueryScopes,
  refreshMockPolling,
  subscribeMockPolling,
  togglePollingQuery,
} from './mock-polling'

/**
 * Mock 调试悬浮面板（FR-496 阶段 6 补丁）。
 *
 * 解决什么：mock 模式下接口「永远 0ms、永远 1200 条实例、控制台被 MSW 日志刷屏」，
 * 没法验弱网 loading / 骨架屏、没法验大数据量下的分页与虚拟滚动、也没法安静看一次请求。
 * 这里把四个旋钮摆到界面上：
 * ① 接口延迟（`setMockLatency` → 域路由统一 await）；
 * ② 数据量档位（`setMockDataScale` + 重播种）；
 * ③ 轮询间隔（`setMockPollInterval` + 全局接管 `refetchInterval`，见 mock-polling.ts）；
 * ④ MSW 请求日志开关（`setMockRequestLog` → worker 以 quiet 重启）。
 * 四者都在运行时生效，不必重启 dev server。
 *
 * 为什么是**独立渲染树**（见 mount-mock-control-panel.tsx）+ 只在 mock 模式动态 import：
 * 面板不属于业务界面，不该让 App 知道 mock 的存在，更要保证生产构建里这段代码根本不存在。
 *
 * 收起状态存在 localStorage（键见 MOCK_PANEL_COLLAPSED_KEY）：调试时常见「收起 → 刷新 →
 * 又要重新收一遍」的烦躁，持久化后刷新保持收起。
 *
 * 视觉：只用 Tailwind 语义 token（bg-card / border-border / text-muted-foreground …），
 * 明暗与五套主题色自动跟随；层级取 z-[45]——高于应用内容（≤z-40 的移动底栏）又低于弹窗
 * （Dialog / Sheet 自 z-50 起），避免盖住模态框。
 */

/** 收起状态持久化键。导出是为了让测试钉住真实键名，避免测试自造键、实现改键后测试仍全绿。 */
export const MOCK_PANEL_COLLAPSED_KEY = 'jm.devmock.panel.collapsed'

/** 面板里最多列几个轮询查询芯片：再多会把浮窗撑长，其余用一行文字交代。 */
const POLL_CHIP_LIMIT = 6

/** 面板容器类：收起态与展开态共用定位与层级，避免两处各写一遍漂移。 */
const PANEL_ANCHOR = 'fixed right-4 bottom-4 z-[45]'

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(MOCK_PANEL_COLLAPSED_KEY) === '1'
  } catch {
    // 隐私模式下 localStorage 读取可能抛错：读不到就当展开，不能因为持久化失败让面板消失
    return false
  }
}

function writeCollapsed(collapsed: boolean): void {
  try {
    window.localStorage.setItem(MOCK_PANEL_COLLAPSED_KEY, collapsed ? '1' : '0')
  } catch {
    /* 持久化失败不影响本次会话内的收起/展开 */
  }
}

/** Mock 调试悬浮面板：展开态是设置面板，收起态是右下角小球。 */
export function MockControlPanel(): ReactElement {
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const [latency, setLatency] = useState(getMockLatency)
  const [scale, setScale] = useState<MockDataScale>(getMockDataScale)
  // 换档位只改配置、集合要重播种才变；而页面上的列表早已拿过旧数据，
  // 故提示「刷新后生效」，不让用户对着没变化的列表以为面板坏了。
  const [scaleChanged, setScaleChanged] = useState(false)
  const [pollInterval, setPollInterval] = useState(getMockPollInterval)
  const [requestLog, setRequestLogState] = useState(isMockRequestLogEnabled)

  // 轮询查询集合会随页面切换增删（面板在渲染树之外，拿不到 QueryClientProvider），
  // 用 store 订阅 + 快照字符串避免每次查询更新都白重渲染（getSnapshot 返回原值即不重渲染）。
  const pollingSnapshot = useSyncExternalStore(subscribeMockPolling, () =>
    JSON.stringify(listPollingQueryScopes()),
  )
  const pollingScopes = useMemo(
    () => JSON.parse(pollingSnapshot) as Array<{ label: string; excluded: boolean }>,
    [pollingSnapshot],
  )

  const toggleCollapsed = useCallback((next: boolean) => {
    setCollapsed(next)
    writeCollapsed(next)
  }, [])

  const applyLatency = useCallback((ms: number) => {
    setLatency(setMockLatency(ms))
  }, [])

  const applyScale = useCallback((next: MockDataScale) => {
    // 点当前档位不该重置数据（用户可能只是想看看当前是什么档）
    if (next === getMockDataScale()) return
    setMockDataScale(next)
    resetMockData()
    setScale(next)
    setScaleChanged(true)
  }, [])

  const applyPollInterval = useCallback((ms: number) => {
    setPollInterval(setMockPollInterval(ms))
    // 只改配置不改排期的话，最长要等当前那一轮到期（最多 60s）才看得出变化
    refreshMockPolling()
  }, [])

  const applyRequestLog = useCallback((enabled: boolean) => {
    // MSW 只在 start() 读 quiet，切换要 stop→start 重配网络层（见 devmock/browser.ts）
    void setMockRequestLog(enabled).then(() => setRequestLogState(isMockRequestLogEnabled()))
  }, [])

  const reset = useCallback(() => {
    applyLatency(0)
    applyScale(MOCK_DEFAULT_DATA_SCALE)
    applyPollInterval(MOCK_DEFAULT_POLL_INTERVAL)
  }, [applyLatency, applyScale, applyPollInterval])

  if (collapsed) {
    return (
      <Button
        type="button"
        variant="outline"
        size="icon-lg"
        data-testid="mock-control-ball"
        aria-label="展开 Mock 调试面板"
        tooltip="Mock 调试面板"
        className={`${PANEL_ANCHOR} rounded-full bg-card shadow-lg`}
        onClick={() => toggleCollapsed(false)}
      >
        <Bug aria-hidden />
      </Button>
    )
  }

  const profile = mockDataScaleProfile()

  return (
    <aside
      data-testid="mock-control-panel"
      aria-label="Mock 调试面板"
      className={`${PANEL_ANCHOR} w-80 rounded-lg border border-border bg-card p-3 text-card-foreground shadow-lg`}
    >
      <header className="mb-2 flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-xs font-semibold">
          <Bug className="size-3.5 text-primary" aria-hidden />
          Mock 调试
        </span>
        <span className="flex items-center gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            data-testid="mock-reset"
            aria-label="重置为默认值"
            tooltip="重置延迟 / 数据量 / 轮询间隔"
            onClick={reset}
          >
            <RotateCcw aria-hidden />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            data-testid="mock-collapse"
            aria-label="收起为悬浮球"
            tooltip="收起为悬浮球"
            onClick={() => toggleCollapsed(true)}
          >
            <Minimize2 aria-hidden />
          </Button>
        </span>
      </header>

      {/* ── 接口延迟 ── */}
      <section className="space-y-1.5">
        <div className="flex items-center justify-between text-xs">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <Gauge className="size-3.5" aria-hidden />
            接口延迟
          </span>
          <span data-testid="mock-latency-value" className="font-mono text-foreground">
            {latency} ms
          </span>
        </div>
        <div className="flex flex-wrap gap-1">
          {MOCK_LATENCY_PRESETS.map((ms) => (
            <Button
              key={ms}
              type="button"
              size="xs"
              variant={latency === ms ? 'default' : 'outline'}
              aria-pressed={latency === ms}
              data-testid={`mock-latency-preset-${ms}`}
              onClick={() => applyLatency(ms)}
            >
              {ms === 0 ? '关闭' : `${ms}ms`}
            </Button>
          ))}
        </div>
        <input
          type="range"
          min={0}
          max={MOCK_LATENCY_MAX}
          step={50}
          value={latency}
          data-testid="mock-latency-range"
          aria-label="接口延迟（毫秒）"
          // 原生 range 只取 accent-primary 上色，其余交给语义 token：自定义外观在明暗/五套主题下都要重调，不划算
          className="h-1.5 w-full cursor-pointer appearance-none rounded-full bg-muted accent-primary"
          onChange={(event) => applyLatency(Number(event.target.value))}
        />
      </section>

      {/* ── 数据量档位 ── */}
      <section className="mt-3 space-y-1.5">
        <div className="flex items-center justify-between text-xs">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <Database className="size-3.5" aria-hidden />
            数据量
          </span>
          <span data-testid="mock-scale-value" className="text-foreground">
            {MOCK_DATA_SCALE_LABELS[scale]} · 约 {profile.instanceCount} 实例 / {profile.logCount} 日志
          </span>
        </div>
        <div className="flex gap-1">
          {(Object.keys(MOCK_DATA_SCALES) as MockDataScale[]).map((key) => {
            const option = MOCK_DATA_SCALES[key]
            return (
              <Button
                key={key}
                type="button"
                size="xs"
                className="flex-1"
                variant={scale === key ? 'default' : 'outline'}
                aria-pressed={scale === key}
                data-testid={`mock-scale-${key}`}
                tooltip={`${option.instanceCount} 实例 / ${option.logCount} 日志 / ${option.taskCount} 任务`}
                onClick={() => applyScale(key)}
              >
                {MOCK_DATA_SCALE_LABELS[key]}
              </Button>
            )
          })}
        </div>
        {scaleChanged && (
          <div className="flex items-center justify-between gap-2 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
            <span>已按新档位重置数据</span>
            <Button
              type="button"
              size="xs"
              variant="outline"
              data-testid="mock-reload"
              tooltip="重新加载页面，让列表按新规模重新取数"
              onClick={() => window.location.reload()}
            >
              <RefreshCw aria-hidden />
              刷新
            </Button>
          </div>
        )}
      </section>

      {/* ── 轮询间隔 ── */}
      <section className="mt-3 space-y-1.5">
        <div className="flex items-center justify-between text-xs">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <Timer className="size-3.5" aria-hidden />
            轮询间隔
          </span>
          <span data-testid="mock-poll-value" className="font-mono text-foreground">
            {pollInterval === MOCK_POLL_FOLLOW_ORIGINAL
              ? '跟随原始'
              : pollInterval === 0
                ? '已关闭'
                : `${pollInterval / 1000} s`}
          </span>
        </div>
        <div className="flex flex-wrap gap-1">
          {MOCK_POLL_INTERVAL_PRESETS.map((ms) => (
            <Button
              key={ms}
              type="button"
              size="xs"
              variant={pollInterval === ms ? 'default' : 'outline'}
              aria-pressed={pollInterval === ms}
              data-testid={`mock-poll-preset-${ms}`}
              onClick={() => applyPollInterval(ms)}
            >
              {ms === MOCK_POLL_FOLLOW_ORIGINAL ? '跟随原始' : ms === 0 ? '关闭' : `${ms / 1000}s`}
            </Button>
          ))}
        </div>
        {/* 黑白名单：列的是「本来就在轮询」的查询，点一下就把它排除 / 纳入 */}
        <div className="text-xs text-muted-foreground" data-testid="mock-poll-scope">
          作用于 {pollingScopes.length} 个轮询查询
          {pollInterval === MOCK_POLL_FOLLOW_ORIGINAL && (
            <span data-testid="mock-poll-passive">（当前跟随原始，未接管）</span>
          )}
          {pollingScopes.length > 0 && (
            <span className="mt-1 flex flex-wrap gap-1">
              {pollingScopes.slice(0, POLL_CHIP_LIMIT).map(({ label, excluded: off }) => {
                return (
                  <Button
                    key={label}
                    type="button"
                    size="xs"
                    variant={off ? 'outline' : 'secondary'}
                    aria-pressed={!off}
                    aria-label={`${off ? '纳入' : '排除'}轮询查询 ${label}`}
                    data-testid={`mock-poll-chip-${label}`}
                    className={off ? 'text-muted-foreground line-through' : undefined}
                    onClick={() => togglePollingQuery(label)}
                  >
                    {label}
                  </Button>
                )
              })}
              {pollingScopes.length > POLL_CHIP_LIMIT && (
                <span>其余 {pollingScopes.length - POLL_CHIP_LIMIT} 个未列出</span>
              )}
            </span>
          )}
        </div>
      </section>

      {/* ── MSW 请求日志 ── */}
      <section className="mt-3 flex items-center justify-between gap-2 text-xs">
        <span className="flex items-center gap-1.5 text-muted-foreground">
          <ScrollText className="size-3.5" aria-hidden />
          请求日志
        </span>
        <span className="flex items-center gap-1">
          <Button
            type="button"
            size="xs"
            variant={requestLog ? 'default' : 'outline'}
            aria-pressed={requestLog}
            data-testid="mock-request-log-on"
            tooltip="打开后 MSW 会把每个请求命中的 handler 与响应体打进控制台"
            onClick={() => applyRequestLog(true)}
          >
            打印
          </Button>
          <Button
            type="button"
            size="xs"
            variant={requestLog ? 'outline' : 'default'}
            aria-pressed={!requestLog}
            data-testid="mock-request-log-off"
            tooltip="默认静默：一次首屏几十个请求、响应体整段进控制台，会埋掉业务日志"
            onClick={() => applyRequestLog(false)}
          >
            静默
          </Button>
        </span>
      </section>

      <p className="mt-2 text-xs leading-tight text-muted-foreground">
        仅 mock 模式可用。切换数据量会重置假后端数据（含手工新增）。
      </p>
    </aside>
  )
}
