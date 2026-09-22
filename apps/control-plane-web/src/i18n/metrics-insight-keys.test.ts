import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import zh from './zh.json'
import en from './en.json'

/**
 * FR-463/464/465/469 观测增强命名空间（slo / capacity / attribution / ranking / playerTrend
 * 及 `metrics.gc*`）的 i18n 守护。
 *
 * 背景：既有的 `missing-keys.test.ts` 只查「代码引用的键是否存在」，**不查反向**
 * ——孤儿键（定义了但从不引用）会静默堆积。本次自审（M6）实测新增 83 键中有 9 键零引用，
 * 其中 3 个带插值的单位格式键被硬编码 `` `${v} ms` `` 取代（导致单位不可本地化），
 * 而它们的存在又让人误以为已走 i18n。故本测试补上两个方向：
 *  ① zh/en 在本组命名空间上的叶子键集合必须完全一致；
 *  ② 本组命名空间的每个键都必须被源码**精确引用**（动态拼接的 `capacity.${confidence}`
 *     三态白名单除外），防止孤儿键回潮。
 */

const i18nDir = dirname(fileURLToPath(import.meta.url))
const srcDir = join(i18nDir, '..')

/** 本组命名空间前缀（含 `metrics.gc` 前缀：GC 曲线的两项标题）。 */
const NAMESPACES = ['slo.', 'capacity.', 'attribution.', 'ranking.', 'playerTrend.']
/** 跨命名空间但属本 FR 的键前缀（GC 曲线标题刻意放在 metrics 下，见 M4）。 */
const EXTRA_PREFIXES = ['metrics.gc']

function flattenKeys(obj: Record<string, unknown>, prefix = ''): Set<string> {
  const out = new Set<string>()
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      for (const p of flattenKeys(v as Record<string, unknown>, path)) out.add(p)
    } else {
      out.add(path)
    }
  }
  return out
}

function listSourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listSourceFiles(full))
    else if (/\.(ts|tsx)$/.test(entry.name)) out.push(full)
  }
  return out
}

function collectSourceBlob(): string {
  return listSourceFiles(srcDir)
    .filter((f) => statSync(f).isFile())
    .map((f) => readFileSync(f, 'utf-8'))
    .join('\n')
}

const inScope = (k: string) => NAMESPACES.some((n) => k.startsWith(n)) || EXTRA_PREFIXES.includes(k)

describe('观测增强 i18n（FR-463/464/465/469）', () => {
  const zhKeys = [...flattenKeys(zh as Record<string, unknown>)].filter(inScope).sort()
  const enKeys = [...flattenKeys(en as Record<string, unknown>)].filter(inScope).sort()
  const blob = collectSourceBlob()

  it('zh/en 在本组命名空间上的叶子键集合完全一致（逐键对称）', () => {
    expect(zhKeys.length, '本组命名空间叶子键数').toBeGreaterThan(0)
    expect(zhKeys, 'zh 与 en 的本组键集合应逐键对称').toEqual(enKeys)
  })

  it('插值变量在 zh/en 两侧一致（避免 {{value}} 只在一侧）', () => {
    const vars = (s: unknown) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort()
    const flatZh = new Map<string, unknown>()
    const flatEn = new Map<string, unknown>()
    const walk = (obj: Record<string, unknown>, prefix: string, into: Map<string, unknown>) => {
      for (const [k, v] of Object.entries(obj)) {
        const path = prefix ? `${prefix}.${k}` : k
        if (v && typeof v === 'object' && !Array.isArray(v)) walk(v as Record<string, unknown>, path, into)
        else into.set(path, v)
      }
    }
    walk(zh as Record<string, unknown>, '', flatZh)
    walk(en as Record<string, unknown>, '', flatEn)
    const mismatch = [...flatZh.entries()]
      .filter(([k]) => inScope(k) && flatEn.has(k))
      .filter(([k, v]) => JSON.stringify(vars(v)) !== JSON.stringify(vars(flatEn.get(k))))
      .map(([k]) => k)
    expect(mismatch, '插值变量不一致的键').toEqual([])
  })

  it('本组每个键都被源码精确引用（无孤儿键，M6）', () => {
    // `capacity.high|low|insufficient` 由 `t(`capacity.${item.confidence}`)` 动态拼接消费，
    // 无法静态精确匹配，故显式白名单；其余键必须字面量出现。
    const dynamicWhitelist = new Set(['capacity.high', 'capacity.low', 'capacity.insufficient'])
    const orphans = zhKeys
      .filter((k) => !dynamicWhitelist.has(k))
      .filter((k) => !blob.includes(`'${k}'`) && !blob.includes(`"${k}"`) && !blob.includes('`' + k + '`'))
    expect(orphans, '定义了但零引用的孤儿键').toEqual([])
  })
})
