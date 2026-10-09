import { describe, expect, it } from 'vitest'
import { maskInstallId, maskMachineId, maskPlayerName } from './privacy-mask'

describe('privacy-mask（FR-360）', () => {
  it('maskPlayerName 短名不改、长名尾截断', () => {
    expect(maskPlayerName('Alex')).toBe('Alex')
    expect(maskPlayerName('VeryLongPlayerNameHere', 10)).toBe('VeryLongP…')
    expect(maskPlayerName('')).toBe('')
    expect(maskPlayerName(null)).toBe('')
  })

  it('maskMachineId 前6后4；过短全掩', () => {
    expect(maskMachineId('abcdef1234567890')).toBe('abcdef…7890')
    expect(maskMachineId('short')).toBe('***')
    expect(maskMachineId('')).toBe('')
    expect(maskInstallId('install-id-value-xyz')).toBe('instal…-xyz')
  })
})
