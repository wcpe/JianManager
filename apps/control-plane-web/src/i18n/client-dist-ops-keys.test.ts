import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import zh from './zh.json'
import en from './en.json'

/**
 * 页面 B「客户端分发运维」（FR-430）· i18n 键存在性补充校验（QA 独立补充）。
 *
 * 背景：既有 `missing-keys.test.ts` 只静态扫描 t 后紧跟引号字面量的调用形态，**漏掉间接键**：
 *  - `labelKey: 'clientDistOps.segLive'` 经 `t(o.labelKey)` 消费；
 *  - `KPI_I18N.xxx` 等常量经变量传入 `t()`。
 * 这些键若缺失只会静默走 `t(key, '中文兜底')` 或渲染裸键，出英文界面时暴露中文。
 *
 * 本测试改为「抓取页面 B 源码里出现的所有 `clientDistOps.*` 字符串字面量」，
 * 断言其同时存在于 zh.json 与 en.json，并校验该命名空间 zh/en 叶子键集合完全一致。
 * （动态拼接键 `t(\`ns.${x}\`)` 仍不在静态扫描范围。）
 */

const i18nDir = dirname(fileURLToPath(import.meta.url))
const srcDir = join(i18nDir, '..')

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

/** 页面 B 相关源码文件（相对 src）——外壳 + 安全侧区块 + 迁移组件 + 不可信徽标。 */
function pageBFiles(): string[] {
  const files = ['pages/ProtectionCenterPage.tsx', 'components/UntrustedFieldBadge.tsx']
  const distDir = join(srcDir, 'components', 'client-dist')
  for (const entry of readdirSync(distDir, { withFileTypes: true })) {
    if (!entry.isFile()) continue
    if (!/\.(ts|tsx)$/.test(entry.name)) continue
    if (/\.(dom\.)?test\.(ts|tsx)$/.test(entry.name)) continue
    files.push(`components/client-dist/${entry.name}`)
  }
  return files
}

/** 抓取任意引号内的 `clientDistOps.*` 字面量（不要求紧跟 `t(`）——覆盖间接键。 */
const KEY_RE = /(['"])(clientDistOps\.[A-Za-z0-9_.]+)\1/g

function collectReferencedKeys(): Map<string, string[]> {
  const refs = new Map<string, string[]>()
  for (const rel of pageBFiles()) {
    const text = readFileSync(join(srcDir, rel), 'utf-8')
    for (const m of text.matchAll(KEY_RE)) {
      const key = m[2]
      const list = refs.get(key) ?? []
      if (!list.includes(rel)) list.push(rel)
      refs.set(key, list)
    }
  }
  return refs
}

describe('clientDistOps i18n（页面 B · FR-430）', () => {
  const zhKeys = flattenKeys(zh as Record<string, unknown>)
  const enKeys = flattenKeys(en as Record<string, unknown>)
  const referenced = collectReferencedKeys()

  it('clientDistOps 命名空间 zh/en 叶子键集合完全一致', () => {
    const zhOps = [...zhKeys].filter((k) => k.startsWith('clientDistOps.')).sort()
    const enOps = [...enKeys].filter((k) => k.startsWith('clientDistOps.')).sort()
    expect(zhOps, 'zh clientDistOps 叶子键').toEqual(enOps)
    expect(zhOps.length).toBeGreaterThan(0)
  })

  it('页面 B 源码引用的每个 clientDistOps.* 键在 zh/en 都有定义（不靠兜底）', () => {
    expect(referenced.size).toBeGreaterThan(0)
    const missing = [...referenced.entries()]
      .filter(([k]) => !zhKeys.has(k) || !enKeys.has(k))
      .map(([k, files]) => `${k}  <-  ${files.join(', ')} (zh=${zhKeys.has(k)}, en=${enKeys.has(k)})`)
    expect(missing, '页面 B 引用但语言包缺失的键').toEqual([])
  })
})
