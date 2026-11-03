export const connectivityTargets = [
  { id: 'aliyun', name: '阿里云' },
  { id: 'google', name: 'Google' },
  { id: 'openai', name: 'OpenAI' },
  { id: 'github', name: 'GitHub' },
] as const
export interface ConnectivitySample {
  at: number
  status: 'ok' | 'restricted' | 'error'
  latencyMs?: number
  httpStatus?: number
  code?: string
}
export interface ConnectivityStatus {
  core: string
  state: 'ready' | 'stopped' | 'changing' | 'unavailable'
  checking: boolean
  checkedAt: number
  targets: { id: string; samples: ConnectivitySample[] }[]
}

export function parseConnectivity(value: string): ConnectivityStatus {
  const data = JSON.parse(value) as ConnectivityStatus
  if (
    !data ||
    !['ready', 'stopped', 'changing', 'unavailable'].includes(data.state) ||
    typeof data.core !== 'string' ||
    typeof data.checking !== 'boolean' ||
    !Number.isFinite(data.checkedAt) ||
    !Array.isArray(data.targets) ||
    data.targets.length !== 4
  )
    throw new Error('连接检测数据暂不可用')
  const seen = new Set<string>()
  for (const target of data.targets) {
    if (
      !target ||
      !connectivityTargets.some((t) => t.id === target.id) ||
      seen.has(target.id) ||
      !Array.isArray(target.samples) ||
      target.samples.length > 18
    )
      throw new Error('连接检测数据暂不可用')
    seen.add(target.id)
    for (const sample of target.samples) {
      if (
        !sample ||
        !Number.isFinite(sample.at) ||
        !['ok', 'restricted', 'error'].includes(sample.status) ||
        (sample.latencyMs !== undefined && (!Number.isFinite(sample.latencyMs) || sample.latencyMs < 0)) ||
        (sample.status !== 'error' &&
          (sample.latencyMs === undefined ||
            !Number.isInteger(sample.httpStatus) ||
            sample.httpStatus! < 100 ||
            sample.httpStatus! > 599)) ||
        (sample.code !== undefined && typeof sample.code !== 'string')
      )
        throw new Error('连接检测数据暂不可用')
    }
  }
  return data
}

export function connectivityLabel(sample?: ConnectivitySample) {
  if (!sample) return '未检测'
  if (sample.status === 'ok') return '连接正常'
  if (sample.status === 'restricted')
    return sample.httpStatus && sample.httpStatus >= 500
      ? `服务异常 HTTP ${sample.httpStatus}`
      : `访问受限 HTTP ${sample.httpStatus || '—'}`
  return (
    (
      { timeout: '连接超时', dns_failed: '解析失败', tls_failed: '证书错误', connection_failed: '连接失败' } as Record<
        string,
        string
      >
    )[sample.code || ''] || '连接失败'
  )
}
export function connectivityTone(sample?: ConnectivitySample) {
  if (!sample) return 'muted'
  if (sample.status === 'error') return 'error'
  if (sample.status === 'restricted' || (sample.latencyMs || 0) >= 600) return 'warning'
  return 'ok'
}
export const connectivityAutoKey = 'nailink.connectivity.auto'
