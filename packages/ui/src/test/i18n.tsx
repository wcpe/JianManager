import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { render, type RenderOptions } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'

/**
 * 包内测试用的最小 i18n 实例 —— 刻意不共享应用的 `@/i18n`。
 *
 * 为什么自带：本包不得依赖应用（`@/i18n` 会带进应用的资源加载与初始化副作用），
 * 而组件里确实存在少量 UI 文案。
 *
 * 为什么资源只有三条：本包组件受控、文案面很窄，目前只有 `ReleaseNotes` 用到
 * （`common.cancel` / `releaseNotes.openExternal` / `releaseNotes.openExternalTitle`）。
 * **若将来某组件需要的 key 明显变多，正确做法是把该组件的文案改为 props 注入，
 * 而不是往这里继续堆资源** —— 否则这个文件会变成第二份应用语言包。
 */
const testResources = {
  'zh-CN': {
    translation: {
      common: { cancel: '取消' },
      releaseNotes: {
        openExternal: '打开',
        openExternalTitle: '打开外部链接？',
      },
    },
  },
}

if (!i18n.isInitialized) {
  void i18n.use(initReactI18next).init({
    lng: 'zh-CN',
    fallbackLng: 'zh-CN',
    resources: testResources,
    interpolation: { escapeValue: false },
  })
}

/** 只包 i18n 的渲染 —— 受控组件不需要 Router / QueryClient，故不提供它们。 */
export function renderWithI18n(ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={i18n}>{children}</I18nextProvider>
  }
  return render(ui, { wrapper: Wrapper, ...options })
}
