import { describe, it, expect } from 'vitest'
import type { AssetInfo, AssetTypeGroup, JDKMatrixItem } from '@/api/runtimeAssets'
import {
  formatBytes,
  buildJDKMatrix,
  buildRuntimeGrid,
  filterAssetGroups,
  shortSha,
  DEFAULT_ASSET_FILTER,
  RUNTIME_TYPE_LABEL,
} from './runtime-assets-view'

function jdk(p: Partial<JDKMatrixItem>): JDKMatrixItem {
  return {
    id: 1,
    nodeId: 1,
    nodeName: 'n1',
    nodeOnline: true,
    vendor: 'Temurin',
    majorVersion: 21,
    version: '21.0.4',
    arch: 'x64',
    path: '/opt/jdk',
    managed: true,
    instances: [],
    refCount: 0,
    ...p,
  }
}

function asset(p: Partial<AssetInfo>): AssetInfo {
  return {
    id: 1,
    type: 'core',
    name: 'paper',
    version: '1.20.4',
    filename: 'paper.jar',
    sha256: 'abc123def456ffff',
    md5: '',
    size: 1000,
    contentType: '',
    sourceUrl: '',
    metadata: '',
    storageState: 'hot',
    storageBackend: 'local',
    refCount: 0,
    relPath: '',
    createdAt: '',
    lastUsedAt: null,
    ...p,
  }
}

describe('formatBytes', () => {
  it('handles zero and negatives', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(NaN)).toBe('0 B')
  })
  it('scales units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1024 * 1024 * 5)).toBe('5 MB')
  })
})

describe('buildJDKMatrix', () => {
  it('builds rows/columns and merges same vendor-major into one cell', () => {
    const items = [
      jdk({ id: 10, nodeId: 1, nodeName: 'alpha', majorVersion: 21, version: '21.0.1', refCount: 1 }),
      jdk({ id: 11, nodeId: 1, nodeName: 'alpha', majorVersion: 21, version: '21.0.4', refCount: 2 }),
      jdk({ id: 20, nodeId: 2, nodeName: 'beta', majorVersion: 17, version: '17.0.9', refCount: 0 }),
    ]
    const m = buildJDKMatrix(items)
    // 列按 major 降序：21 在 17 前
    expect(m.columns.map((c) => c.key)).toEqual(['Temurin-21', 'Temurin-17'])
    // 行按节点名升序
    expect(m.rows.map((r) => r.nodeName)).toEqual(['alpha', 'beta'])
    // alpha 的 Temurin-21 格合并两条、引用累加 3
    const alpha = m.rows.find((r) => r.nodeName === 'alpha')!
    expect(alpha.cells['Temurin-21'].items).toHaveLength(2)
    expect(alpha.cells['Temurin-21'].refCount).toBe(3)
    // beta 无 Temurin-21 格
    const beta = m.rows.find((r) => r.nodeName === 'beta')!
    expect(beta.cells['Temurin-21']).toBeUndefined()
    expect(beta.cells['Temurin-17'].refCount).toBe(0)
  })

  it('returns empty for no items', () => {
    const m = buildJDKMatrix([])
    expect(m.columns).toEqual([])
    expect(m.rows).toEqual([])
  })
})

describe('filterAssetGroups', () => {
  const groups: AssetTypeGroup[] = [
    {
      type: 'core',
      items: [asset({ id: 1, name: 'paper', refCount: 2 }), asset({ id: 2, name: 'purpur', refCount: 0 })],
      count: 2,
      totalSize: 2000,
      referencedCount: 1,
      hotCount: 2,
      archivedCount: 0,
      externalCount: 0,
    },
    {
      type: 'plugin',
      items: [asset({ id: 3, type: 'plugin', name: 'essentials', refCount: 0 })],
      count: 1,
      totalSize: 500,
      referencedCount: 0,
      hotCount: 1,
      archivedCount: 0,
      externalCount: 0,
    },
  ]

  it('default filter returns all groups unchanged in length', () => {
    const out = filterAssetGroups(groups, DEFAULT_ASSET_FILTER)
    expect(out).toHaveLength(2)
  })

  it('filters by type', () => {
    const out = filterAssetGroups(groups, { ...DEFAULT_ASSET_FILTER, type: 'plugin' })
    expect(out).toHaveLength(1)
    expect(out[0].type).toBe('plugin')
  })

  it('onlyReferenced drops unreferenced items and empty groups', () => {
    const out = filterAssetGroups(groups, { ...DEFAULT_ASSET_FILTER, onlyReferenced: true })
    // plugin 组全无引用被剔除；core 组只剩 paper
    expect(out).toHaveLength(1)
    expect(out[0].type).toBe('core')
    expect(out[0].items.map((a) => a.name)).toEqual(['paper'])
  })

  it('search matches name case-insensitively', () => {
    const out = filterAssetGroups(groups, { ...DEFAULT_ASSET_FILTER, search: 'PURPUR' })
    expect(out).toHaveLength(1)
    expect(out[0].items.map((a) => a.name)).toEqual(['purpur'])
  })
})

