import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const repoRoot = path.resolve(__dirname, '../../..')

function readDoc(relativePath: string) {
  return readFileSync(path.join(repoRoot, relativePath), 'utf8')
}

describe('console redesign documentation', () => {
  it('marks FR-267~272 as delivered in the PRD', () => {
    const prd = readDoc('docs/PRD.md')

    for (const fr of ['FR-267', 'FR-268', 'FR-269', 'FR-270', 'FR-271', 'FR-272']) {
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
    for (const fr of ['FR-267', 'FR-268', 'FR-269', 'FR-270', 'FR-271', 'FR-272']) {
      expect(spec).toContain(`- [x] ${fr}`)
    }
  })
})
