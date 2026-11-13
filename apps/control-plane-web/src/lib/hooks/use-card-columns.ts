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
  if (typeof window === 'undefined') return 4
  /*
    1280 宽下放 4 列：卡片已精简（去掉 40px 图标块、收紧行距），
    单卡宽度约 230px 足以容纳「名称 + 状态 + 类型/端口 + 两条资源条 + 主操作」，
    比原来的 3 列一屏多看 4 台。断点与 Tailwind 的 sm(640)/lg(1024)/xl(1280) 对齐。
  */
  if (window.innerWidth >= 1280) return 4
  if (window.innerWidth >= 1024) return 3
  if (window.innerWidth >= 640) return 2
  return 1
}

/**
 * 随窗口宽度变化返回当前卡片列数（1 / 2 / 3 / 4）。
 *
 * **断点必须与消费方的 CSS 严格一致**：调用方用 `columns` 决定「一行切几张卡」，
 * 而网格列数由 CSS 决定。两者差一档，多出的卡就会被挤到隐式第二行；
 * 虚拟化行盒是固定高度，溢出会压到下一条虚拟行上。
 * 当前口径：<640→1、≥640(sm)→2、≥1024(lg)→3、≥1280(xl)→4。
 */
export function useCardColumns(): number {
  const [columns, setColumns] = useState(readCardColumns)

  useEffect(() => {
    const update = () => setColumns(readCardColumns())
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  return columns
}