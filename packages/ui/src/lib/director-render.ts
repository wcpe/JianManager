import { createContext, useContext } from 'react'

/**
 * 导播台渲染节流上下文（FR-168 / ADR-035）。
 *
 * 导播台把多个场景（预设）的卡**同时挂载**以保活 WS（瞬切零延迟），但只有 **active** 场景全速渲染；
 * 预热但非激活的场景**降频 / 暂停渲染**（终端 xterm 停 render 但 WS 继续收数据进缓冲，切回一次性 flush）。
 *
 * 本上下文向一个场景子树广播「是否激活」。默认 `active: true`：单实例画布 / 超级工作台（FR-166/167）
 * **无 Provider**，故消费者（如终端）默认全速渲染、行为完全不变——只有导播台在非激活场景子树外包
 * `active=false` 的 Provider 才触发节流。
 */
export interface DirectorRenderState {
  /** 当前场景子树是否处于激活（全速渲染）。false = 预热但非激活，应降频 / 暂停重绘。 */
  active: boolean
}

/** 默认激活：无 Provider 时（FR-166/167）一切照旧全速渲染。 */
const DirectorRenderContext = createContext<DirectorRenderState>({ active: true })

/** 给一个场景子树提供激活态（导播台用）。 */
export const DirectorRenderProvider = DirectorRenderContext.Provider

/**
 * 读取当前子树的导播台渲染态。
 * 终端 / 监控等持续渲染的卡用它决定是否暂停重绘 / 降低轮询；非导播台场景恒为 `active: true`。
 */
export function useDirectorRender(): DirectorRenderState {
  return useContext(DirectorRenderContext)
}
