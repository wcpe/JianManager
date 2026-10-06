/**
 * 实例标签的协议常量（FR-047）。
 *
 * 环境维度复用 Tags 字段、以 `env:` 前缀写入——该前缀与后端 `model.EnvTagPrefix` 一致，
 * 是**跨端协议**而非展示细节，故随视图归包、由应用侧再导出，避免两处各写一份字面量。
 */
export const ENV_TAG_PREFIX = 'env:'
