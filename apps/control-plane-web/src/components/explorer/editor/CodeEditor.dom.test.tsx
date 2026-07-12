import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import CodeEditor from './CodeEditor'
import { useThemeStore } from '@/stores/theme'

beforeAll(() => {
  if (!('getClientRects' in Range.prototype)) {
    ;(Range.prototype as unknown as { getClientRects: () => DOMRectList }).getClientRects = () =>
      ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  } else {
    Range.prototype.getClientRects = () =>
      ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  }
  Range.prototype.getBoundingClientRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
})

beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('light', 'dark')
  act(() => useThemeStore.getState().setTheme('light'))
})

describe('CodeEditor 主题', () => {
  it('跟随全局暗色模式应用编辑器主题状态', async () => {
    act(() => useThemeStore.getState().setTheme('dark'))

    const { container } = render(<CodeEditor value="motd=hello" filename="server.properties" />)

    await waitFor(() =>
      expect(container.querySelector('[data-editor-theme]')).toHaveAttribute('data-editor-theme', 'dark'),
    )
  })

  it('从暗色切回亮色时同步编辑器主题状态', async () => {
    act(() => useThemeStore.getState().setTheme('dark'))
    const { container } = render(<CodeEditor value="motd=hello" filename="server.properties" />)
    await waitFor(() =>
      expect(container.querySelector('[data-editor-theme]')).toHaveAttribute('data-editor-theme', 'dark'),
    )

    act(() => useThemeStore.getState().setTheme('light'))

    await waitFor(() =>
      expect(container.querySelector('[data-editor-theme]')).toHaveAttribute('data-editor-theme', 'light'),
    )
  })
})
