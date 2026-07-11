import { describe, it, expect } from 'vitest'
import zh from './zh.json'
import en from './en.json'

/**
 * audit.actions 中英映射完整性（FR-303）。
 * 两语言的键集合必须完全一致，防止后续 FR 只加一侧翻译造成中英漂移；
 * 值必须是非空字符串（审计页直接展示，空串会渲染成空白标签）。
 */

/** 安全取出 audit.actions 命名空间块（缺失时返回空对象，让断言给出可读的失败信息）。 */
function getActions(resource: unknown): Record<string, unknown> {
  const audit = (resource as { audit?: { actions?: Record<string, unknown> } }).audit
  return audit?.actions ?? {}
}

describe('audit.actions 中英映射完整性（FR-303）', () => {
  const zhActions = getActions(zh)
  const enActions = getActions(en)

  it('zh 与 en 的 audit.actions 键集合完全一致', () => {
    expect(Object.keys(zhActions).sort()).toEqual(Object.keys(enActions).sort())
  })

  it('映射非空（覆盖当前代码全量 60+ action 键）', () => {
    expect(Object.keys(zhActions).length).toBeGreaterThanOrEqual(60)
  })

  it('所有翻译值均为非空字符串', () => {
    for (const [key, value] of Object.entries(zhActions)) {
      expect(typeof value === 'string' && value.trim() !== '', `zh audit.actions.${key} 为空`).toBe(true)
    }
    for (const [key, value] of Object.entries(enActions)) {
      expect(typeof value === 'string' && value.trim() !== '', `en audit.actions.${key} 为空`).toBe(true)
    }
  })
})
