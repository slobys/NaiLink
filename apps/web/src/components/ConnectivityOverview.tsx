import type { ConnectivitySample, ConnectivityStatus } from '~/utils/connectivity'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { PawPrint, RefreshCw, Sparkles } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useGeneralQuery } from '~/apis'
import { GithubIcon } from '~/components/GithubIcon'
import { Button } from '~/components/ui/button'
import { Switch } from '~/components/ui/switch'
import { useGQLQueryClient } from '~/contexts'
import { useRouterMaintenance } from '~/hooks/useRouterMaintenance'
import { cn } from '~/lib/utils'
import {
  connectivityAutoKey,
  connectivityLabel,
  connectivityTargets,
  connectivityTone,
  parseConnectivity,
} from '~/utils/connectivity'

const queryKey = ['connectivity-status']
const tones = {
  muted: { text: 'text-muted-foreground', bar: 'bg-muted-foreground/15' },
  ok: { text: 'text-emerald-600 dark:text-emerald-400', bar: 'bg-emerald-500/55' },
  warning: { text: 'text-amber-600 dark:text-amber-400', bar: 'bg-amber-500/60' },
  error: { text: 'text-red-600 dark:text-red-400', bar: 'bg-red-500/60' },
}
function Brand({ id }: { id: string }) {
  if (id === 'google') return <span className="text-xl font-bold text-blue-500">G</span>
  const Icon = id === 'baidu' ? PawPrint : id === 'github' ? GithubIcon : Sparkles
  return (
    <Icon
      className={cn(
        'h-4 w-4',
        id === 'baidu' ? 'text-blue-600' : id === 'openai' ? 'text-emerald-600' : 'text-foreground',
      )}
    />
  )
}
function Bar({ sample }: { sample?: ConnectivitySample }) {
  return (
    <span
      className={cn('min-w-0 flex-1 rounded-[1px]', tones[connectivityTone(sample)].bar)}
      style={{
        height: sample
          ? `${sample.status === 'error' ? 5 : Math.min(20, 4 + Math.log2(1 + (sample.latencyMs || 0) / 30) * 4)}px`
          : '4px',
      }}
      title={
        sample
          ? `${new Date(sample.at).toLocaleTimeString()} · ${connectivityLabel(sample)}${sample.latencyMs !== undefined ? ` · ${sample.latencyMs} ms` : ''}`
          : undefined
      }
    />
  )
}

