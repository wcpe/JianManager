// 实现已迁至 @jianmanager/ui（ADR-097），此处保留同名默认导出维持既有导入路径。
//
// 注意（既有决策，见 ConsoleHeader 注释）：本组件当前**无消费方**——顶栏已改用对象头承担
// 面包屑，故它保留在仓库里不删，等对象头接住它。迁包后仍保持这一保留状态：
// 包内 WorkbenchLeafParts.PageBreadcrumb 为受控版（pathname 与 renderLink 由外壳注入）。
export { PageBreadcrumb as default } from '@/components/views/console/WorkbenchLeafParts'
