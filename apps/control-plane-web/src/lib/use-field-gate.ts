/**
 * 表单错误展示时机门控（验收矩阵 #3，补 FR-072 校验体系缺口）。
 *
 * FR-072 的 validateFields 每次渲染对空初值即产错，FieldError 无条件渲染，
 * 导致「弹窗一打开满屏必填红字」。本 hook 提供 touched/submitted 门控：
 * 字段错误仅在「该字段已失焦触碰」或「已尝试提交」后展示；提交按钮的
 * disabled 判定仍用真实 errors（不受门控影响），保证不可提交非法表单。
 */
import { useCallback, useState } from 'react'

export interface FieldGate {
  /** 标记字段已触碰（接到输入的 onBlur）。 */
  touch: (field: string) => void
  /** 标记已尝试提交（handleSubmit 首行调用），此后全部错误可见。 */
  submit: () => void
  /** 重置门控（弹窗复用挂载时随 open 重置）。 */
  reset: () => void
  /** 门控后的字段错误：未触碰且未提交时抑制为 undefined。 */
  show: (field: string, error: string | undefined) => string | undefined
}

export function useFieldGate(): FieldGate {
  const [touched, setTouched] = useState<Record<string, boolean>>({})
  const [submitted, setSubmitted] = useState(false)

  const touch = useCallback((field: string) => {
    setTouched((prev) => (prev[field] ? prev : { ...prev, [field]: true }))
  }, [])
  const submit = useCallback(() => setSubmitted(true), [])
  const reset = useCallback(() => {
    setTouched({})
    setSubmitted(false)
  }, [])
  const show = useCallback(
    (field: string, error: string | undefined) =>
      submitted || touched[field] ? error : undefined,
    [submitted, touched],
  )

  return { touch, submit, reset, show }
}
