import { useTranslation } from 'react-i18next'
import { useTemplates } from '@/api/templates'

export default function TemplatesPage() {
  const { t } = useTranslation()
  const { data: templates, isLoading } = useTemplates()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">{t('templates.title')}</h1>
      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {templates?.map((tpl) => (
            <div key={tpl.id} className="border rounded-lg p-4 hover:shadow-md transition-shadow">
              <h3 className="font-medium text-lg mb-1">{tpl.name}</h3>
              <p className="text-xs text-muted-foreground mb-2">{tpl.type}</p>
              {tpl.description && <p className="text-sm text-muted-foreground mb-3">{tpl.description}</p>}
              <div className="text-xs font-mono bg-muted p-2 rounded overflow-hidden text-ellipsis">
                {tpl.startCommand}
              </div>
              {tpl.downloadUrl && (
                <a
                  href={tpl.downloadUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs text-primary hover:underline mt-2 inline-block"
                >
                  {t('templates.downloadLink')}
                </a>
              )}
            </div>
          ))}
          {(!templates || templates.length === 0) && (
            <p className="text-muted-foreground col-span-full text-center py-8">{t('templates.empty')}</p>
          )}
        </div>
      )}
    </div>
  )
}
