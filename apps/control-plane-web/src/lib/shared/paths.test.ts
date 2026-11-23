import { describe, it, expect } from 'vitest'
import {
  joinPath,
  parentDir,
  baseName,
  extName,
  breadcrumbs,
  isWithin,
  isValidName,
} from './paths'

describe('joinPath', () => {
  it('joins dir and name; empty dir means root', () => {
    expect(joinPath('', 'a.txt')).toBe('a.txt')
    expect(joinPath('plugins', 'a.yml')).toBe('plugins/a.yml')
    expect(joinPath('a/b', 'c')).toBe('a/b/c')
  })
})

describe('parentDir', () => {
  it('returns parent or empty for root-level', () => {
    expect(parentDir('a.txt')).toBe('')
    expect(parentDir('plugins/a.yml')).toBe('plugins')
    expect(parentDir('a/b/c')).toBe('a/b')
  })
})

describe('baseName', () => {
  it('returns last segment', () => {
    expect(baseName('a.txt')).toBe('a.txt')
    expect(baseName('plugins/Essentials/config.yml')).toBe('config.yml')
  })
})

describe('extName', () => {
  it('returns lowercase extension without dot', () => {
    expect(extName('config.YML')).toBe('yml')
    expect(extName('server.properties')).toBe('properties')
    expect(extName('plugins/x.JSON')).toBe('json')
  })
  it('returns empty for no-ext or dotfiles', () => {
    expect(extName('README')).toBe('')
    expect(extName('.gitignore')).toBe('')
    expect(extName('Makefile')).toBe('')
  })
})

describe('breadcrumbs', () => {
  it('splits path into cumulative crumbs', () => {
    expect(breadcrumbs('')).toEqual([])
    expect(breadcrumbs('plugins')).toEqual([{ name: 'plugins', path: 'plugins' }])
    expect(breadcrumbs('plugins/Essentials')).toEqual([
      { name: 'plugins', path: 'plugins' },
      { name: 'Essentials', path: 'plugins/Essentials' },
    ])
  })
})

describe('isWithin', () => {
  it('detects containment; root contains all', () => {
    expect(isWithin('', 'anything/here')).toBe(true)
    expect(isWithin('a', 'a')).toBe(true)
    expect(isWithin('a', 'a/b')).toBe(true)
    expect(isWithin('a', 'ab')).toBe(false)
    expect(isWithin('a/b', 'a')).toBe(false)
  })
})

describe('isValidName', () => {
  it('rejects empty, dot, dotdot, and separators', () => {
    expect(isValidName('config.yml')).toBe(true)
    expect(isValidName('')).toBe(false)
    expect(isValidName('.')).toBe(false)
    expect(isValidName('..')).toBe(false)
    expect(isValidName('a/b')).toBe(false)
    expect(isValidName('a\\b')).toBe(false)
  })
})
