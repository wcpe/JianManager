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
  const host = document.createElement('div')
  host.id = HOST_ID
  document.body.append(host)
  createRoot(host).render(
    <StrictMode>
      <MockControlPanel />
    </StrictMode>,
  )
}
