import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  DEFAULT_HOME_MODE,
  loadHomeMode,
  parseHomeModeParam,
  resetHomeMode,
  resolveHomeMode,
  saveHomeMode,
  homeModeHref,
} from './home-mode'

// §4.1/§4.2：首页模式记忆的纯逻辑。
// 这里只测「优先级 / 记忆读写 / 重置 / 非法值回落」，渲染层由 Workspace 的路由分支承担。

const KEY = 'console.homeMode'

describe('home-mode 模式解析与记忆', () => {
  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    localStorage.clear()
  })

  describe('parseHomeModeParam', () => {
    it('只认 classic / assistant，其余一律视为未指定', () => {
      expect(parseHomeModeParam('classic')).toBe('classic')
      expect(parseHomeModeParam('assistant')).toBe('assistant')
      // 非法值不能当成「某个模式」——否则会写进记忆，也很难排查
      expect(parseHomeModeParam('foo')).toBeNull()
      expect(parseHomeModeParam('')).toBeNull()
      expect(parseHomeModeParam(null)).toBeNull()
      // 大小写不宽容：避免 ?home=Classic 与 ?home=classic 两种行为
      expect(parseHomeModeParam('Classic')).toBeNull()
    })
  })

  describe('resolveHomeMode 优先级：显式参数 > 记忆 > 默认', () => {
    it('无参数无记忆 → 默认 AI', () => {
      expect(resolveHomeMode(null)).toBe(DEFAULT_HOME_MODE)
      expect(DEFAULT_HOME_MODE).toBe('assistant')
    })

    it('有记忆 → 用记忆', () => {
      saveHomeMode('classic')
      expect(resolveHomeMode(null)).toBe('classic')
    })

    it('显式参数压过记忆（深链只对本次生效）', () => {
      saveHomeMode('classic')
      expect(resolveHomeMode('assistant')).toBe('assistant')
    })
  })

  describe('saveHomeMode 语义', () => {
    it('选择经典 → 写入键', () => {
      saveHomeMode('classic')
      expect(localStorage.getItem(KEY)).toBe('classic')
      expect(loadHomeMode()).toBe('classic')
    })

    it('选择默认值 → 移除键而不是写入默认值', () => {
      // 这样「偏好 == 默认」与「从未设置」等价：将来默认值若调整，
      // 没表过态的用户会跟着走，而不是被一个陈旧的 'assistant' 钉住。
      saveHomeMode('classic')
      saveHomeMode('assistant')
      expect(localStorage.getItem(KEY)).toBeNull()
      expect(loadHomeMode()).toBeNull()
    })
  })

  describe('loadHomeMode 容错', () => {
    it('非法存量值回落为「未记忆」而不是崩', () => {
      localStorage.setItem(KEY, 'garbage')
      expect(loadHomeMode()).toBeNull()
      expect(resolveHomeMode(null)).toBe(DEFAULT_HOME_MODE)
    })
  })

  describe('resetHomeMode', () => {
    it('清除记忆后回到默认', () => {
      saveHomeMode('classic')
      expect(resolveHomeMode(null)).toBe('classic')
      resetHomeMode()
      expect(localStorage.getItem(KEY)).toBeNull()
      expect(resolveHomeMode(null)).toBe(DEFAULT_HOME_MODE)
    })

    it('重复重置幂等', () => {
      resetHomeMode()
      resetHomeMode()
      expect(loadHomeMode()).toBeNull()
    })
  })
})

/** 
 * 深链**只读不写**（§8「深链不写记忆」）。
 *
 * 这条的性质与上面几条不同：上面测的是「值算得对不对」，这里测的是
 * **解析过程没有副作用**。深链 `/?home=classic` 只应对本次生效；
 * 一旦解析时顺手写了记忆，用户下次直接访问 `/` 就会莫名其妙落到经典首页，
 * 而且这种 bug 在 UI 上表现为「首页偏好自己变了」，极难归因。
 *
 * 验证边界：此处验的是 `parseHomeModeParam` + `resolveHomeMode` 这对纯函数
 * 确实不落盘。真正的端到端（Workspace 挂载后 localStorage 仍空）需要浏览器用例，
 * 代码侧可佐证的是 `Workspace.tsx` 只 import 了这两个读函数、没有 import `saveHomeMode`。
 */
describe('深链不写记忆', () => {
  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    localStorage.clear()
  })

  it('解析 ?home=classic 得到 classic，但不写 localStorage', () => {
    const param = parseHomeModeParam('classic')
    expect(resolveHomeMode(param)).toBe('classic')
    // 关键断言：读一次之后记忆仍为空
    expect(localStorage.getItem(KEY)).toBeNull()
    expect(loadHomeMode()).toBeNull()
  })

  it('解析 ?home=assistant 同样不写', () => {
    const param = parseHomeModeParam('assistant')
    expect(resolveHomeMode(param)).toBe('assistant')
    expect(localStorage.getItem(KEY)).toBeNull()
  })

  it('已有记忆时，深链解析不覆盖既有记忆', () => {
    // 用 classic 作既有记忆：`assistant` 是默认值，`saveHomeMode('assistant')`
    // 会移除键而不是写入（见上面「选择默认值 → 移除键」用例），拿它当记忆会测出假阴性。
    saveHomeMode('classic')
    const param = parseHomeModeParam('assistant')
    expect(resolveHomeMode(param)).toBe('assistant')
    // 本次显示 AI，但记忆仍是 classic
    expect(loadHomeMode()).toBe('classic')
  })
})

/** 
 * 「切换首页模式」的目标 URL（§8「切换保留其它合法查询参数」）。
 *
 * 这条是**实测发现的缺口**：原实现切换时写死 `'/'` 或 `'/?home=classic'`，
 * 用户带着 `range=7d` 切一下模式，筛选条件就没了。
 */
describe('homeModeHref 保留其它查询参数', () => {
  it('切到经典：加 home=classic，其余参数保留', () => {
    expect(homeModeHref('classic', '?range=7d')).toBe('/?range=7d&home=classic')
  })

  it('切到默认模式：删掉 home，其余参数保留', () => {
    // 必须删而不是留着旧值——留着会让 URL 参数继续压过记忆，切换看起来没生效
    expect(homeModeHref(null, '?home=classic&range=7d')).toBe('/?range=7d')
  })

  it('没有任何参数时退化为裸 /', () => {
    expect(homeModeHref(null, '')).toBe('/')
    expect(homeModeHref(null, '?home=classic')).toBe('/')
  })

  it('已有 home 时被覆盖而不是重复拼接', () => {
    expect(homeModeHref('classic', '?home=assistant&tab=x')).toBe('/?tab=x&home=classic')
  })

  it('锚点等无关参数不受影响', () => {
    expect(homeModeHref('classic', '?page=2&sort=name')).toBe('/?page=2&sort=name&home=classic')
  })
})
