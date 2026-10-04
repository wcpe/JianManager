import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { MockControlPanel } from './MockControlPanel'

/**
 * 挂载 Mock 调试面板（FR-496 阶段 6 补丁）。
 *
 * 为什么另起一棵 React 树而不是往 App 里塞组件：
 * ① 面板与业务无关，塞进 App 就等于把「mock」写进业务外壳的渲染路径，生产构建也得跟着带上判断；
 * ② 面板只需要挂在 document.body 上（自身 position: fixed），无需 QueryClient / Router 等上下文，
 *    单独 createRoot 最省事，也不受业务树的 ErrorBoundary / 布局影响。
 * 由 main.tsx 在 mock 模式（VITE_MOCK）下动态 import 本模块后调用，生产构建里不存在调用点。
 */

/** 面板宿主容器 id：重复挂载（如 HMR 后 main.tsx 重跑）时据此幂等跳过，避免叠出两个面板。 */
const HOST_ID = 'mock-control-root'

export function mountMockControlPanel(): void {
  if (typeof document === 'undefined' || document.getElementById(HOST_ID)) return
  /**
   * 自动化（e2e）环境不挂面板。
   *
   * 面板 `fixed right-4 bottom-4 z-[45] w-80` 钉在右下角，会盖住表格行右侧的
   * 「编辑 / 删除」等按钮。Playwright 点击时先报 `element is visible, enabled and stable`，
   * 紧接着 `… mock-control-panel … intercepts pointer events`，随后一路重试到 30 秒超时——
   * 最终失败信息只显示 `locator.click: Test timeout`，完全看不出是被什么挡住的，
   * 极易误判成「按钮不存在」或「表格没渲染」。
   *
   * 判据用 `navigator.webdriver`：自动化驱动下为 true，人工开浏览器调试时为 false。
   * 于是「人调试时面板照常、跑 e2e 时不挡路」，也不必在测试侧注入样式去对抗挂载时机
   * （试过在 e2e helper 里 addStyleTag / addInitScript：前者被整页导航清掉，
   * 后者与面板的异步挂载时序纠缠，都不如在源头这一行干脆）。
   */
  if (navigator.webdriver) return
  const host = document.createElement('div')
  host.id = HOST_ID
  document.body.append(host)
  createRoot(host).render(
    <StrictMode>
      <MockControlPanel />
    </StrictMode>,
  )
}
