import type { TFunction } from 'i18next'
import type { NodeLatencyProbeResult } from '~/apis'
import { buildSchema, parse, validate } from 'graphql'
import { describe, expect, it } from 'vitest'
import {
  formatLatencyDescription,
  formatLatencyLabel,
  hasMeasuredLatency,
  latencyResultFields,
  latencyTone,
} from './latency'

const measured = (latencyMs: number): NodeLatencyProbeResult => ({
  id: 'fixture',
  alive: true,
  latencyMs,
  testedAt: '',
})
describe('latency colors', () => {
  it.each([false, true])('queries the supported latency fields for router=%s', (router) => {
    const schema = buildSchema(`
      scalar Time
      type NodeLatency { id: ID! latencyMs: Int alive: Boolean! testedAt: Time! message: String ${router ? 'source: String! steadyMs: Int' : ''} }
      type Query { nodeLatencies: [NodeLatency!]! }
      type Mutation { testNodeLatencies(ids: [ID!]): [NodeLatency!]! }
    `)
    for (const query of [
      `query { nodeLatencies { ${latencyResultFields(router)} } }`,
      `mutation($ids: [ID!]) { testNodeLatencies(ids: $ids) { ${latencyResultFields(router)} } }`,
    ])
      expect(validate(schema, parse(query))).toEqual([])
    if (router) expect(latencyResultFields(router)).toContain('source')
  })
  it('distinguishes automatic and manual measurements without changing their numbers', () => {
    const translate = ((key: string) => key) as TFunction
    expect(formatLatencyDescription({ ...measured(180), source: 'manual' }, translate)).toBe(
      'latency.manualDescription · latency.coldMeasurement',
    )
    expect(formatLatencyDescription({ ...measured(30), source: 'automatic' }, translate)).toBe(
      'latency.automaticDescription',
    )
    expect(formatLatencyDescription(measured(180), translate)).toBe('latency.unknownDescription')
    expect(formatLatencyDescription(undefined, translate)).toBeUndefined()
    expect(formatLatencyLabel({ ...measured(180), source: 'manual' }, translate)).toBe('180 ms')
    expect(formatLatencyLabel({ ...measured(30), source: 'automatic' }, translate)).toBe('30 ms')
  })
  it('only renders known failure categories and never echoes arbitrary backend text', () => {
    const translate = ((key: string) => key) as TFunction
    expect(formatLatencyLabel({ ...measured(0), alive: false, message: 'tls_failed' }, translate)).toBe(
      'latency.nodeFailure.tls',
    )
    for (const [message, key] of [
      ['node_tcp_timeout', 'nodeConnect'],
      ['tls_handshake_timeout', 'tlsHandshake'],
      ['websocket_failed', 'websocket'],
      ['gateway_failed', 'gateway'],
      ['core_ports_unavailable', 'ports'],
    ]) {
      expect(formatLatencyLabel({ ...measured(0), alive: false, message }, translate)).toBe(
        `latency.nodeFailure.${key}`,
      )
    }
    expect(formatLatencyLabel({ ...measured(0), alive: false, message: 'private://password@host' }, translate)).toBe(
      'latency.failed',
    )
    for (const message of ['unsupported_node_option', 'unsupported_vmess_option', 'invalid_ech']) {
      expect(formatLatencyLabel({ ...measured(0), alive: false, message }, translate)).toBe(
        'latency.nodeFailure.parameters',
      )
    }
    expect(formatLatencyLabel({ ...measured(0), alive: false, message: 'invalid_ech_public_name' }, translate)).toBe(
      'latency.nodeFailure.ech',
    )
  })
  it.each([
    [0, 'good'],
    [199, 'good'],
    [200, 'warning'],
    [499, 'warning'],
    [500, 'bad'],
    [1500, 'bad'],
  ] as const)('colors %d ms as %s', (latency, tone) => {
    expect(latencyTone(measured(latency))).toBe(tone)
  })
  it('marks failed probes red even when an old latency exists', () => {
    const failed = { ...measured(18), alive: false }
    expect(latencyTone(failed)).toBe('bad')
    expect(hasMeasuredLatency(failed)).toBe(false)
  })
  it('does not classify unknown or invalid measurements as good', () => {
    expect(latencyTone()).toBe('unmeasured')
    for (const value of [-1, Infinity, NaN]) expect(latencyTone(measured(value))).toBe('unmeasured')
    expect(latencyTone({ ...measured(1), latencyMs: null })).toBe('unmeasured')
  })
})
