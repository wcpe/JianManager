/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
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
export default defineConfig({
  // 注入前端版本号，运维控制台侧栏底部展示（FR-037；来源见上 FR-288）
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  // 路由已按页 lazy 分割；再把重型第三方库拆成独立 vendor chunk（v0.9.0 走查 #13）：
  // recharts/codemirror/xterm 等不再被卷进某个应用 chunk（原「PluginManager」单 chunk ~798KB），
  // 改善首屏体积与缓存命中（vendor 极少变动、可长期缓存）。
  build: {
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
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
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
        },
      },
    ],
  },
})
