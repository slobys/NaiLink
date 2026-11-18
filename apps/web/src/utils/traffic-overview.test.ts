import { describe, expect, it } from 'vitest'
import { trafficHistoryDue, trafficOverviewInterval } from './traffic-overview'

describe('traffic overview polling', () => {
  it('follows the native sample interval at each connection threshold', () => {
    expect(trafficOverviewInterval(true, 10, false)).toBe(1_000)
    expect(trafficOverviewInterval(true, 2048, false)).toBe(2_000)
    expect(trafficOverviewInterval(true, 19_000, false)).toBe(4_000)
    expect(trafficOverviewInterval(false, 19_000, false)).toBe(1_000)
  })

  it('backs off on errors and respects slower chart windows', () => {
    expect(trafficOverviewInterval(true, 19_000, true)).toBe(5_000)
    expect(trafficHistoryDue(60, undefined, 1_000)).toBe(true)
    expect(trafficHistoryDue(60, 1_000, 5_999)).toBe(false)
    expect(trafficHistoryDue(60, 1_000, 6_000)).toBe(true)
    expect(trafficHistoryDue(3600, 1_000, 6_000)).toBe(false)
    expect(trafficHistoryDue(3600, 1_000, 11_000)).toBe(true)
  })
})
