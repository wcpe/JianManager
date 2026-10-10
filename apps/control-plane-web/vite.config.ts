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
  // 路由已按页 lazy 分割；第三方依赖**不再按名字拆 vendor chunk**（2025-10 首屏走查，见下）。
  //
  // 【为什么撤掉当年的 vendor 分组】原配置把 recharts/d3 拆成 'charts'、react 系拆成
  // 'react-vendor'、其余拆成 'vendor'/'query'，本意是「vendor 极少变动、可长期缓存」。
  // 但实测（rolldown 1.0.3 / vite 8.0.16）只要**给 recharts 命名分组**，rolldown 就会把
  // react、react-dom 一并并进那个 chunk：产物里 `charts-*.js` 同时装着 recharts 与整个
  // React 运行时，而**每个 chunk 都要 `import { require_react } from "./charts-*.js"`**。
  // 于是这个 359 kB（gzip 106 kB）的 chunk 成为全站每页首屏的硬依赖，被写进
  // dist/index.html 的 modulepreload——落地页（/，就是个仪表盘）也得先下完它才能跑。
  //
  // 实测对照（`npx vite build`，gzip，只统计首屏必需：入口 + index.html 预加载项）：
  //   原配置                     入口 153 kB + charts 107 + react-vendor 138 + vendor 110
  //                              + query 11 ≈ 519 kB，其中 recharts 占 107 kB
  //   只保留 editor 分组（本配置） 入口 262 kB + react 3.2 + clsx 2.7 + preload-helper 0.7
  //                              ≈ 268 kB；recharts 落在自己的 LineChart-*.js（322 kB，
  //                              未被预加载，只有监控/客户端分发等真正画图的路由按需取）
  //   只给 react 系命名、不给 charts 命名 → recharts 又被并进 react-vendor 并继续被预加载
  //                              （同样实测过）；charts 与 react 系都命名也一样。
  //
  // 结论：命名分组在 rolldown 下换不来「更小的首屏」，只换来「更大的首屏」。
  // 撤掉后回到 rolldown 默认分块——它按真实可达性分块，react 单独只有 8 kB，
  // recharts 与编辑器各自独立成 chunk，都不进首屏预加载。
  // 唯一保留的命名组是编辑器：codemirror 只被少数页面用到，独立分组仍符合原意且无害
  // （实测它不在预加载列表里）。
  build: {
    // safe-delete 环境下 vite 清空 dist/assets（>50 文件的批量 rmSync）会被拦截报错
    // （SAFE_DELETE_BULK_CONFIRM_REQUIRED，见 .workbuddy 部署日志）。产物全部带内容哈希、
    // index.html 只引用本次构建的文件，残留旧 chunk 无害；彻底清理走脚本 rename 迁移。
    emptyOutDir: false,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (!id.includes('node_modules')) return undefined
          if (id.includes('@codemirror') || id.includes('codemirror') || id.includes('@lezer')) return 'editor'
          // 其余一律交回 rolldown 默认分块：不要给 recharts / react / @tanstack 命名。
          return undefined
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
    /**
     * 覆盖率（补齐规则与工具的脱节）。
     *
     * `.claude/rules/testing-and-quality.md` 要求「新增代码覆盖率 ≥60%」，
     * 但此前既没装 @vitest/coverage-v8 也没任何配置——该规则在前端无法执行。
     *
     * 口径：只统计 `src/**` 的实现文件（排除测试、测试工具、类型声明、语言包 JSON）。
     * thresholds 见下方注释——**不在本次设定硬阈值**，理由是需要先有基线数据，
     * 否则一开就是红的、反而会被人绕过。
     */
    coverage: {
      provider: 'v8',
      reporter: ['text-summary', 'html', 'json-summary'],
      reportsDirectory: './coverage',
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/**/*.d.ts',
        'src/test/**',
        'src/i18n/zh.json',
        'src/i18n/en.json',
      ],
    },
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
