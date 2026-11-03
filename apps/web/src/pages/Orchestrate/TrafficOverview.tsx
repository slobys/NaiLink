import type { CSSProperties, ReactNode } from 'react'
import type { ChartConfig } from '~/components/ui/chart'
import dayjs from 'dayjs'
import { ArrowDown, ArrowUp, ChevronDown, Download, Link2, Radio, Upload } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import { useTrafficOverviewQuery } from '~/apis'
import { Button } from '~/components/ui/button'
import { Card, CardContent } from '~/components/ui/card'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '~/components/ui/chart'
import { useRouterMaintenanceQuery } from '~/hooks/useRouterMaintenance'
import { cn } from '~/lib/utils'
import { coreTransitionActive } from '~/utils/router-maintenance'
import { routerMode } from '~/utils/router-mode'

type TimeRangeKey = '1m' | '10m' | '30m' | '1h'

interface TimeRangeOption {
  key: TimeRangeKey
  seconds: number
  maxPoints: number
  label: string
}

const TIME_RANGE_OPTIONS: TimeRangeOption[] = [
  { key: '1m', seconds: 60, maxPoints: 120, label: '1m' },
  { key: '10m', seconds: 10 * 60, maxPoints: 240, label: '10m' },
  { key: '30m', seconds: 30 * 60, maxPoints: 360, label: '30m' },
  { key: '1h', seconds: 60 * 60, maxPoints: 480, label: '1h' },
]

function formatBytes(value: number) {
  if (value < 1024) return `${value.toFixed(0)} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)} KB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(value < 10 * 1024 ** 2 ? 1 : 0)} MB`
  return `${(value / 1024 ** 3).toFixed(1)} GB`
}

function formatRate(value: number) {
  return `${formatBytes(value)}/s`
}

function formatAxisRate(value: number) {
  if (value < 1024) return `${value.toFixed(0)}B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)}K`
  return `${(value / 1024 ** 2).toFixed(value < 10 * 1024 ** 2 ? 1 : 0)}M`
}

function splitFormattedRate(value: number) {
  const formatted = formatRate(value)
  const spaceIndex = formatted.indexOf(' ')

  if (spaceIndex === -1) {
    return { amount: formatted, unit: '' }
  }

  return {
    amount: formatted.slice(0, spaceIndex),
    unit: formatted.slice(spaceIndex + 1),
  }
}

function splitFormattedBytes(value: number) {
  const formatted = formatBytes(value)
  const spaceIndex = formatted.indexOf(' ')

  if (spaceIndex === -1) {
    return { amount: formatted, unit: '' }
  }

  return {
    amount: formatted.slice(0, spaceIndex),
    unit: formatted.slice(spaceIndex + 1),
  }
}

function safeParseMetricTotal(value?: string) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function createTintStyle(colorVar: string): CSSProperties {
  return {
    backgroundColor: `color-mix(in oklab, ${colorVar} 8%, var(--card))`,
    borderColor: `color-mix(in oklab, ${colorVar} 16%, var(--border))`,
  }
}

function createIconTintStyle(colorVar: string): CSSProperties {
  return {
    color: colorVar,
    backgroundColor: `color-mix(in oklab, ${colorVar} 14%, transparent)`,
  }
}

