import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './i18n'
import './index.css'
import { initThemeFromStorage } from '@jianmanager/ui/lib/theme'
import App from './App'

// 首屏无闪 + 登录/初始化页也套主题（FR-164）：在 React 挂载前先把
// 主题色（data-theme）与明暗（class）套到 <html>，早于任何组件渲染。
initThemeFromStorage()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
})

/**
 * VITE_MOCK=1 时启 MSW Service Worker，整站打到内存假后端（FR-196，`npm run dev:mock`）。
 * 动态 import 确保 mock 代码不进生产包。否则直连真后端，行为不变。
 *
 * FR-496 阶段 6 补丁：
 * - worker 起来之前先还原调试面板持久化的四个旋钮（延迟 / 数据量 / 轮询间隔 / 请求日志）：
 *   `applyPersistedMockRuntime()` 内含「按档位重播种 + 补写登录会话」，替代原先散在这里的
 *   `resetDb()` + 会话补写逻辑；worker 由 devmock 的 `startMockWorker()` 启动——它默认
 *   `quiet`，不再把每个请求的 handler 与响应体打进控制台。
 * - worker 起来之后再挂调试面板，并给 queryClient 装上全局轮询控制（默认 1s 覆盖全部
 *   `refetchInterval`，见 components/devtools/mock-polling.ts）。
 * 面板与轮询控制都是动态 import，生产构建里 `import.meta.env.VITE_MOCK` 为假、
 * 这些分支会被静态消除，mock 相关代码不进产物。
 */
async function enableMocking() {
  if (!import.meta.env.VITE_MOCK) return
  const [{ startMockWorker }, runtime] = await Promise.all([
    import('@jianmanager/devmock/browser'),
    import('@jianmanager/devmock/runtime-control'),
  ])
  // 内存假后端刷新即重置，但 localStorage 仍存 token / 各档位——还原档位时顺带补一个会话，
  // 使 mock 模式刷新后保持登录（否则刷新→会话丢→首个 API 401→跳登录，整站不可持续点）。
  runtime.applyPersistedMockRuntime()
  await startMockWorker()
  const [{ installMockPollingControl, loadPersistedPollingExclude }, { mountMockControlPanel }] = await Promise.all([
    import('./components/devtools/mock-polling'),
    import('./components/devtools/mount-mock-control-panel'),
  ])
  // 白名单留空 = 全部轮询查询都归面板管；黑名单是面板里点掉的那些（刷新后保持调试现场）
  installMockPollingControl(queryClient, { exclude: loadPersistedPollingExclude() })
  mountMockControlPanel()
}

/**
 * 先挂载，再异步启动 mock —— **绝不把 render 放在 mock 就绪之后**。
 *
 * 旧实现是 `enableMocking().then(() => render())`：MSW 启动慢或卡住时，整页始终空白，
 * 且**没有任何可观测线索**（console 静默、body 为空、模块请求挂起），极易被误判成页面崩溃。
 * 实测 e2e 的 navigation-benchmark 就因此稳定失败在「关键页面就绪」这一条上。
 *
 * 代价与补偿：外壳先渲染后，MSW 尚未接管期间发出的 API 请求会失败。故 mock 就绪后
 * `invalidateQueries()` 让这些查询重来一次——数据比外壳晚到是可接受的，白屏不是。
 * 真后端模式下本就不进 mock 分支，行为与旧版一致。
 */
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </BrowserRouter>
  </StrictMode>,
)

void enableMocking()
  .then(() => {
    // 此前发出的请求可能打在 MSW 接管之前，就绪后统一重取。
    void queryClient.invalidateQueries()
  })
  .catch((err) => {
    // mock 起不来只应影响数据，不该让整个控制台不可用。
    console.error('[mock] 启动失败，控制台将以真后端模式继续：', err)
  })
