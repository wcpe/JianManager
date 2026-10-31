import type { HealthMetrics } from './InstanceRowSpark'

/**
 * 模块级桥：格子（InstanceRowView）→ 悬浮层（InstanceRowSpark）的事件通道。
 *
 * 【为什么单独一个文件】`InstanceRowSpark.tsx` 导出了组件，再导出这个可变对象会让
 * `react-refresh/only-export-components` 报「非组件导出」——HMR 会退化成整页刷新。
 * 把桥抽出来，两边都只是导入一个普通模块，组件文件保持「只导出组件」。
 *
 * 【为什么不用 Context】为这一个回调套一层 Provider，会让整张表（1000+ 行）跟着重渲染；
 * 而它只是个「当前悬浮层的处理器」引用，模块级单例是最轻的通道。
 */
export const healthHoverBridgeRef: {
  current: (el: HTMLElement | null, metrics?: HealthMetrics, running?: boolean) => void
} = {
  current: () => {},
}
