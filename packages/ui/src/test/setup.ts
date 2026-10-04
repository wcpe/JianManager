import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

/**
 * packages/ui 组件测试的 jsdom 全局 setup（FR-496 阶段 0）。
 *
 * 刻意保持最小：组件库测试不挂 msw、不碰鉴权态、不依赖假后端，
 * 因此只需在每例后卸载渲染树，防止 DOM 与副作用在用例间泄漏。
 * 若后续出现需要跨用例共享的全局垫片（如浏览器 API polyfill），在此追加并注明缘由。
 */
afterEach(() => {
  cleanup()
})
