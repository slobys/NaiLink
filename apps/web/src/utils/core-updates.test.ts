import { describe, expect, it } from 'vitest'
import { coreUpdateState, coreVersionCompare } from './core-updates'

describe('independent core updates', () => {
  const now = 1_800_000_000_000
  const core = {
    installed: true,
    supported: true,
    version: '1.14.3-nailink.1',
    update: { version: '1.14.4-nailink.1', checked: now / 1000 },
  }
  it('compares both upstream and compatibility revisions', () => {
    expect(coreVersionCompare('1.14.4-nailink.1', '1.14.3-nailink.1')).toBe(1)
    expect(coreVersionCompare('1.14.4-nailink.2', '1.14.4-nailink.1')).toBe(1)
    expect(coreVersionCompare('1.19.33', '1.19.34')).toBe(-1)
    expect(coreVersionCompare('unknown', '1.19.34')).toBeUndefined()
  })
  it('requires a fresh successful check before offering an update', () => {
    expect(coreUpdateState(core, now)).toBe('available')
    expect(coreUpdateState({ ...core, update: undefined }, now)).toBe('unknown')
    expect(coreUpdateState(core, now + 3_600_000)).toBe('unknown')
    expect(coreUpdateState({ ...core, update: { ...core.update, checked: now / 1000 + 61 } }, now)).toBe('unknown')
    expect(coreUpdateState({ ...core, update: { ...core.update, code: 'download_timeout' } }, now)).toBe('error')
  })
  it('distinguishes latest, newer local version and not installed', () => {
    expect(coreUpdateState({ ...core, version: '1.14.4-nailink.1' }, now)).toBe('latest')
    expect(coreUpdateState({ ...core, version: '1.14.5-nailink.1' }, now)).toBe('ahead')
    expect(coreUpdateState({ ...core, installed: false, version: '' }, now)).toBe('available')
  })
})
