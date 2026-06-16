import { useTemplates } from '@/api/templates'

export default function TemplatesPage() {
  const { data: templates, isLoading } = useTemplates()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">服务端模板</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {templates?.map((t) => (
            <div key={t.id} className="border rounded-lg p-4 hover:shadow-md transition-shadow">
              <h3 className="font-medium text-lg mb-1">{t.name}</h3>
              <p className="text-xs text-muted-foreground mb-2">{t.type}</p>
              {t.description && <p className="text-sm text-muted-foreground mb-3">{t.description}</p>}
              <div className="text-xs font-mono bg-muted p-2 rounded overflow-hidden text-ellipsis">
                {t.startCommand}
              </div>
              {t.downloadUrl && (
                <a
                  href={t.downloadUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs text-primary hover:underline mt-2 inline-block"
                >
                  下载链接
                </a>
              )}
            </div>
          ))}
          {(!templates || templates.length === 0) && (
            <p className="text-muted-foreground col-span-full text-center py-8">暂无模板</p>
          )}
        </div>
      )}
    </div>
  )
}
