import { useEffect, useMemo } from 'react'

import { ROUTE_LOADERS, matchRouteKey } from './route-chunks'

/**
 * 路由 chunk 预取（FR-496 阶段 6 补丁）。
 *
 * 切页等待的大头是目标页 chunk 的下载，而 `React.lazy` 只在首次渲染时才发起 import——
 * 用户点下去才开始下载。本模块把「导航意图」（hover / focus / 鼠标进入侧栏）翻译成一次
 * `import()`，让下载与用户「要不要点」的犹豫时间重叠；点下去时模块已在缓存里，`lazy` 同步解析。
 *
 * 【幂等】
 * 同一个路由键只真正 import 一次：`import()` 本身有模块缓存，但它仍会走一次 Promise/微任务，
 * 且失败时会被反复重试；这里用 `started` 集合兜住重复意图，失败时再撤掉标记允许下次重试。
 * 加载器来自 {@link ROUTE_LOADERS}（模块级箭头函数，不在渲染期新建 import 表达式）。
 *
 * 【尽力而为】
 * 未命中路由表的目标（外链、`/api/...`、锚点）一律静默忽略：预取绝不能因为路径不认识
 * 而抛错或拖慢导航本身。
 */
export interface RoutePrefetcher {
  /** 按导航目标（可带 `?query#hash`）触发一次预取；重复调用无副作用。 */
  prefetch: (to: string) => void
  /** 已发起过预取的路由键，按发起顺序——仅供观测与测试。 */
  prefetched: () => string[]
}

/** 去掉 query/hash 与结尾斜杠，得到可查表的路径。 */
export function normalizeTarget(to: string): string {
  const [raw = ''] = to.split(/[?#]/)
  if (raw === '') return '/'
  return raw.length > 1 && raw.endsWith('/') ? raw.slice(0, -1) : raw
}

/**
 * 创建一个幂等预取器（纯逻辑，供单测与多入口复用；预取动作经 `loaders` 注入，不耦合路由表）。
 * 应用侧用模块级单例 {@link prefetchRoute}，因此所有入口共享同一份「已预取」状态。
 */
export function createRoutePrefetcher(
  loaders: Record<string, () => Promise<unknown>> = ROUTE_LOADERS,
): RoutePrefetcher {
  const started = new Set<string>()
  const order: string[] = []

  const prefetch = (to: string) => {
    const key = matchRouteKey(normalizeTarget(to))
    if (key === null || started.has(key)) return
    const load = loaders[key]
    if (!load) return

    started.add(key)
    order.push(key)
    // 失败（断网 / 部署换版本导致 chunk 404）不能留下「已预取」的假状态：
    // 撤掉标记，用户下次给出同样意图时再试一次。
    void load().catch(() => {
      started.delete(key)
      order.splice(order.indexOf(key), 1)
    })
  }

  return { prefetch, prefetched: () => [...order] }
}

/** 应用级预取器：模块加载时建一次，`prefetch` 在所有组件间保持同一引用。 */
const appPrefetcher = createRoutePrefetcher()

/** 预取某个导航目标对应的 chunk（幂等；未知目标忽略）。 */
export const prefetchRoute = appPrefetcher.prefetch

/** 已预取的路由键（观测/测试用）。 */
export const prefetchedRoutes = appPrefetcher.prefetched

/**
 * 把「悬停 / 获得焦点」变成预取意图，返回可展开到导航元素上的 handlers。
 * 用 `useMemo` 固定引用：`NavLink` 之类的行组件不会因为 handler 变化而重渲染。
 */
export function useRouteIntentPrefetch(to: string): {
  onMouseEnter: () => void
  onFocus: () => void
} {
  return useMemo(
    () => ({
      onMouseEnter: () => prefetchRoute(to),
      onFocus: () => prefetchRoute(to),
    }),
    [to],
  )
}

/**
 * 页面内链接的通用预取：在 document 上代理 `pointerover` / `focusin`，
 * 命中最近的 `<a href="/…">` 就预取。
 *
 * 为什么要代理而不是逐个链接挂 handler：控制台里有大量 `<Link>`/`<a>` 分散在各个页面与
 * 侧栏文件里，逐个挂会漏、还得改一堆不该动的文件；代理挂在 document 上一次覆盖全部
 * （含移动端导航、面包屑、空态引导、资源树链接）。预取本身幂等，重复命中无代价。
 */
export function useLinkIntentPrefetch(): void {
  useEffect(() => {
    const onIntent = (event: Event) => {
      const target = event.target
      if (!(target instanceof Element)) return
      const href = target.closest('a[href]')?.getAttribute('href')
      // 只认站内绝对路径：外链、`//host`、锚点、`/api/...` 下载端点都不在路由表里，
      // 交给 matchRouteKey 兜底忽略。
      if (!href || !href.startsWith('/') || href.startsWith('//')) return
      prefetchRoute(href)
    }

    document.addEventListener('pointerover', onIntent, { passive: true })
    document.addEventListener('focusin', onIntent)
    return () => {
      document.removeEventListener('pointerover', onIntent)
      document.removeEventListener('focusin', onIntent)
    }
  }, [])
}

/**
 * 侧栏常驻快捷入口（服务器运维工作区的固定三条，见 `WorkspaceSidebar` 的 `SideNavShortcuts` 段）。
 *
 * 【为什么需要这条兜底】
 * 侧栏的导航行是 `SideNavRow`（`<button>` + `navigate()`），ui 包的行组件当前不接路由
 * （`WorkspaceSidebar.tsx` 头注里登记的阶段 7 缺口），所以 hover 事件上拿不到目标路径，
 * 上面的链接代理与显式 handler 都覆盖不到这些行。这里退一步：**鼠标进入侧栏**就是一个足够强的
 * 「用户要去导航」信号，此时把这几个固定入口的 chunk 预热掉。
 * 列表刻意压到三条（其中 `/` 是落地页、通常已在缓存），代价上限即 `/instances` + `/nodes` 两个 chunk；
 * 换成「预热全部侧栏入口」会一次拉几十个 chunk，那样的带宽代价不该由一次 hover 触发。
 */
export const SIDEBAR_SHORTCUT_WARMUP: readonly string[] = ['/', '/instances', '/nodes']

/** 鼠标首次进入侧栏时预热常驻快捷入口（只做一次判定，之后早退）。 */
export function useSidebarShortcutWarmup(): void {
  useEffect(() => {
    let warmed = false
    const onPointerOver = (event: Event) => {
      if (warmed) return
      const target = event.target
      if (!(target instanceof Element) || target.closest('[data-slot="console-sidebar"]') === null) return
      warmed = true
      for (const to of SIDEBAR_SHORTCUT_WARMUP) prefetchRoute(to)
    }

    document.addEventListener('pointerover', onPointerOver, { passive: true })
    return () => document.removeEventListener('pointerover', onPointerOver)
  }, [])
}
