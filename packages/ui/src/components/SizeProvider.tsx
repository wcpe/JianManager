import * as React from 'react'

/**
 * 尺寸上下文（FR-496 阶段 6）：把「界面紧凑度」沿组件树级联下去。
 *
 * 背景：运维台需要在同一套页面上支持两种密度——监控墙/大屏偏宽松，实例与日志列表
 * 偏紧凑（一屏多放几行）。此前这只能靠调用点逐个传 `size`，于是同一页里
 * 工具栏是 sm、表格行是 default，密度不成体系。
 *
 * 与 Astryx `SizeContext` 的对应关系：Astryx 级联的是 `sm | md | lg` 三种元素尺寸；
 * 本库的 `Button` 等组件已自带 `size`（且语义是「控件自身大小」，取值更多），
 * 再引入一套 sm/md/lg 会与之混淆，故这里级联的是**密度**（`compact | default`），
 * 与控件自身的 `size` 正交：密度决定「同一控件在密集场景下用哪一档」，
 * `size` 仍是调用点的显式选择。
 *
 * 解析优先级（与 Astryx 一致）：显式入参 > 上层 Provider > 兜底默认。
 * 0 个 Provider 时 `useDensity()` 返回 `default`，因此组件库可被独立使用而不依赖宿主包一层 Provider。
 */

/** 界面紧凑度：`compact` 用于列表/日志等密集场景，`default` 为常规排版。 */
export type Density = 'compact' | 'default'

/**
 * 尺寸上下文。取值为 `null` 表示「上层没有任何 Provider」——
 * 与「Provider 显式给了 default」区分开，后者表示调用方明确要求常规密度。
 */
export const SizeContext = React.createContext<Density | null>(null)
SizeContext.displayName = 'SizeContext'

/**
 * 密度 Provider：包住一棵子树即可切换其中所有组件的紧凑度。
 *
 * 刻意不渲染任何 DOM（纯上下文）——密度是行为提示而非布局盒，
 * 插入包裹元素会让 `flex`/`grid` 容器的直接子元素关系变化，
 * 这类「加一层 div」的改动在页面级布局里代价极高。
 */
export function SizeProvider({
  density = 'default',
  children,
}: {
  /** 本子树使用的密度，默认 `default`（即不改动任何组件的既有表现）。 */
  density?: Density
  children?: React.ReactNode
}) {
  return <SizeContext.Provider value={density}>{children}</SizeContext.Provider>
}

/**
 * 读取当前密度。
 *
 * @param explicit 组件自身的显式密度入参；传了就压过上下文（调用点永远说了算）
 * @param fallback 既无入参也无 Provider 时的兜底，默认 `default`
 */
export function useDensity(
  explicit?: Density | null,
  fallback: Density = 'default',
): Density {
  const inherited = React.use(SizeContext)
  return explicit ?? inherited ?? fallback
}
