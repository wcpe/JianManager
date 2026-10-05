/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'
import { readFileSync } from 'node:fs'

// FR-288（见 ADR-064/065）：版本唯一真源 = internal/version/version.go。
// 构建期（dev 与 build 同路径）解析注入 __APP_VERSION__，package.json 不再承载
// 版本语义（冻结 0.0.0）——bump 真源一处，前端展示 / Go /version / 产物路径三者一致。
const versionGo = readFileSync(path.resolve(__dirname, '../../internal/version/version.go'), 'utf8')
const appVersion = /^var Version = "(.+)"$/m.exec(versionGo)?.[1] ?? '0.0.0-unknown'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  // **必须从配置函数的 `env` 读，不能读 `process.env`**（NEW-1，2026-10-12 修复）：
  // vite 的 `loadEnv` 把 `.env*` 装进**配置函数收到的 `env` 对象**与 `import.meta.env`，
  // **不写回 `process.env`**；`constants.ts` 的包装也只为 NODE_ENV/BROWSER 开了特例。
  // 此前的 `process.env.VITE_API_TARGET` 因此**永远读不到 `.env.development.local`**
  // 里的覆盖值（实测：本机该文件已设 19000，dev 模式代理仍指向 8080 ⇒ 静默打到错误的 CP）。
  // 仍保留 `process.env` 优先：shell 显式 `VITE_API_TARGET=... pnpm dev` 的行为不变。
  const env = loadEnv(mode, process.cwd(), 'VITE_')
  const apiTarget = process.env.VITE_API_TARGET ?? env.VITE_API_TARGET
  return {
  // 注入前端版本号，运维控制台侧栏底部展示（FR-037；来源见上 FR-288）
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  // 路由已按页 lazy 分割；再把重型第三方库拆成独立 vendor chunk（v0.9.0 走查 #13）：
  // recharts/codemirror/xterm 等不再被卷进某个应用 chunk（原「PluginManager」单 chunk ~798KB），
  // 改善首屏体积与缓存命中（vendor 极少变动、可长期缓存）。
  build: {
    // safe-delete 环境下 vite 清空 dist/assets（>50 文件的批量 rmSync）会被拦截报错
    // （SAFE_DELETE_BULK_CONFIRM_REQUIRED，见 .workbuddy 部署日志）。产物全部带内容哈希、
    // index.html 只引用本次构建的文件，残留旧 chunk 无害；彻底清理走脚本 rename 迁移。
    emptyOutDir: false,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (!id.includes('node_modules')) return undefined
          if (id.includes('recharts') || id.includes('d3-') || id.includes('victory')) return 'charts'
          if (id.includes('@codemirror') || id.includes('codemirror') || id.includes('@lezer')) return 'editor'
          if (id.includes('@xterm')) return 'terminal'
          if (id.includes('react-dom') || id.includes('react-router') || id.includes('/react/')) return 'react-vendor'
          if (id.includes('@tanstack')) return 'query'
          return 'vendor'
        },
      },
    },
  },
  plugins: [react(), tailwindcss()],
  // @jianmanager/ui 经 pnpm workspace 真依赖解析（源码 exports，Vite 直接转译），不再 alias（FR-283）。
  resolve: {
    alias: [{ find: '@', replacement: path.resolve(__dirname, './src') }],
  },
  optimizeDeps: {
    // 第一方 workspace 包以**源码**形式被消费（exports 直接指向 src、由消费方 Vite 转译），
    // 因此不应参与依赖预构建——预构建会把它们打成单文件，使包内改动失去 HMR。
    //
    // 注：曾短暂怀疑本配置能修「访问某些页面时长时间白屏」，实测**无关**（加了 exclude 后
    // 同样复现，见 e2e/navigation-benchmark 的已知问题）。保留它只因这是第一方源码包的
    // 正确形态，与那次白屏无因果关系，勿据此处注释去推断白屏的成因。
    exclude: ['@jianmanager/ui', '@jianmanager/devmock'],
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        // 默认目标＝仓库自带 CP HTTP 端口（configs/control-plane.yml 的 server.port、
        // docker-compose 的 JIANMANAGER_SERVER_PORT，均为 8080）——按仓库文档起 CP 再 `pnpm dev`
        // 即开箱可用。
        //
        // 本机若跑的是另一个 CP（例如生产实例），**不要**改这里的默认值：把覆盖写进
        // `.env.development.local`（该文件被 .gitignore 忽略、不入库），例如
        // `VITE_API_TARGET=http://127.0.0.1:<端口>`。该覆盖值由上方 `loadEnv` 读取
        // （**不是** `process.env`，见 NEW-1 的说明）。默认值一旦指向某台具体部署，仓库默认
        // 开发流就不可用，且 mock 模式（MSW `onUnhandledRequest: 'bypass'`）会把未被假后端
        // 实现的端点真实打到那台部署上。
        //
        // mock 模式（`pnpm dev:mock` / Playwright e2e）默认指向一个**明确无监听**的回环端口：
        // MSW 只对已实现端点给假响应，未实现的一律 bypass —— 若沿用真实目标，这些请求会
        // 打到「恰好监听该端口的那台部署」上（实测：`/api/v1/logs/federation` 因 devmock 无
        // 假实现被转发到真实 CP 并返回 401/5xx），使 e2e 结果依赖本机进程而不可复现。
        // 指向空端口后未实现端点**确定性失败**（ECONNREFUSED ⇒ 页面走既有的降级/失败态），
        // 缺口应补 devmock 假后端而非依赖出网。仍可经 shell 显式 `VITE_API_TARGET=...` 覆盖。
        target: apiTarget
          ?? (mode === 'mock' ? 'http://127.0.0.1:9' : 'http://127.0.0.1:8080'),
        changeOrigin: true,
      },
    },
  },
  // vitest 双 project（FR-196 / ADR-047 决策 4）：node 跑纯逻辑单测（保留现状），
  // dom 跑 jsdom + testing-library 的组件 / 页面强断言（*.dom.test.tsx）。互不污染。
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: 'node',
          environment: 'node',
          include: ['src/**/*.test.{ts,tsx}'],
          exclude: ['src/**/*.dom.test.tsx'],
          sequence: { groupOrder: 0 },
        },
      },
      {
        extends: true,
        test: {
          name: 'dom',
          environment: 'jsdom',
          include: ['src/**/*.dom.test.tsx'],
          setupFiles: ['./src/test/setup.ts'],
          testTimeout: 10_000,
          maxWorkers: 4,
          sequence: { groupOrder: 1 },
        },
      },
    ],
  },
  }
})