describe('shortSha', () => {
  it('takes first 12 chars', () => {
    expect(shortSha('abcdef0123456789')).toBe('abcdef012345')
    expect(shortSha('')).toBe('')
  })
})

describe('buildRuntimeGrid（FR-301 多运行时矩阵）', () => {
  function rt(p: Partial<import('@/api/runtimeAssets').RuntimeMatrixEntry>): import('@/api/runtimeAssets').RuntimeMatrixEntry {
    return {
      id: 1,
      nodeId: 1,
      nodeName: 'n1',
      nodeOnline: true,
      type: 'jdk',
      name: 'Temurin',
      majorVersion: 21,
      version: '21.0.4',
      arch: 'x64',
      path: '/opt/jdk',
      managed: true,
      instances: [],
      refCount: 0,
      ...p,
    }
  }

  it('多类型分列：jdk 列在前（type 升序），同类型内 major 降序', () => {
    const grid = buildRuntimeGrid([
      rt({ id: 1, type: 'nodejs', name: 'Node.js 22', majorVersion: 22 }),
      rt({ id: 2, type: 'jdk', name: 'Temurin', majorVersion: 17 }),
      rt({ id: 3, type: 'jdk', name: 'Temurin', majorVersion: 21 }),
    ])
    expect(grid.columns.map((c) => `${c.type}:${c.label}-${c.majorVersion}`)).toEqual([
      'jdk:Temurin-21',
      'jdk:Temurin-17',
      'nodejs:Node.js-22',
    ])
  })

  it('不同类型同 major 不合并为同一列', () => {
    const grid = buildRuntimeGrid([
      rt({ id: 1, type: 'jdk', name: 'Temurin', majorVersion: 22 }),
      rt({ id: 2, type: 'nodejs', name: 'Node.js 22', majorVersion: 22, nodeId: 1 }),
    ])
    expect(grid.columns).toHaveLength(2)
    expect(grid.rows).toHaveLength(1)
    expect(Object.keys(grid.rows[0].cells)).toHaveLength(2)
  })

  it('同节点同列多条合并累加引用数；跨节点分行', () => {
    const grid = buildRuntimeGrid([
      rt({ id: 1, nodeId: 1, nodeName: 'a', refCount: 2 }),
      rt({ id: 2, nodeId: 1, nodeName: 'a', version: '21.0.1', refCount: 1 }),
      rt({ id: 3, nodeId: 2, nodeName: 'b', type: 'nodejs', name: 'Node.js 22', majorVersion: 22 }),
    ])
    expect(grid.rows.map((r) => r.nodeName)).toEqual(['a', 'b'])
    const jdkKey = grid.columns.find((c) => c.type === 'jdk')!.key
    expect(grid.rows[0].cells[jdkKey].items).toHaveLength(2)
    expect(grid.rows[0].cells[jdkKey].refCount).toBe(3)
    // b 节点无 jdk 列格。
    expect(grid.rows[1].cells[jdkKey]).toBeUndefined()
  })

  it('非 jdk 列 label 用类型展示名（RUNTIME_TYPE_LABEL），未知类型回退原样', () => {
    const grid = buildRuntimeGrid([
      rt({ id: 1, type: 'nodejs', name: 'Node.js 22', majorVersion: 22 }),
      rt({ id: 2, type: 'ruby', name: 'Ruby 3', majorVersion: 3 }),
    ])
    const labels = grid.columns.map((c) => c.label)
    expect(labels).toContain('Node.js')
    expect(labels).toContain('ruby')
    expect(RUNTIME_TYPE_LABEL.jdk).toBe('JDK')
  })
})
