import { describe, it, expect } from 'vitest'
import {
  favoritesKey,
  loadFavorites,
  saveFavorites,
  isFavorite,
  toggleFavorite,
  removeFavorite,
  type FavoritesStorage,
} from './favorites'

/** 内存 storage（模拟 localStorage）。 */
function memStorage(initial: Record<string, string> = {}): FavoritesStorage & { data: Record<string, string> } {
  const data: Record<string, string> = { ...initial }
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => {
      data[k] = v
    },
  }
}

describe('favoritesKey', () => {
  it('按实例隔离', () => {
    expect(favoritesKey(1)).not.toBe(favoritesKey(2))
    expect(favoritesKey(7)).toContain('7')
  })
})

describe('loadFavorites', () => {
  it('无 storage 返回空', () => {
    expect(loadFavorites(null, 1)).toEqual([])
  })
  it('无记录返回空', () => {
    expect(loadFavorites(memStorage(), 1)).toEqual([])
  })
  it('读取并去重、过滤空与非字符串', () => {
    const s = memStorage({
      [favoritesKey(1)]: JSON.stringify(['a.yml', 'a.yml', '', 'b.properties', 123, '  c.toml  ']),
    })
    expect(loadFavorites(s, 1)).toEqual(['a.yml', 'b.properties', 'c.toml'])
  })
  it('坏 JSON 容错为空', () => {
    const s = memStorage({ [favoritesKey(1)]: '{not json' })
    expect(loadFavorites(s, 1)).toEqual([])
  })
  it('非数组 JSON 容错为空', () => {
    const s = memStorage({ [favoritesKey(1)]: '{"x":1}' })
    expect(loadFavorites(s, 1)).toEqual([])
  })
})

describe('saveFavorites + loadFavorites 往返', () => {
  it('写后可读且去重', () => {
    const s = memStorage()
    saveFavorites(s, 5, ['x.yml', 'x.yml', 'y.json'])
    expect(loadFavorites(s, 5)).toEqual(['x.yml', 'y.json'])
  })
  it('无 storage 不抛错', () => {
    expect(() => saveFavorites(null, 1, ['a'])).not.toThrow()
  })
})

describe('toggleFavorite / removeFavorite / isFavorite', () => {
  it('toggle 追加与移除', () => {
    expect(toggleFavorite([], 'a.yml')).toEqual(['a.yml'])
    expect(toggleFavorite(['a.yml'], 'a.yml')).toEqual([])
    expect(toggleFavorite(['a.yml'], 'b.yml')).toEqual(['a.yml', 'b.yml'])
  })
  it('toggle 空路径无操作', () => {
    expect(toggleFavorite(['a.yml'], '   ')).toEqual(['a.yml'])
  })
  it('remove 不存在项保持不变', () => {
    expect(removeFavorite(['a.yml'], 'b.yml')).toEqual(['a.yml'])
    expect(removeFavorite(['a.yml', 'b.yml'], 'a.yml')).toEqual(['b.yml'])
  })
  it('isFavorite', () => {
    expect(isFavorite(['a.yml'], 'a.yml')).toBe(true)
    expect(isFavorite(['a.yml'], 'b.yml')).toBe(false)
  })
})
