import { describe, it, expect } from 'vitest'
import {
  emptySelection,
  clickSelect,
  selectAll,
  clearSelection,
  isSelected,
  pruneSelection,
} from './selection'

const keys = ['a', 'b', 'c', 'd', 'e']

describe('clickSelect — plain click', () => {
  it('single-selects and sets anchor', () => {
    const s = clickSelect(emptySelection(), 'c', keys)
    expect([...s.selected]).toEqual(['c'])
    expect(s.anchor).toBe('c')
  })
  it('replaces previous selection', () => {
    let s = clickSelect(emptySelection(), 'a', keys)
    s = clickSelect(s, 'd', keys)
    expect([...s.selected]).toEqual(['d'])
    expect(s.anchor).toBe('d')
  })
})

describe('clickSelect — ctrl/meta toggle', () => {
  it('adds and removes individual items, moving anchor', () => {
    let s = clickSelect(emptySelection(), 'a', keys)
    s = clickSelect(s, 'c', keys, { ctrlOrMeta: true })
    expect([...s.selected].sort()).toEqual(['a', 'c'])
    expect(s.anchor).toBe('c')
    s = clickSelect(s, 'a', keys, { ctrlOrMeta: true })
    expect([...s.selected]).toEqual(['c'])
    expect(s.anchor).toBe('a')
  })
})

describe('clickSelect — shift range', () => {
  it('selects contiguous range from anchor (forward)', () => {
    let s = clickSelect(emptySelection(), 'b', keys) // anchor b
    s = clickSelect(s, 'd', keys, { shift: true })
    expect([...s.selected]).toEqual(['b', 'c', 'd'])
    expect(s.anchor).toBe('b') // anchor unchanged
  })
  it('selects range backward and keeps anchor', () => {
    let s = clickSelect(emptySelection(), 'd', keys)
    s = clickSelect(s, 'b', keys, { shift: true })
    expect([...s.selected]).toEqual(['b', 'c', 'd'])
    expect(s.anchor).toBe('d')
  })
  it('re-shift from same anchor replaces range', () => {
    let s = clickSelect(emptySelection(), 'b', keys)
    s = clickSelect(s, 'e', keys, { shift: true })
    s = clickSelect(s, 'c', keys, { shift: true })
    expect([...s.selected]).toEqual(['b', 'c'])
  })
  it('falls back to single select when no anchor', () => {
    const s = clickSelect(emptySelection(), 'c', keys, { shift: true })
    expect([...s.selected]).toEqual(['c'])
    expect(s.anchor).toBe('c')
  })
})

describe('selectAll / clearSelection', () => {
  it('selects every key with first as anchor', () => {
    const s = selectAll(keys)
    expect([...s.selected]).toEqual(keys)
    expect(s.anchor).toBe('a')
  })
  it('clears to empty', () => {
    expect(clearSelection().selected.size).toBe(0)
    expect(clearSelection().anchor).toBeNull()
  })
})

describe('isSelected', () => {
  it('reflects membership', () => {
    const s = selectAll(['a', 'b'])
    expect(isSelected(s, 'a')).toBe(true)
    expect(isSelected(s, 'z')).toBe(false)
  })
})

describe('pruneSelection', () => {
  it('drops stale keys after refresh, keeps valid anchor', () => {
    let s = selectAll(['a', 'b', 'c']) // anchor 'a'
    s = pruneSelection(s, ['a', 'c']) // b removed from listing
    expect([...s.selected].sort()).toEqual(['a', 'c'])
    expect(s.anchor).toBe('a') // anchor 'a' still valid
  })
  it('nulls anchor when it becomes stale', () => {
    let s = clickSelect(emptySelection(), 'b', ['a', 'b', 'c']) // anchor 'b'
    s = pruneSelection(s, ['a', 'c']) // b removed
    expect(s.anchor).toBeNull()
    expect(s.selected.size).toBe(0)
  })
  it('keeps anchor if still valid', () => {
    let s = clickSelect(emptySelection(), 'b', ['a', 'b', 'c'])
    s = pruneSelection(s, ['a', 'b'])
    expect(s.anchor).toBe('b')
    expect([...s.selected]).toEqual(['b'])
  })
})
