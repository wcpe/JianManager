// 实现已回迁应用侧（原 ADR-097 迁包已撤销），此处保留同名默认导出维持既有导入路径。
// 视图现为受控：节点列表、当前选中与切换回调由外壳注入（原 useNodes + console store 上提）。
export { NodeSwitcher as default } from '@/components/views/console/ConsoleLeafParts'
