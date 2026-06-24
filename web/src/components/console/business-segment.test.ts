import { describe, it, expect } from 'vitest'
import { isWriteAction } from './business-actions'
import type { BusinessAction } from '@/api/business'

function action(name: string, readOnly?: boolean): BusinessAction {
  return { action: name, readOnly }
}

describe('isWriteAction（FR-121 写动作判定）', () => {
  it('readOnly=true 为读动作', () => {
    expect(isWriteAction(action('balance', true))).toBe(false)
  })

  it('readOnly=false 为写动作', () => {
    expect(isWriteAction(action('deposit', false))).toBe(true)
  })

  it('readOnly 缺省从严视为写动作', () => {
    expect(isWriteAction(action('deposit'))).toBe(true)
  })
})
