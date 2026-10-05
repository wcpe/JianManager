import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

/**
 * 业务视图包组件测试的 jsdom 全局 setup，与 packages/ui 同款且刻意保持最小。
 *
 * 本包组件受控（数据与路由身份经 props 注入），测试不挂 msw、不依赖假后端、
 * 也不需要路由 Provider，故只需每例后卸载渲染树，防止 DOM 与副作用泄漏。
 */
afterEach(() => {
  cleanup()
})
