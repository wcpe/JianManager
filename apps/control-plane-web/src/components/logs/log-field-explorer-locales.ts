import i18n from '@/i18n'

import { LOG_FIELD_EXPLORER_LOCALES } from './log-field-explorer-locale-data'

// A local namespace keeps the self-contained explorer independent of page/global locale edits.
export { LOG_FIELD_EXPLORER_LOCALES } from './log-field-explorer-locale-data'
export const LOG_FIELD_EXPLORER_NS = 'logFieldExplorer'

for (const [language, resource] of Object.entries(LOG_FIELD_EXPLORER_LOCALES)) {
  i18n.addResourceBundle(language, LOG_FIELD_EXPLORER_NS, resource, true, false)
}
