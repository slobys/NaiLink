import { describe, expect, it } from 'vitest'
import { connectivityLabel, connectivityTargets, connectivityTone, parseConnectivity } from './connectivity'

function fixture() {
  return {
    core: 'dae',
    state: 'ready',
    checking: false,
    checkedAt: 1234,
    targets: connectivityTargets.map(({ id }) => ({
      id,
      samples: [{ at: 1234, status: 'ok', latencyMs: 31, httpStatus: 204 }],
    })),
  }
}
describe('dashboard connectivity', () => {
  it('distinguishes reachable HTTP restrictions from network failure and slow responses', () => {
    expect(connectivityLabel({ at: 1, status: 'restricted', httpStatus: 403 })).toBe('访问受限 HTTP 403')
    expect(connectivityLabel({ at: 1, status: 'restricted', httpStatus: 503 })).toBe('服务异常 HTTP 503')
    expect(connectivityLabel({ at: 1, status: 'error', code: 'dns_failed' })).toBe('解析失败')
    expect(connectivityTone({ at: 1, status: 'restricted', latencyMs: 10 })).toBe('warning')
    expect(connectivityTone({ at: 1, status: 'ok', latencyMs: 800 })).toBe('warning')
    expect(connectivityTone({ at: 1, status: 'error', code: 'timeout' })).toBe('error')
    expect(connectivityTone()).toBe('muted')
  })
  it('accepts an empty stopped snapshot and valid histories', () => {
    expect(parseConnectivity(JSON.stringify(fixture())).targets[0].samples[0].latencyMs).toBe(31)
    const data = fixture()
    data.state = 'stopped'
    data.targets.forEach((t) => {
      t.samples = []
    })
    expect(parseConnectivity(JSON.stringify(data)).state).toBe('stopped')
  })
  it('rejects duplicate targets, oversized histories and incomplete successes', () => {
    const duplicate = fixture()
    duplicate.targets[0].id = duplicate.targets[1].id
    const oversized = fixture()
    oversized.targets[0].samples = Array.from({ length: 19 }, (_, at) => ({ ...oversized.targets[0].samples[0], at }))
    const invalid = fixture()
    invalid.targets[0].samples[0].latencyMs = -1
    const missing = fixture()
    delete (missing.targets[0].samples[0] as { httpStatus?: number }).httpStatus
    for (const data of [duplicate, oversized, invalid, missing, { ...fixture(), targets: [null] }, null])
      expect(() => parseConnectivity(JSON.stringify(data))).toThrow()
  })
})
