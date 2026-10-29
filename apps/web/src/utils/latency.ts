import type { TFunction } from 'i18next'
import type { NodeLatencyProbeResult } from '~/apis'

// The generic Docker backend retains the upstream schema. Only router builds
// request its additive source field; manual mutations still know their source.
export function latencyResultFields(includeSource: boolean) {
  return `id latencyMs alive testedAt message ${includeSource ? 'source steadyMs' : ''}`
}

export function hasMeasuredLatency(result?: NodeLatencyProbeResult) {
  return result?.alive === true && Number.isFinite(result.latencyMs) && result.latencyMs! >= 0
}

export function formatLatencyDescription(result: NodeLatencyProbeResult | undefined, t: TFunction) {
  if (!result) return undefined
  const source = t(
    result.source === 'automatic'
      ? 'latency.automaticDescription'
      : result.source === 'manual'
        ? 'latency.manualDescription'
        : 'latency.unknownDescription',
  )
  const time = result.testedAt ? new Date(result.testedAt).toLocaleString() : ''
  const measured =
    result.source === 'manual' && hasMeasuredLatency(result)
      ? t('latency.coldMeasurement', { ms: result.latencyMs })
      : ''
  const steady =
    result.source === 'manual' && result.steadyMs != null ? t('latency.steadyMeasurement', { ms: result.steadyMs }) : ''
  return [source, measured, steady, time].filter(Boolean).join(' · ')
}

export function latencyTone(result?: NodeLatencyProbeResult): 'good' | 'warning' | 'bad' | 'unmeasured' {
  if (!result) return 'unmeasured'
  if (result.alive === false) return 'bad'
  if (!hasMeasuredLatency(result)) return 'unmeasured'
  if (result.latencyMs! < 200) return 'good'
  if (result.latencyMs! < 500) return 'warning'
  return 'bad'
}

export function formatLatencyLabel(result: NodeLatencyProbeResult | undefined, t: TFunction) {
  if (!result) {
    return undefined
  }
  if (result.alive === false) {
    if (result.message === 'unsupported_protocol' || result.message === 'mihomo_protocol_unsupported')
      return t('latency.nodeFailure.protocol')
    if (result.message === 'invalid_ech_public_name') return t('latency.nodeFailure.ech')
    // Native adapter failures happen before a network request. Keep them
    // distinct from line timeouts without echoing arbitrary backend text.
    if (
      result.message &&
      result.message !== 'invalid_node' &&
      /^(?:unsupported_[a-z_]+|invalid_[a-z_]+|conflicting_node_options)$/.test(result.message)
    ) {
      return t('latency.nodeFailure.parameters')
    }
    const reasons = {
      extra_not_installed: 'extension',
      mihomo_option_unsupported: 'invalidNode',
      mihomo_configuration_invalid: 'invalidNode',
      mihomo_not_installed: 'extension',
      tor_bootstrap_failed: 'torBootstrap',
      invalid_node: 'invalidNode',
      dns_failed: 'dns',
      tls_failed: 'tls',
      connection_refused: 'refused',
      connection_closed: 'closed',
      timeout: 'timeout',
      node_tcp_timeout: 'nodeConnect',
      tls_handshake_timeout: 'tlsHandshake',
      websocket_failed: 'websocket',
      gateway_failed: 'gateway',
      cancelled: 'cancelled',
      authentication_failed: 'authentication',
      check_target_failed: 'target',
      udp_unsupported: 'udpUnsupported',
      core_ports_unavailable: 'ports',
    } as const
    const reason =
      result.message && Object.hasOwn(reasons, result.message)
        ? reasons[result.message as keyof typeof reasons]
        : undefined
    if (reason) return t(`latency.nodeFailure.${reason}`)
    return t('latency.failed')
  }
  if (hasMeasuredLatency(result)) {
    return `${result.latencyMs} ms`
  }
  return t('latency.unavailable')
}
