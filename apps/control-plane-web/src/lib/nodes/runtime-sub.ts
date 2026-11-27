/**
 * 节点「运行时」页签的二级位置（`?tab=runtime&sub=…`）。
 *
 * 为什么单独一个模块：它被两处使用——页面（解析/写 URL）与工作台（渲染 Segments）。
 * 放在组件文件里会迫使组件文件导出非组件值，破坏 Fast Refresh 的
 * "only-export-components" 约束（仓库有该 lint 规则）。
 */

/** 二级 sub。缺省 = inventory（运行时资产）。 */
export type RuntimeSub = 'inventory' | 'packages' | 'log' | 'logAcq'

/** 全部合法 sub（顺序即 Segments 的渲染顺序）。 */
export const RUNTIME_SUBS: RuntimeSub[] = ['inventory', 'packages', 'log', 'logAcq']

/**
 * 从 URL 解析 sub；非法值回退默认。
 *
 * 【为什么容忍非法值而不是报错】`?sub=` 是不影响数据正确性的展示位置，
 * 一个手改坏的链接应该落到默认段而不是白屏——与 `?tab=` 的处理口径一致。
 */
export function readRuntimeSub(raw: string | null): RuntimeSub {
  return RUNTIME_SUBS.includes(raw as RuntimeSub) ? (raw as RuntimeSub) : 'inventory'
}
