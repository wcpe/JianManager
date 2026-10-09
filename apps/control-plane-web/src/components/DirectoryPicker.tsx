import { useState } from 'react'
import { useBrowseDir } from '@/api/nodeRuntime'
import DirectoryPickerView from '@/components/views/nodes/DirectoryPicker'

/** 节点目录选择器的公开 props（与原实现一致，调用点无需改动）。 */
interface DirectoryPickerProps {
  /** 节点 ID（经 CP 委托 Worker 浏览）。 */
  nodeId: number
  /** 选定目录回调（传回绝对路径）。 */
  onPick: (path: string) => void
  /** 取消/关闭回调。 */
  onCancel: () => void
  /** 初始浏览路径（空=起点：盘符/根）。 */
  initialPath?: string
}

/**
 * 节点目录选择器的应用接线层（ADR-097 a 范式）。
 *
 * 选择器本体已迁入组件库并受控（不取数）；本层持有「当前路径」——它是查询键的一部分，
 * 换路径即换查询，因此不能留在视图内部——并把浏览结果注入视图。
 * 保留同名同签名的默认导出，使既有调用点（JDK 登记、导入向导）无需改动。
 */
export default function DirectoryPicker({ nodeId, onPick, onCancel, initialPath = '' }: DirectoryPickerProps) {
  const [path, setPath] = useState(initialPath)
  const { data, isLoading, isError, error, refetch, isFetching } = useBrowseDir(nodeId, path)

  const errMsg = (error as { response?: { data?: { message?: string } } })?.response?.data?.message

  return (
    <DirectoryPickerView
      path={path}
      data={data}
      isLoading={isLoading}
      isError={isError}
      errorMessage={errMsg}
      isFetching={isFetching}
      onNavigate={setPath}
      onRefresh={() => void refetch()}
      onPick={onPick}
      onCancel={onCancel}
    />
  )
}
