/**
 * 表单字段标签 + 必填标记 + 内联错误（FR-072）。
 *
 * 统一创建/编辑模态框的「必填(*)/选填明确 + 错误内联提示」。必填项在标签后渲染
 * 红色 `*`（带无障碍文案）；可选 {@link FieldError} 在字段下方以 destructive 文字提示。
 */
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

interface FieldLabelProps extends React.ComponentProps<'label'> {
  /** 是否必填：为真时在标签后追加红色 `*`。 */
  required?: boolean
}

/** 字段标签。required 时追加红色星号与屏幕阅读器文案。 */
export function FieldLabel({ required, className, children, ...props }: FieldLabelProps) {
  const { t } = useTranslation()
  return (
    <label
      data-slot="field-label"
      className={cn('text-sm font-medium', className)}
      {...props}
    >
      {children}
      {required && (
        <span className="ml-0.5 text-destructive" aria-hidden="true">
          *
        </span>
      )}
      {required && <span className="sr-only"> ({t('validation.requiredMark')})</span>}
    </label>
  )
}

interface FieldErrorProps {
  /** 错误的 i18n key（validation 命名空间）；空则不渲染。 */
  error?: string
  /** 可选 i18n 插值参数（如 minLength 的 {{min}}）。 */
  values?: Record<string, unknown>
}

/** 字段内联错误文字。传入 i18n key（可带插值参数），翻译后以 destructive 小字展示。 */
export function FieldError({ error, values }: FieldErrorProps) {
  const { t } = useTranslation()
  if (!error) return null
  return <p className="mt-1 text-xs text-destructive">{t(error, values)}</p>
}
