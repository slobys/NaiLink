import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useGQLQueryClient } from '~/contexts'
import { routerMode } from '~/utils/router-mode'
import { Button } from './ui/button'
import { Popover, PopoverContent, PopoverTrigger } from './ui/popover'

interface RuntimeGroups {
  core: string
  ready: boolean
  selectionAvailable: boolean
  groups: { name: string; nodes: string[]; selected?: string }[]
}

export function GroupRuntimeSummary({ name }: { name: string }) {
  const { t } = useTranslation()
  const gql = useGQLQueryClient()
  const [open, setOpen] = useState(false)
  const query = useQuery({
    queryKey: ['core-runtime-groups'],
    enabled: routerMode && open,
    staleTime: 5000,
    retry: false,
    queryFn: async () => {
      const result = await gql.request<{ coreRuntimeGroups: string }>('{coreRuntimeGroups}')
      return JSON.parse(result.coreRuntimeGroups) as RuntimeGroups
    },
  })
  if (!routerMode) return null
  const runtime = query.data?.groups.find((group) => group.name === name)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button size="sm" variant="ghost" className="h-7 px-1.5 text-xs">
          {t('panel.appliedOutlet')}
        </Button>
      </PopoverTrigger>
      <PopoverContent
        data-group-runtime={name}
        aria-label={`${t('panel.appliedOutlet')} ${name}`}
        className="max-h-64 w-80 max-w-[calc(100vw-2rem)] overflow-y-auto text-xs"
        align="start"
      >
        <div className="flex items-center justify-between gap-2" role="status">
          <span>
            {query.isPending
              ? t('panel.loadingOutlet')
              : query.isError
                ? t('panel.outletReadFailed')
                : !query.data?.ready
                  ? t('panel.outletNotReady')
                  : !runtime
                    ? t('panel.groupNotApplied')
                    : runtime.selected
                      ? `${query.data.core} · ${runtime.selected}`
                      : t('panel.appliedCandidates', { count: runtime.nodes.length })}
          </span>
          <Button
            size="icon"
            variant="ghost"
            className="size-8 shrink-0"
            disabled={query.isFetching}
            onClick={() => query.refetch()}
            aria-label={t('panel.refreshOutlet')}
          >
            <RefreshCw className="size-3.5" />
          </Button>
        </div>
        {runtime && <p className="mt-1 break-words leading-relaxed">{runtime.nodes.join(' / ')}</p>}
        {runtime && query.data?.core === 'dae' && (
          <p className="mt-1 leading-relaxed text-muted-foreground">{t('panel.daeOutletHint')}</p>
        )}
        {runtime && query.data?.core !== 'dae' && !query.data?.selectionAvailable && (
          <p className="mt-1 leading-relaxed text-muted-foreground">{t('panel.nativeOutletUnavailable')}</p>
        )}
      </PopoverContent>
    </Popover>
  )
}
