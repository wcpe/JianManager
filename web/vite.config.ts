/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'
import pkg from './package.json' with { type: 'json' }

// https://vite.dev/config/
export default defineConfig({
  // 注入前端版本号，运维控制台侧栏底部展示（FR-037）
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
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
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
