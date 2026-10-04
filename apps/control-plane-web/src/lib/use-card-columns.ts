import { useEffect, useState } from 'react'

/**
 * 实例卡片网格的列数（供虚拟化按「行」计算窗口）。
 *
 * 【为什么用 window.innerWidth 而不是容器宽度】本网格所在内容区的宽度由侧栏开合决定，
 * 而侧栏动画期间宽度**逐帧**变化——读容器宽会让列数在整个过渡里反复抖动，虚拟化的行高
 * 与占位随之乱跳。窗口宽度只在真正 resize 时变，且下面的断点与 Tailwind 的 sm(640)/
 * xl(1280) 硬对齐，能保证「虚拟化以为的列数」== 「CSS 实际渲染的列数」。
 */
export function readCardColumns(): number {
  if (typeof window === 'undefined') return 3
  if (window.innerWidth >= 1280) return 3
  if (window.innerWidth >= 640) return 2
  return 1
}

/** 随窗口宽度变化返回当前卡片列数（1 / 2 / 3）。 */
export function useCardColumns(): number {
  const [columns, setColumns] = useState(readCardColumns)

  useEffect(() => {
    const update = () => setColumns(readCardColumns())
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  return columns
}
