import { useEffect, useState } from 'react'

/**
 * 防抖：value 停止变化 delay 毫秒后才更新返回值，用于搜索输入等高频源
 * （自 BotsPage 抽出共用，FR-336）。
 */
export function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(id)
  }, [value, delay])
  return debounced
}
