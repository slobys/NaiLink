import { useIsMutating, useQueryClient } from '@tanstack/react-query'
import { Activity, Power } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'
import { toast } from 'sonner'
import { useGeneralQuery, useRunMutation } from '~/apis'
import { CommunityLinks } from '~/components/CommunityLinks'
import { RouterCore } from '~/components/RouterCore'
import { RouterGeodata, RouterVersion } from '~/components/RouterMaintenance'
import { ThemePicker } from '~/components/ThemePicker'
import { Button } from '~/components/ui/button'
import { QUERY_KEY_GENERAL } from '~/constants'
import { useRouterMaintenance } from '~/hooks/useRouterMaintenance'
import { configurationJob, ConfigurationPendingError } from '~/utils/configuration-apply'
import { jobActive, maintenanceMessages } from '~/utils/router-maintenance'

export function RouterHeader() {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const title =
    pathname === '/logs'
      ? '运行日志'
      : pathname === '/resources'
        ? t('panel.resources')
        : pathname === '/advanced'
          ? t('panel.advanced')
          : t('panel.dashboard')
  const query = useGeneralQuery()
  const run = useRunMutation()
  const mutations = useIsMutating({
    predicate: (mutation) =>
      !['node-latency-test', 'geodata-version-check', 'connectivity-check'].includes(
        String(mutation.options.mutationKey?.[0]),
      ),
  })
  const { busy: maintenanceBusy, query: maintenance } = useRouterMaintenance()
  const client = useQueryClient()
  const job = configurationJob(maintenance.data?.job) ? maintenance.data?.job : undefined
  const configuring = jobActive(job)
  const [failed, setFailed] = useState<Error>()
  // A reopened page did not own the original mutation. Its durable task receipt
  // must still refresh general/running-group state when the worker finishes.
  useEffect(() => {
    if (job && !jobActive(job)) {
      client.invalidateQueries({ queryKey: QUERY_KEY_GENERAL })
      client.invalidateQueries({ queryKey: ['core-runtime-groups'] })
      if (failed instanceof ConfigurationPendingError && failed.operation === job.id) {
        setFailed(undefined)
        if (job.state === 'success')
          toast.success(job.action === 'config_stop' ? t('panel.stopped') : t('panel.applied'))
      }
    }
  }, [client, job?.id, job?.state, failed, t])
  const saving = mutations > 0 || maintenanceBusy
  const [operation, setOperation] = useState<'apply' | 'toggle'>('toggle')
  const failure =
    failed?.message ||
    (job?.state === 'error' ? maintenanceMessages[job.code || ''] || t('panel.runFailed') : undefined)
  const running = query.data?.general.dae.running ?? false
  const modified = query.data?.general.dae.modified ?? false
  const invoke = async (action: 'apply' | 'toggle') => {
    if (saving) return
    setOperation(action)
    setFailed(undefined)
    try {
      await run.mutateAsync(action === 'apply' ? false : running)
      toast.success(action === 'apply' ? t('panel.applied') : running ? t('panel.stopped') : t('panel.running'))
    } catch (error) {
      setFailed(error instanceof Error ? error : new Error(t('panel.runFailed')))
    }
  }
  return (
    <header className="sticky top-0 z-40 border-b bg-background/95 backdrop-blur-sm">
      <div className="router-toolbar mx-auto grid max-w-[1400px] items-center gap-3 px-4 py-3 lg:px-8">
        <div className="flex min-w-0 items-center gap-2.5">
          <Activity className="h-5 w-5 text-primary" aria-hidden="true" />
          <h1 className="text-base font-semibold tracking-tight">{title}</h1>
        </div>
        <CommunityLinks />
        <div className="router-toolbar-actions flex flex-wrap items-center gap-2">
          <RouterVersion />
          <RouterGeodata />
          <RouterCore />
          <ThemePicker variant="compact" />
          <span
            role="status"
            className={`inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-medium ${running ? 'bg-emerald-500/10 text-emerald-800 dark:text-emerald-300' : 'bg-muted text-muted-foreground'}`}
          >
            <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
            {query.isPending || query.isError ? t('panel.checking') : running ? t('panel.running') : t('panel.stopped')}
          </span>
          {(modified || (saving && !run.isPending)) && (
            <span role="status" className="text-xs text-muted-foreground">
              {saving && !run.isPending ? t('panel.saving') : t('panel.unapplied')}
            </span>
          )}
          {modified && (
            <Button
              size="sm"
              disabled={saving || query.isError}
              aria-busy={run.isPending && operation === 'apply'}
              onClick={() => invoke('apply')}
            >
              {run.isPending && operation === 'apply' ? t('panel.applying') : t('panel.apply')}
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            disabled={saving || query.isPending || query.isError}
            aria-busy={run.isPending && operation === 'toggle'}
            onClick={() => invoke('toggle')}
          >
            <Power className="h-3.5 w-3.5" aria-hidden="true" />
            {run.isPending && operation === 'toggle'
              ? running
                ? t('panel.stopping')
                : t('panel.starting')
              : running
                ? t('panel.stop')
                : t('panel.start')}
          </Button>
        </div>
        {configuring && (
          <div role="status" className="col-span-full rounded-lg bg-primary/5 px-3 py-2 text-sm text-muted-foreground">
            {job?.action === 'config_stop' ? '正在停止代理' : '正在应用配置'}
            ，关闭页面后后台任务仍会继续，请勿重复提交。
          </div>
        )}
        {!configuring && !run.isPending && (failure || query.isError) && (
          <div
            role="alert"
            className="col-span-full flex w-full flex-wrap items-center gap-3 rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm"
          >
            <span>{failure || t('panel.loadFailed')}</span>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setFailed(undefined)
                query.refetch()
                maintenance.refetch()
              }}
              disabled={saving}
            >
              重新读取状态
            </Button>
          </div>
        )}
      </div>
    </header>
  )
}
