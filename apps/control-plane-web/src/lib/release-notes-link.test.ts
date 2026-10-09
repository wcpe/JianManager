import { describe, it, expect } from 'vitest'
import { isSafeExternalLink, confirmOpenMessage } from './release-notes-link'

describe('isSafeExternalLink', () => {
  it('放行 http/https 绝对链接', () => {
    expect(isSafeExternalLink('https://github.com/wcpe/JianManager')).toBe(true)
    expect(isSafeExternalLink('http://example.com')).toBe(true)
  })

  it('不放行空/相对/锚点链接', () => {
    expect(isSafeExternalLink('')).toBe(false)
    expect(isSafeExternalLink(undefined)).toBe(false)
    expect(isSafeExternalLink(null)).toBe(false)
    expect(isSafeExternalLink('#section')).toBe(false)
    expect(isSafeExternalLink('/relative/path')).toBe(false)
    expect(isSafeExternalLink('relative')).toBe(false)
  })

  it('不放行危险 scheme', () => {
    expect(isSafeExternalLink('javascript:alert(1)')).toBe(false)
    expect(isSafeExternalLink('data:text/html,<script>')).toBe(false)
    expect(isSafeExternalLink('mailto:a@b.com')).toBe(false)
  })
})

describe('confirmOpenMessage', () => {
  it('包含目标 URL 供核对', () => {
    const url = 'https://github.com/wcpe/JianManager/releases'
    expect(confirmOpenMessage(url)).toContain(url)
  })
})
