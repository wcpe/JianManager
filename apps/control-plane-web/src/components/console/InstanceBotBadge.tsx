// 实现已回迁应用侧（原 ADR-097 迁包已撤销），此处保留同名默认导出维持既有导入路径。
// 包内命名为 InstanceBotChip（避免与 `lib/bot-list` 的 InstanceBotBadge 类型同名）。
export { InstanceBotChip as default } from '@/components/views/console/ConsoleLeafParts'
