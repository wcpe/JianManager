/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

/**
 * 业务视图包单测配置，与 packages/ui 同形态（双 project：node 跑纯逻辑、dom 跑组件渲染）。
 *
 * 与 packages/ui 的差异：本包组件**受控**，不取数、不碰路由，因此 dom 测试既不需要
 * msw 也不需要路由 Provider —— 直接 render 即可。这正是受控化的收益之一：
 * 组件的测试不再依赖应用的一整套运行时。
 */
export default defineConfig({
  plugins: [react()],
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: 'node',
          environment: 'node',
          include: ['src/**/*.test.ts'],
        },
      },
      {
        extends: true,
        test: {
          name: 'dom',
          environment: 'jsdom',
          include: ['src/**/*.test.tsx'],
          setupFiles: ['./src/test/setup.ts'],
          testTimeout: 10_000,
        },
      },
    ],
  },
})