function TrafficMetricCard({
  title,
  amount,
  unit,
  icon,
  colorVar,
  compact = false,
}: {
  title: string
  amount: string
  unit: string
  icon: ReactNode
  colorVar: string
  compact?: boolean
}) {
  if (compact)
    return (
      <div className="rounded-xl border px-3 py-2.5" style={createTintStyle(colorVar)}>
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <span className="shrink-0 [&>svg]:h-3.5 [&>svg]:w-3.5" style={{ color: colorVar }}>
            {icon}
          </span>
          <span>{title}</span>
        </div>
        <div className="mt-1.5 flex flex-wrap items-baseline gap-x-1 gap-y-0.5">
          <span className="text-xl font-semibold tabular-nums leading-none tracking-tight">{amount}</span>
          <span className="text-[11px] text-muted-foreground">{unit}</span>
        </div>
      </div>
    )
  return (
    <div
      className="h-full rounded-xl border bg-card px-4 py-3 shadow-sm transition-colors"
      style={createTintStyle(colorVar)}
    >
      <div className="flex items-center gap-3">
        <div
          className="flex h-8 w-8 items-center justify-center rounded-lg transition-colors"
          style={createIconTintStyle(colorVar)}
        >
          {icon}
        </div>
        <div className="min-w-0">
          <p className="text-sm font-medium text-muted-foreground">{title}</p>
          <div className="mt-0.5 flex items-baseline gap-1.5">
            <span className="text-[1.4rem] font-semibold tabular-nums leading-none tracking-tight text-foreground">
              {amount}
            </span>
            <span className="text-sm text-muted-foreground">{unit}</span>
          </div>
        </div>
      </div>
    </div>
  )
}

function TrafficRangeButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return (
    <Button
      variant="outline"
      size="xs"
      className={cn(
        'rounded-full px-3 transition-colors',
        active && 'border-primary/40 bg-primary/10 text-primary hover:bg-primary/15',
      )}
      onClick={onClick}
    >
      {children}
    </Button>
  )
}

