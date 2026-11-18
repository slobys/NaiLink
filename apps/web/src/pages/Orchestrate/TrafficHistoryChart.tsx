import dayjs from 'dayjs'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '~/components/ui/chart'

function formatAxisRate(value: number) {
  if (value < 1024) return `${value.toFixed(0)}B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)}K`
  return `${(value / 1024 ** 2).toFixed(value < 10 * 1024 ** 2 ? 1 : 0)}M`
}

interface TrafficHistoryChartProps {
  data: { timestamp: number; upload: number; download: number }[]
  windowStart: number
  windowEnd: number
  formatRate: (value: number) => string
}

const directions = ['upload', 'download'] as const

export default function TrafficHistoryChart({ data, windowStart, windowEnd, formatRate }: TrafficHistoryChartProps) {
  const { t } = useTranslation()
  const chartId = useId()
  const config = {
    upload: { label: t('trafficOverview.uploadLegend'), color: 'var(--chart-1)' },
    download: { label: t('trafficOverview.downloadLegend'), color: 'var(--chart-2)' },
  }
  return (
    <ChartContainer config={config} className="traffic-chart aspect-auto w-full">
      <AreaChart data={data} margin={{ left: 0, right: 8, top: 6, bottom: 0 }}>
        <defs>
          {directions.map((key) => (
            <linearGradient key={key} id={`${chartId}-${key}-fill`} x1="0" y1="0" x2="0" y2="1">
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
          domain={[windowStart, windowEnd]}
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
        {directions.map((key) => (
          <Area
            key={key}
            dataKey={key}
            type="monotone"
            stroke={`var(--color-${key})`}
            strokeWidth={2}
            fill={`url(#${chartId}-${key}-fill)`}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}
