import { Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

export function NodeActiveMark({ groups }: { groups?: string[] }) {
  const { t } = useTranslation()
  if (!groups?.length) return null
  const label = `${t('panel.currentOutlet')}: ${groups.join(' / ')}`
  return (
    <span className="node-active-mark" role="img" aria-label={label} title={label}>
      <Check aria-hidden="true" />
    </span>
  )
}
