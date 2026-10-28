/**
 * 首页模式偏好（FR-520 §4.1/§4.2）：记住上次选择的首页模式，存前端并允许重置。
 *
 * 【为什么不放 zustand store】
 * 模式决定**渲染哪一整棵树**（AI 首页 / 经典仪表盘），必须在首次渲染前同步得出。
 * 走 store 的话要先订阅、再在 effect 里读到值，首帧会先按默认渲染再切走——用户会看到
 * 一次闪回，而且两棵树的 chunk 都会被拉下来（正好破掉「按需挂载」这个目的）。
 * 因此这里是一个**同步读**的纯函数模块：读取发生在 render 期，不存在 effect 时序问题。
 */

/** 存储键。与 stores/console.ts 的 `console.*` 前缀风格一致。 */
const HOME_MODE_KEY = 'console.homeMode'

/** 首页模式。 */
export type HomeMode = 'assistant' | 'classic'

/** 默认模式：AI 首页（FR-520 验收「直接进入首页默认 AI 模式」）。 */
export const DEFAULT_HOME_MODE: HomeMode = 'assistant'

/**
 * 从 URL 的 `home` 参数解析显式模式。
 *
 * 只有 `classic` / `assistant` 两个值有意义；其它取值（如 `?home=foo`）按**未指定**处理，
 * 与 spec §4.1「非 legal 取值按无参数处理」一致——不报错页，也不写记忆。
 */
export function parseHomeModeParam(home: string | null): HomeMode | null {
  if (home === 'classic') return 'classic'
  if (home === 'assistant') return 'assistant'
  return null
}

/**
 * 读取记忆的首页模式。
 *
 * 非法值 / 无 localStorage / 读取抛错（隐私模式）一律回落 `null`（= 未记忆），
 * 由调用方继续回落到默认值。**不抛错**：一个偏好读失败不该让整站白屏。
 */
export function loadHomeMode(): HomeMode | null {
  if (typeof localStorage === 'undefined') return null
  let raw: string | null = null
  try {
    raw = localStorage.getItem(HOME_MODE_KEY)
  } catch {
    return null
  }
  if (raw === 'classic' || raw === 'assistant') return raw
  return null
}

/**
 * 写入记忆。**仅在用户主动切换时调用**。
 *
 * 深链（`?home=classic` / `?home=assistant`）**不写记忆**：从文档或告警里点进来一次，
 * 不该永久改变他下次打开首页的模式（spec §4.1）。
 *
 * 传入默认值时**移除键**而不是写入默认值：这样「偏好 == 默认」与「从未设置」等价，
 * 默认值将来若调整，没表过态的用户会跟着走，而不是被一个陈旧的 'assistant' 钉住。
 */
export function saveHomeMode(mode: HomeMode): void {
  if (typeof localStorage === 'undefined') return
  try {
    if (mode === DEFAULT_HOME_MODE) localStorage.removeItem(HOME_MODE_KEY)
    else localStorage.setItem(HOME_MODE_KEY, mode)
  } catch {
    // 存储不可用（配额/隐私模式）时静默降级：模式仍按本次选择生效，只是不记忆。
  }
}

/** 清除记忆（重置入口）。清除后回到默认 AI 模式。 */
export function resetHomeMode(): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.removeItem(HOME_MODE_KEY)
  } catch {
    // 同上：清不掉也不该抛错打断交互。
  }
}

/**
 * 解析本轮应渲染的模式。**优先级：显式 URL 参数 > 记忆 > 默认**（spec §4.1 冻结）。
 *
 * 这是 render 期直接调用的纯函数，因此冷启动不会闪回。
 */
export function resolveHomeMode(param: HomeMode | null): HomeMode {
  return param ?? loadHomeMode() ?? DEFAULT_HOME_MODE
}

/**
 * 构造「切换首页模式」的目标 URL：**保留除 `home` 之外的现有查询参数**
 * （FR-520 §8「切换保留其它合法查询参数」）。
 *
 * 为什么不能写死 `'/'` 或 `'/?home=classic'`：用户可能正带着筛选条件（如经典首页的
 * `range=7d`）在用，切一下首页模式就把条件清空，等于迫使他回来重设——尤其是在
 * 两个模式之间来回对比时，这种丢失很烦人。
 *
 * `mode` 传 `null` 表示目标是「默认模式」：此时必须**删掉** `home` 参数，
 * 而不是留空——留着旧值会让 URL 参数继续压过记忆，切换看起来没生效。
 */
export function homeModeHref(mode: HomeMode | null, search: string): string {
  const params = new URLSearchParams(search)
  params.delete('home')
  if (mode) params.set('home', mode)
  const qs = params.toString()
  return qs ? `/?${qs}` : '/'
}
