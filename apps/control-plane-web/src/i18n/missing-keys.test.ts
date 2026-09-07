import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import zh from './zh.json'
import en from './en.json'

// 全量 i18n key 缺失检测（静态 best-effort）：
// 扫描 src/**/*.{ts,tsx} 中所有静态 i18next 翻译调用的首参字符串字面量，
// 断言每个 key 同时存在于 zh.json 与 en.json。
// 背景：clientDistMonitor.downloadBytesTrend 等 104 个 key 在代码中使用、
// 但语言包从未定义（多数有 defaultValue 中文兜底，英文界面会漏出中文兜底；
// 少数如 downloadBytesTrend / serverConsole.noSeriesYet 无兜底，直接渲染裸 key）。
// 动态拼接 key（t(`ns.${x}`)）无法静态枚举，不在本测试覆盖范围。

const i18nDir = dirname(fileURLToPath(import.meta.url))
const srcDir = join(i18nDir, '..')

function flattenKeys(obj: Record<string, unknown>, prefix = ''): Set<string> {
  const out = new Set<string>()
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') {
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
    else if (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) out.push(full)
  }
  return out
}

// 与仓库 i18n 审计脚本一致的提取规则：t( 或 i18n.t(，首参为单/双引号字面量
const CALL_RE = /(?:^|[^\w.])(?:i18n\.)?\bt(?:<[^>]*>)?\(\s*(['"])([^'"\n]+)\1/g

function collectUsedKeys(): Map<string, string[]> {
  const used = new Map<string, string[]>()
  for (const file of listSourceFiles(srcDir)) {
    const text = readFileSync(file, 'utf-8')
    for (const m of text.matchAll(CALL_RE)) {
      const key = m[2]
      const rel = file.replace(srcDir + '\\', '').replace(srcDir + '/', '')
      const list = used.get(key) ?? []
      if (!list.includes(rel)) list.push(rel)
      used.set(key, list)
    }
  }
  return used
}

describe('i18n 语言包完整性', () => {
  const zhKeys = flattenKeys(zh)
  const enKeys = flattenKeys(en)
  const used = collectUsedKeys()

  it('zh/en 两个语言包 key 集合完全一致', () => {
    const onlyZh = [...zhKeys].filter((k) => !enKeys.has(k))
    const onlyEn = [...enKeys].filter((k) => !zhKeys.has(k))
    expect(onlyZh, '仅在 zh.json 中存在的 key').toEqual([])
    expect(onlyEn, '仅在 en.json 中存在的 key').toEqual([])
  })

  it('代码中使用的每个静态 t() key 在 zh/en 中都有定义', () => {
    const missing = [...used.entries()]
      .filter(([k]) => !zhKeys.has(k) || !enKeys.has(k))
      .map(([k, files]) => `${k}  <-  ${files.join(', ')}`)
    expect(missing, '语言包缺失的 key（zh 或 en）').toEqual([])
  })
})
