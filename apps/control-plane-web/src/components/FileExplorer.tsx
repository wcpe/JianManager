// 实现已迁至 @jianmanager/ui（ADR-097），此处保留默认导出维持既有导入路径。
//
// 原接线层读 useDangerPermission('group') 计算门禁，但受控版 DangerConfirm 仅在调用点
// 显式声明 scope 时才参与门禁判定，而本组件从未声明 scope —— 即该删除一直没有门禁。
// 迁移保持既有行为不变（不引入新门禁），故本层退化为纯转发。
export { default } from '@jianmanager/ui/components/views/explorer/FileExplorer'
export type { FileExplorerProps } from '@jianmanager/ui/components/views/explorer/FileExplorer'