export function ConnectivityOverview() {
  const client = useQueryClient()
  const gql = useGQLQueryClient()
  const { data: general } = useGeneralQuery()
  const { busy } = useRouterMaintenance()
  const [visible, setVisible] = useState(!document.hidden)
  const [automatic, setAutomatic] = useState(() => {
    try {
      return localStorage.getItem(connectivityAutoKey) !== '0'
    } catch {
      return true
    }
  })
  useEffect(() => {
    const update = () => setVisible(!document.hidden)
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])
  const running = general?.general.dae.running === true
  const read = async (refresh: boolean, force: boolean, signal?: AbortSignal) => {
    const data = await gql.request<{ connectivityStatus: string }, { refresh: boolean; force: boolean }>(
      `query ConnectivityStatus($refresh: Boolean!, $force: Boolean!) { connectivityStatus(refresh: $refresh, force: $force) }`,
      { refresh, force },
      signal,
    )
    return parseConnectivity(data.connectivityStatus)
  }
  const query = useQuery({
    queryKey: [...queryKey, automatic],
    queryFn: ({ signal }) => read(automatic && running && !busy, false, signal),
    enabled: visible,
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval:
      automatic && running && !busy && visible
        ? (q) => (!q.state.error && (q.state.data?.checking || !q.state.data?.checkedAt) ? 2000 : 30000)
        : false,
    refetchIntervalInBackground: false,
    placeholderData: (previous) => previous,
  })
  const manual = useMutation({
    mutationKey: ['connectivity-check'],
    mutationFn: () => read(true, true),
    retry: false,
    onSuccess: (status: ConnectivityStatus) => client.setQueryData([...queryKey, automatic], status),
  })
  // Include current running state: an early read during initial general-query
  // loading must not postpone the first real check by a complete interval.
  useEffect(() => {
    if (running && automatic && visible && !busy) query.refetch()
  }, [running, automatic, visible, busy])
  const checking = query.isFetching || manual.isPending || query.data?.checking
  const error = query.isError || (manual.isError && manual.submittedAt > query.dataUpdatedAt)
  const stateLabel = !general
    ? '正在读取状态'
    : !running
      ? '代理已停止'
      : busy
        ? '配置处理中'
        : query.data?.state === 'changing'
          ? '内核切换中'
          : '路由器视角'
  const enabled = running && !busy && query.data?.state !== 'changing'
  return (
    <section className="col-span-full min-w-0" aria-label="连接检测" data-connectivity>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2 px-0.5">
        <div className="flex items-center gap-2 text-sm">
          <h2 className="font-semibold">连接检测</h2>
          <span
            className="text-[11px] text-muted-foreground"
            title="从路由器按当前已应用的分流规则检测 HTTP 连通耗时。不是带宽测试，不代表账号可用或地区解锁；按设备设置的分流可能与此不同。"
          >
            {stateLabel}
          </span>
          {!!query.data?.checkedAt && (
            <time
              className="hidden text-[11px] tabular-nums text-muted-foreground sm:inline"
              dateTime={new Date(query.data.checkedAt).toISOString()}
            >
              {new Date(query.data.checkedAt).toLocaleTimeString()}
            </time>
          )}
        </div>
        <div className="flex items-center gap-3">
          <label
            className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground"
            htmlFor="connectivity-auto"
          >
            自动检测
            <Switch
              id="connectivity-auto"
              size="xs"
              checked={automatic}
              onCheckedChange={(value) => {
                setAutomatic(value)
                try {
                  localStorage.setItem(connectivityAutoKey, value ? '1' : '0')
                } catch {
                  /* Storage may be disabled. */
                }
              }}
            />
          </label>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-2 text-xs"
            disabled={!enabled || !!checking}
            onClick={() => manual.mutate()}
            aria-busy={!!checking}
          >
            <RefreshCw className={cn('h-3.5 w-3.5', checking && 'motion-safe:animate-spin')} />
            {checking ? '检测中' : '立即检测'}
          </Button>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2 min-[900px]:grid-cols-4 min-[900px]:gap-3">
        {connectivityTargets.map((target) => {
          const samples = query.data?.targets.find((item) => item.id === target.id)?.samples || []
          const latest = samples.at(-1)
          const tone = tones[enabled ? connectivityTone(latest) : 'muted']
          const available = enabled && latest
          return (
            <article
              key={target.id}
              data-connectivity-target={target.id}
              className="flex min-w-0 items-center gap-2.5 rounded-xl border bg-card px-3 py-3 shadow-sm transition-colors hover:border-primary/35"
              aria-label={`${target.name}：${available ? connectivityLabel(latest) : stateLabel}${available && latest.latencyMs !== undefined ? `，${latest.latencyMs} 毫秒` : ''}`}
              title={available ? connectivityLabel(latest) : '每 30 秒自动检测；仅在面板可见时运行。'}
            >
              <span
                className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-muted/65"
                aria-hidden="true"
              >
                <Brand id={target.id} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="mb-1 flex items-center justify-between gap-2">
                  <h3 className="text-xs text-muted-foreground">{target.name}</h3>
                  <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', tone.bar)} aria-hidden="true" />
                </div>
                <div className="flex h-5 min-w-0 items-end gap-2">
                  <span className="flex h-5 min-w-0 flex-1 items-end gap-px overflow-hidden" aria-hidden="true">
                    {Array.from({ length: 18 }, (_, i) => (
                      <Bar key={i} sample={enabled ? samples[i - (18 - samples.length)] : undefined} />
                    ))}
                  </span>
                  <span
                    className={cn(
                      'shrink-0 whitespace-nowrap text-sm font-semibold leading-none tabular-nums',
                      tone.text,
                    )}
                  >
                    {available ? (
                      latest.status === 'ok' ? (
                        <>
                          {latest.latencyMs}
                          <span className="ml-0.5 text-[10px] font-normal">ms</span>
                        </>
                      ) : latest.status === 'restricted' ? (
                        <span className="text-xs">HTTP {latest.httpStatus}</span>
                      ) : (
                        <span className="text-xs">{connectivityLabel(latest).replace('连接', '')}</span>
                      )
                    ) : (
                      <span className="text-xs font-normal">{checking ? '检测中' : '未检测'}</span>
                    )}
                  </span>
                </div>
              </div>
            </article>
          )
        })}
      </div>
      {error && (
        <p role="status" className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          暂时无法读取检测结果，已保留上次数据。可稍后点击“立即检测”重试。
        </p>
      )}
    </section>
  )
}
