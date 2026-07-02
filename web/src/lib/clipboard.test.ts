/** @vitest-environment jsdom */
import { describe, it, expect, vi } from 'vitest'
import { copyToClipboard } from './clipboard'

describe('copyToClipboard', () => {
  it('原生剪贴板不可用时在当前对话框内创建 textarea 并聚焦复制', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: vi.fn(() => true),
    })
    const execCommand = vi.mocked(document.execCommand)
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    document.body.appendChild(dialog)

    const ok = await copyToClipboard('secret-key')

    expect(ok).toBe(true)
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(dialog.querySelector('textarea')).toBeNull()
    dialog.remove()
  })
})
