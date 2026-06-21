import { describe, it, expect } from 'vitest'
import { languageKindFor, languageExtensionFor } from './language'

describe('languageKindFor', () => {
  it('maps yaml family', () => {
    expect(languageKindFor('config.yml')).toBe('yaml')
    expect(languageKindFor('docker-compose.yaml')).toBe('yaml')
  })
  it('maps json family', () => {
    expect(languageKindFor('a.json')).toBe('json')
    expect(languageKindFor('b.json5')).toBe('json')
  })
  it('maps properties/ini/cfg/conf to properties', () => {
    expect(languageKindFor('server.properties')).toBe('properties')
    expect(languageKindFor('app.ini')).toBe('properties')
    expect(languageKindFor('spigot.cfg')).toBe('properties')
    expect(languageKindFor('redis.conf')).toBe('properties')
  })
  it('maps toml', () => {
    expect(languageKindFor('Cargo.toml')).toBe('toml')
  })
  it('falls back to plain for log/txt/md/sh/unknown/no-ext', () => {
    expect(languageKindFor('latest.log')).toBe('plain')
    expect(languageKindFor('notes.txt')).toBe('plain')
    expect(languageKindFor('README.md')).toBe('plain')
    expect(languageKindFor('start.sh')).toBe('plain')
    expect(languageKindFor('Makefile')).toBe('plain')
    expect(languageKindFor('weird.xyz')).toBe('plain')
  })
  it('is case-insensitive on extension', () => {
    expect(languageKindFor('CONFIG.YML')).toBe('yaml')
    expect(languageKindFor('DATA.JSON')).toBe('json')
  })
})

describe('languageExtensionFor', () => {
  it('returns a non-empty extension for highlighted kinds', () => {
    expect(languageExtensionFor('a.yml').length).toBeGreaterThan(0)
    expect(languageExtensionFor('a.json').length).toBeGreaterThan(0)
    expect(languageExtensionFor('a.properties').length).toBeGreaterThan(0)
    expect(languageExtensionFor('a.toml').length).toBeGreaterThan(0)
  })
  it('returns empty extension array for plain text', () => {
    expect(languageExtensionFor('a.log')).toEqual([])
    expect(languageExtensionFor('Makefile')).toEqual([])
  })
})
