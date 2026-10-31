import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

/** 
 * packages/ui 组件测试的 jsdom 全局 setup（阶段 0）。
 *
 * 刻意保持最小：组件库测试不挂 msw、不碰鉴权态、不依赖假后端，
 * 因此只需在每例后卸载渲染树，防止 DOM 与副作用在用例间泄漏。
 * 若后续出现需要跨用例共享的全局垫片（如浏览器 API polyfill），在此追加并注明缘由。
 */
afterEach(() => {
  cleanup()
})

/** 
 * jsdom 垫片：ResizeObserver（缘由：Radix 的 `use-size` 依赖它——Slider 拇指尺寸、
 * Tooltip/HoverCard 箭头测量等；jsdom 未实现，缺失时渲染期直接抛 ReferenceError）。
 * 组件在 jsdom 下不依赖真实尺寸（断言只看结构/语义），空实现即可。
 */
if (!('ResizeObserver' in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = ResizeObserverStub
}
