import { describe, it, expect } from 'vitest'
import { commentTokensFor, commentTokensForFilename } from './comment'

describe('commentTokensFor', () => {
  it('uses # for yaml/properties/toml/plain', () => {
    expect(commentTokensFor('yaml')).toEqual({ line: '#' })
    expect(commentTokensFor('properties')).toEqual({ line: '#' })
    expect(commentTokensFor('toml')).toEqual({ line: '#' })
    expect(commentTokensFor('plain')).toEqual({ line: '#' })
  })
  it('uses // and block comment for json', () => {
    expect(commentTokensFor('json')).toEqual({ line: '//', block: { open: '/*', close: '*/' } })
  })
})

describe('commentTokensForFilename', () => {
  it('uses block comment for html/xml/svg', () => {
    expect(commentTokensForFilename('index.html', 'plain')).toEqual({
      block: { open: '<!--', close: '-->' },
    })
    expect(commentTokensForFilename('plugin.xml', 'plain')).toEqual({
      block: { open: '<!--', close: '-->' },
    })
    expect(commentTokensForFilename('icon.svg', 'plain')).toEqual({
      block: { open: '<!--', close: '-->' },
    })
  })
  it('uses -- for sql and lua', () => {
    expect(commentTokensForFilename('init.sql', 'plain').line).toBe('--')
    expect(commentTokensForFilename('script.lua', 'plain').line).toBe('--')
  })
  it('falls back to kind-based tokens for non-special extensions', () => {
    expect(commentTokensForFilename('config.yml', 'yaml')).toEqual({ line: '#' })
    expect(commentTokensForFilename('data.json', 'json')).toEqual({
      line: '//',
      block: { open: '/*', close: '*/' },
    })
    expect(commentTokensForFilename('latest.log', 'plain')).toEqual({ line: '#' })
  })
  it('handles names without extension', () => {
    expect(commentTokensForFilename('Dockerfile', 'plain')).toEqual({ line: '#' })
  })
})
