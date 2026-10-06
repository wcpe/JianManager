
/**
 * 悬停预取防抖时长（FR-297）：快速掠过的行不触发请求，
 * 停留超过该时长才视为「有进入意图」而预取实例详情。
 */
export const INSTANCE_PREFETCH_DELAY_MS = 150

/** 防抖悬停预取器：enter 起计时、期间 leave / 换行则取消，仅稳定悬停触发一次预取。 */
export interface HoverPrefetcher {
  /** 悬停进入某实例行：重置计时并对该 id 起 150ms 防抖计时。 */
  enter: (id: number) => void
  /** 悬停离开：取消未触发的预取。 */
  leave: () => void
  /** 组件卸载清理：等价 leave。 */
  cancel: () => void
}

/**
 * 创建防抖悬停预取器（纯逻辑，供单测与多入口复用）。
 * 预取动作经回调注入，本身不耦合 TanStack Query。
 */
export function createHoverPrefetcher(
  prefetch: (id: number) => void,
  delayMs = INSTANCE_PREFETCH_DELAY_MS,
): HoverPrefetcher {
  let timer: ReturnType<typeof setTimeout> | null = null
  const cancel = () => {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }
  return {
    enter(id: number) {
      cancel()
      timer = setTimeout(() => {
        timer = null
        prefetch(id)
      }, delayMs)
    },
    leave: cancel,
    cancel,
  }
}