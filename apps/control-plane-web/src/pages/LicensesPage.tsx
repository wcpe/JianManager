// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做许可清单取数与页头「返回」跳转接线。
import { useNavigate } from 'react-router'
import { useLicenses } from '@/api/licenses'
import { LicensesPageView } from '@/components/views/licenses/LicensesPageView'

/**
 * 开源许可与依赖清单页（FR-135）容器：静态清单 `/licenses.json` 的取数与页头「返回」
 * （`navigate(-1)`）留在此处；列表、分区计数、包名搜索与行内展开交共享视图。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function LicensesPage() {
  const navigate = useNavigate()
  const { data, isLoading, isError } = useLicenses()

  return (
    <LicensesPageView
      dependencies={data?.dependencies}
      generatedAt={data?.generatedAt}
      isLoading={isLoading}
      isError={isError}
      onBack={() => navigate(-1)}
    />
  )
}
