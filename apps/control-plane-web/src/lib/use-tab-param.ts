/**
 * 把 Tab 激活态同步到 URL 查询参数（验收矩阵 #11：刷新可恢复当前标签）。
 *
 * 返回受控 `[tab, setTab]`：tab 从 URL `?<key>=` 读取（非法/缺省回落 defaultValue），
 * setTab 以 replace 写回 URL（不堆历史），使刷新后仍停留在同一 Tab。默认值不写入 URL 以保持地址整洁。
 */
import { useCallback } from 'react'
import { useSearchParams } from 'react-router'

export function useTabParam<T extends string>(
  key: string,
  defaultValue: T,
  valid?: readonly T[],
): [T, (value: string) => void] {
  const [searchParams, setSearchParams] = useSearchParams()
  const raw = searchParams.get(key)
  const current: T =
    raw && (!valid || (valid as readonly string[]).includes(raw)) ? (raw as T) : defaultValue

  // setTab 接受 string 以直接兼容 Radix Tabs onValueChange；非法值在下次读取时回落 defaultValue。
  const setTab = useCallback(
    (value: string) => {
      const next = new URLSearchParams(searchParams)
      if (value === defaultValue) next.delete(key)
      else next.set(key, value)
      setSearchParams(next, { replace: true })
    },
    [key, defaultValue, searchParams, setSearchParams],
  )

  return [current, setTab]
}
