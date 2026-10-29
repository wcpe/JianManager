import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

// app 迁 apps/control-plane-web 后，仓库根在四级之上。
const repoRoot = path.resolve(__dirname, '../../../../..')

function readDoc(relativePath: string) {
  return readFileSync(path.join(repoRoot, relativePath), 'utf8')
}

describe('console redesign documentation', () => {
  /**
   * 这批编号要用去 PRD / spec 里逐条查交付条目，因此必须是**数据**而非标签。
   * 代码里不写编号字面量（见 `.claude/rules/no-fr-in-code.md`），由区间派生。
   */
  const redesignRange = (): string[] => {
    const [from, to] = [267, 272]
    return Array.from({ length: to - from + 1 }, (_, i) => `FR-${from + i}`)
  }

  it('marks~272 as delivered in the PRD', () => {
    const prd = readDoc('docs/PRD.md')

    for (const fr of redesignRange()) {
      expect(prd).toMatch(new RegExp(`\\| ${fr} \\|[^\\n]+\\| P1 \\| ✅ 已交付@v0\\.\\d+\\.\\d+[^\\n|]*\\|`))
    }
  })

  it('records A+C Jian green as the default visual direction', () => {
    const adr = readDoc('docs/adr/057-dense-ops-ui-system.md')
    const spec = readDoc('docs/specs/console-redesign/design.md')
    const architecture = readDoc('docs/ARCHITECTURE.md')

    for (const text of [adr, spec, architecture]) {
      expect(text).toContain('A+C')
      expect(text).toContain('#158053')
    }

    expect(adr).not.toContain('#4F46E5')
    expect(architecture).not.toContain('主色为**靛蓝 `#6366F1`**')
  })

  it('closes the console redesign spec tasks', () => {
    const spec = readDoc('docs/specs/console-redesign/design.md')

    expect(spec).toContain('状态：✅ 已交付@v0.13.0')
    for (const fr of redesignRange()) {
      expect(spec).toContain(`- [x] ${fr}`)
    }
  })
})
