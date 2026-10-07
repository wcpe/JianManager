import { toast } from 'sonner'
import { ConfigFileEditor as ConfigFileEditorImpl } from '@jianmanager/ui'
import type { ConfigFileEditorProps as ConfigFileEditorPropsFull } from '@jianmanager/ui'
import { useConfigRead, useWriteConfig, useWriteConfigFields, useCrossCheck } from '@/api/configs'
import CodeEditor from '@/components/explorer/editor/CodeEditor'

/**
 * 对外 props（沿用原导出名）：接线层自己注入的字段与取数所需的实例 ID 之外，
 * 调用点（ConfigExplorer / GenericConfigSegment）只传原来那套 props，零改动。
 */
export type ConfigFileEditorProps = { instanceId: number } & Omit<
  ConfigFileEditorPropsFull,
  | 'readData'
  | 'isReadLoading'
  | 'readError'
  | 'onWrite'
  | 'onWriteFields'
  | 'onCrossCheck'
  | 'isSaving'
  | 'isChecking'
  | 'renderCodeEditor'
  | 'onNotify'
>

/**
 * 配置编辑器的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层注入：读取结果与加载/失败态、三种写入（原文 / 字段补丁 / 跨文件校验）、
 * 编辑器本体（CodeEditor 接线层，注入主题），以及把提示回执接回 toast。其余 props 原样透传。
 */
export default function ConfigFileEditor({ instanceId, path, ...rest }: ConfigFileEditorProps) {
  const readQ = useConfigRead(instanceId, path)
  const writeMut = useWriteConfig(instanceId)
  const writeFieldsMut = useWriteConfigFields(instanceId)
  const crossMut = useCrossCheck(instanceId)

  return (
    <ConfigFileEditorImpl
      path={path}
      {...rest}
      readData={readQ.data}
      isReadLoading={readQ.isLoading}
      readError={readQ.error ? (readQ.error as Error).message : undefined}
      onWrite={(args, opts) => writeMut.mutate(args, opts)}
      onWriteFields={(args, opts) => writeFieldsMut.mutate(args, opts)}
      onCrossCheck={(args) => crossMut.mutateAsync(args)}
      isSaving={writeMut.isPending || writeFieldsMut.isPending}
      isChecking={crossMut.isPending}
      renderCodeEditor={({ value, filename, gotoLine, gotoNonce, onChange, onSave }) => (
        <CodeEditor value={value} filename={filename} gotoLine={gotoLine} gotoNonce={gotoNonce} onChange={onChange} onSave={onSave} />
      )}
      onNotify={(kind, message) =>
        kind === 'success' ? toast.success(message) : kind === 'error' ? toast.error(message) : toast(message)
      }
    />
  )
}
