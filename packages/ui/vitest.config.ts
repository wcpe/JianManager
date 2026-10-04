/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

/**
 * 组件库单测配置（FR-496 阶段 0 建立）。
 *
 * 背景：packages/ui 自 FR-283 迁入以来零测试，而 .claude/rules/testing-and-quality.md
 * 要求新增代码覆盖率 ≥60%。与主控台采用同一套双 project 形态，理由相同：
 * node 跑纯逻辑（lib/、阈值计算等），dom 跑组件渲染——互不污染，且 jsdom 不拖慢纯函数用例。
 *
 * 与主控台 setup 的差异：组件库不依赖 devmock/msw/鉴权态，故 setup 保持最小。
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
