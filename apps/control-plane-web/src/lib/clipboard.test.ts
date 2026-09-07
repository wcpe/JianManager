/** @vitest-environment jsdom */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { copyToClipboard, readClipboard } from './clipboard'

/** 把 navigator.clipboard 置为指定值（undefined = 模拟 HTTP 非安全上下文）。 */
const stubClipboard = (value: unknown) => {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value })
}

afterEach(() => {
  stubClipboard(undefined)
  vi.restoreAllMocks()
})

describe('copyToClipboard', () => {
  it('原生剪贴板不可用时在当前对话框内创建 textarea 并聚焦复制', async () => {
    stubClipboard(undefined)
    // execCommand 执行时抓当前聚焦元素，验证回退真把待复制文本选中后才 copy。
    let copiedFrom: string | null = null
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: vi.fn(() => {
        copiedFrom = (document.activeElement as HTMLTextAreaElement | null)?.value ?? null
        return true
      }),
    })
    const execCommand = vi.mocked(document.execCommand)
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    document.body.appendChild(dialog)

    const ok = await copyToClipboard('secret-key')

    expect(ok).toBe(true)
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(copiedFrom).toBe('secret-key')
    expect(dialog.querySelector('textarea')).toBeNull()
    dialog.remove()
  })

  it('execCommand 回退也失败时返回 false（调用方据此提示替代路径）', async () => {
    stubClipboard(undefined)
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: vi.fn(() => false),
    })

    await expect(copyToClipboard('secret-key')).resolves.toBe(false)
  })
})

/**
 * BUG-1 复现：HTTP 非安全上下文（如 http://103.45.143.199:50100）下整个
 * navigator.clipboard 为 undefined。裸写 `navigator.clipboard?.readText()` 时可选链
 * 既不抛错也读不到内容，只静默返回 undefined，调用方判 `if (text)` 为假就完全没反应。
 * readClipboard 必须把这种情况报成**可识别的失败**，让 UI 能提示改按 Ctrl+V。
 */
describe('readClipboard', () => {
  it('navigator.clipboard 为 undefined（HTTP 非安全上下文）时报 unavailable，而非静默 undefined', async () => {
    stubClipboard(undefined)

    const result = await readClipboard()

    expect(result).toEqual({ ok: false, reason: 'unavailable' })
    // 回归闸：结果本身必须能判定失败，不允许退回「undefined 当空剪贴板」的静默语义。
    expect(result.ok).toBe(false)
    expect(result).not.toBeUndefined()
  })

  it('clipboard 对象存在但没有 readText 时同样报 unavailable', async () => {
    stubClipboard({ writeText: vi.fn() })

    await expect(readClipboard()).resolves.toEqual({ ok: false, reason: 'unavailable' })
  })

  it('readText 抛错（权限被拒 / 文档失焦）时报 denied', async () => {
    stubClipboard({ readText: vi.fn(async () => { throw new Error('NotAllowedError') }) })

    await expect(readClipboard()).resolves.toEqual({ ok: false, reason: 'denied' })
  })

  it('安全上下文可读时返回文本', async () => {
    stubClipboard({ readText: vi.fn(async () => 'say hello') })

    await expect(readClipboard()).resolves.toEqual({ ok: true, text: 'say hello' })
  })
})