export function TrafficOverview({ compact = false }: { compact?: boolean }) {
  const { t } = useTranslation()
  const [selectedRange, setSelectedRange] = useState<TimeRangeKey>('1m')
  const [showTrends, setTrendsOpen] = useState(false)
  const maintenance = useRouterMaintenanceQuery(routerMode)
  const switchingCore = coreTransitionActive(maintenance.data)
  const trendsOpen = compact || showTrends

  const chartConfig = useMemo(
    () =>
      ({
        upload: { label: t('trafficOverview.uploadLegend'), color: 'var(--chart-1)' },
        download: { label: t('trafficOverview.downloadLegend'), color: 'var(--chart-2)' },
      }) satisfies ChartConfig,
    [t],
  )

  const selectedWindow = useMemo(
    () => TIME_RANGE_OPTIONS.find((option) => option.key === selectedRange) ?? TIME_RANGE_OPTIONS[0],
    [selectedRange],
  )

  const trafficOverviewQuery = useTrafficOverviewQuery(
    selectedWindow.seconds,
    trendsOpen ? selectedWindow.maxPoints : 2,
    !switchingCore,
  )
  const runtimeOverview = trafficOverviewQuery.data
  const chartWindowEnd = useMemo(
    () => (runtimeOverview?.updatedAt ? dayjs(runtimeOverview.updatedAt).valueOf() : Date.now()),
    [runtimeOverview?.updatedAt],
  )
  const chartWindowStart = useMemo(
    () => chartWindowEnd - selectedWindow.seconds * 1000,
    [chartWindowEnd, selectedWindow.seconds],
  )

  const latestSample = useMemo(
    () => ({
      uploadRate: runtimeOverview?.uploadRate ?? 0,
      downloadRate: runtimeOverview?.downloadRate ?? 0,
      uploadTotal: safeParseMetricTotal(runtimeOverview?.uploadTotal),
      downloadTotal: safeParseMetricTotal(runtimeOverview?.downloadTotal),
      activeConnections: runtimeOverview?.activeConnections ?? 0,
      udpSessions: runtimeOverview?.udpSessions ?? 0,
    }),
    [runtimeOverview],
  )

  const trafficChartData = useMemo(
    () =>
      (runtimeOverview?.samples ?? []).map((sample) => ({
        timestamp: dayjs(sample.timestamp).valueOf(),
        upload: sample.uploadRate,
        download: sample.downloadRate,
      })),
    [runtimeOverview?.samples],
  )

  const uploadRateDisplay = splitFormattedRate(latestSample.uploadRate)
  const downloadRateDisplay = splitFormattedRate(latestSample.downloadRate)
  const uploadTotalDisplay = splitFormattedBytes(latestSample.uploadTotal)
  const downloadTotalDisplay = splitFormattedBytes(latestSample.downloadTotal)

  const sessionTraffic = splitFormattedBytes(latestSample.uploadTotal + latestSample.downloadTotal)
  const available = !!runtimeOverview
  return (
    <div className={cn('traffic-overview', compact ? 'traffic-overview-compact' : 'space-y-4')}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">{t('panel.monitor')}</h2>
        {compact ? (
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span
              aria-hidden="true"
              className={cn(
                'h-1.5 w-1.5 rounded-full',
                available && !trafficOverviewQuery.isError && !switchingCore ? 'bg-emerald-500' : 'bg-muted-foreground',
              )}
            />
            {t(
              switchingCore
                ? 'panel.coreSwitching'
                : trafficOverviewQuery.isError
                  ? 'panel.statsUnavailable'
                  : available
                    ? 'panel.live'
                    : 'panel.loading',
            )}
          </span>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            aria-expanded={trendsOpen}
            aria-controls="panel-traffic-trends"
            onClick={() => setTrendsOpen((value) => !value)}
          >
            {trendsOpen ? t('panel.hideTrends') : t('panel.trends')}
            <ChevronDown aria-hidden="true" className={cn('h-4 w-4', trendsOpen && 'rotate-180')} />
          </Button>
        )}
      </div>
      {switchingCore && (
        <p role="status" className="text-xs text-muted-foreground">
          {t('panel.coreSwitching')}
        </p>
      )}
      {!switchingCore && trafficOverviewQuery.isError && (
        <div
          role="status"
          className="flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm text-muted-foreground"
        >
          <span>{t('panel.statsError')}</span>
          <Button variant="outline" size="sm" onClick={() => trafficOverviewQuery.refetch()}>
            {t('panel.retry')}
          </Button>
        </div>
      )}
      <div className={cn('traffic-summary-grid grid grid-cols-2 gap-2', !compact && 'xl:grid-cols-4')}>
        <TrafficMetricCard
          compact={compact}
          title={t('trafficOverview.uploadSpeed')}
          amount={available ? uploadRateDisplay.amount : '—'}
          unit={available ? uploadRateDisplay.unit : ''}
          icon={<ArrowUp aria-hidden="true" className="h-4 w-4" />}
          colorVar="var(--chart-1)"
        />
        <TrafficMetricCard
          compact={compact}
          title={t('trafficOverview.downloadSpeed')}
          amount={available ? downloadRateDisplay.amount : '—'}
          unit={available ? downloadRateDisplay.unit : ''}
          icon={<ArrowDown aria-hidden="true" className="h-4 w-4" />}
          colorVar="var(--chart-2)"
        />
        <TrafficMetricCard
          compact={compact}
          title={t('trafficOverview.activeConnections')}
          amount={available ? latestSample.activeConnections.toLocaleString() : '—'}
          unit={t('trafficOverview.connectionsUnit')}
          icon={<Link2 aria-hidden="true" className="h-4 w-4" />}
          colorVar="var(--primary)"
        />
        <TrafficMetricCard
          compact={compact}
          title={t(compact ? 'panel.totalTrafficShort' : 'panel.totalTraffic')}
          amount={available ? sessionTraffic.amount : '—'}
          unit={available ? sessionTraffic.unit : ''}
          icon={<Download aria-hidden="true" className="h-4 w-4" />}
          colorVar="var(--chart-3)"
        />
      </div>
      {runtimeOverview?.updatedAt && (
        <p className="text-xs text-muted-foreground">
          {t('panel.freshness', { time: new Date(runtimeOverview.updatedAt).toLocaleTimeString() })}
        </p>
      )}
      {trendsOpen && (
        <Card id="panel-traffic-trends" padding="none" className="overflow-hidden rounded-2xl">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2.5">
            <h3 className="text-sm font-medium">{t('panel.trafficTrend')}</h3>
            <div className="flex gap-1" role="group" aria-label={t('panel.timeRange')}>
              {TIME_RANGE_OPTIONS.map((option) => (
                <TrafficRangeButton
                  key={option.key}
                  active={selectedRange === option.key}
                  onClick={() => setSelectedRange(option.key)}
                >
                  {t(`trafficOverview.ranges.${option.key}`)}
                </TrafficRangeButton>
              ))}
            </div>
          </div>
          <CardContent className="traffic-trend-body space-y-3 p-3">
            <div className="flex items-center gap-4 text-xs text-muted-foreground">
              {(['upload', 'download'] as const).map((key, index) => (
                <span key={key} className="flex items-center gap-1.5">
                  <span
                    aria-hidden="true"
                    className="h-2 w-2 rounded-full"
                    style={{ backgroundColor: `var(--chart-${index + 1})` }}
                  />
                  {t(`trafficOverview.${key}Legend`)}
                </span>
              ))}
              {!runtimeOverview?.samples.length && (
                <span>{trafficOverviewQuery.isPending ? t('panel.loading') : t('panel.emptyStats')}</span>
              )}
            </div>
            <ChartContainer config={chartConfig} className="traffic-chart aspect-auto w-full">
              <AreaChart data={trafficChartData} margin={{ left: 0, right: 8, top: 6, bottom: 0 }}>
                <defs>
                  {(['upload', 'download'] as const).map((key) => (
                    <linearGradient key={key} id={`traffic-${key}-fill`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={`var(--color-${key})`} stopOpacity={0.2} />
                      <stop offset="100%" stopColor={`var(--color-${key})`} stopOpacity={0} />
                    </linearGradient>
                  ))}
                </defs>
                <CartesianGrid vertical={false} strokeDasharray="3 3" />
                <XAxis
                  type="number"
                  scale="time"
                  dataKey="timestamp"
                  axisLine={false}
                  tickLine={false}
                  minTickGap={24}
                  tickMargin={10}
                  height={30}
                  domain={[chartWindowStart, chartWindowEnd]}
                  allowDataOverflow
                  tickFormatter={(value) => dayjs(value).format('HH:mm')}
                />
                <YAxis
                  axisLine={false}
                  tickLine={false}
                  tickMargin={6}
                  width={48}
                  tickFormatter={(value) => formatAxisRate(Number(value))}
                  domain={[0, (dataMax: number) => Math.max(32 * 1024, dataMax * 1.15)]}
                />
                <ChartTooltip
                  content={
                    <ChartTooltipContent
                      labelFormatter={(value) => dayjs(Number(value)).format('HH:mm:ss')}
                      formatter={(value) => formatRate(Number(value))}
                      indicator="line"
                    />
                  }
                />
                {(['upload', 'download'] as const).map((key) => (
                  <Area
                    key={key}
                    dataKey={key}
                    type="monotone"
                    stroke={`var(--color-${key})`}
                    strokeWidth={2}
                    fill={`url(#traffic-${key}-fill)`}
                    isAnimationActive={false}
                  />
                ))}
              </AreaChart>
            </ChartContainer>
            <dl className="grid grid-cols-3 gap-2 border-t pt-3">
              {[
                [
                  t('trafficOverview.totalUpload'),
                  available ? uploadTotalDisplay.amount : '—',
                  available ? uploadTotalDisplay.unit : '',
                  <Upload key="upload" className="h-3.5 w-3.5" />,
                ],
                [
                  t('trafficOverview.totalDownload'),
                  available ? downloadTotalDisplay.amount : '—',
                  available ? downloadTotalDisplay.unit : '',
                  <Download key="download" className="h-3.5 w-3.5" />,
                ],
                [
                  t('trafficOverview.udpSessions'),
                  available ? latestSample.udpSessions.toLocaleString() : '—',
                  t('trafficOverview.sessionsUnit'),
                  <Radio key="udp" className="h-3.5 w-3.5" />,
                ],
              ].map(([label, amount, unit, icon]) => (
                <div key={String(label)} className="min-w-0">
                  <dt className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
                    {icon}
                    {label}
                  </dt>
                  <dd className="mt-1 flex flex-wrap items-baseline gap-1 text-sm font-semibold tabular-nums">
                    {amount}
                    <span className="text-[10px] font-normal text-muted-foreground">{unit}</span>
                  </dd>
                </div>
              ))}
            </dl>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
