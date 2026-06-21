import { describe, it, expect } from 'vitest'
import { isSaveKey } from './save-key'

describe('isSaveKey', () => {
  it('matches Ctrl+S and Cmd+S (lower and upper case)', () => {
    expect(isSaveKey({ key: 's', ctrlKey: true })).toBe(true)
    expect(isSaveKey({ key: 'S', ctrlKey: true })).toBe(true)
    expect(isSaveKey({ key: 's', metaKey: true })).toBe(true)
  })
  it('does not match plain s or other modifiers', () => {
    expect(isSaveKey({ key: 's' })).toBe(false)
    expect(isSaveKey({ key: 'a', ctrlKey: true })).toBe(false)
    expect(isSaveKey({ key: 'Enter', ctrlKey: true })).toBe(false)
  })
})
