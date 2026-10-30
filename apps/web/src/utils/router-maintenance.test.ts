import { describe, expect, it, vi } from 'vitest'
import {
  checkGeodataVersion,
  coreProblemLabel,
  coreTransitionActive,
  geodataCheckExpired,
  geodataVersionState,
  jobActive,
  maintenanceAccessExpired,
  newerVersion,
  readMaintenance,
  releaseCheckExpired,
  updateTargetInstalled,
} from './router-maintenance'

describe('maintenance state', () => {
  it('reports a stale backend instead of silently claiming that rule versions were checked', async () => {
    vi.stubGlobal('window', {
      parent: { L: { require: async () => ({ declare: () => async () => ({ success: true, current: '0.6.55-1' }) }) } },
    })
    try {
      await expect(checkGeodataVersion()).rejects.toThrow('规则版本检查接口尚未就绪')
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it('checks rule metadata through the existing read-only interface without starting an update', async () => {
    const invoke = vi.fn(async (..._args: string[]) => ({
      success: true,
      current: '0.6.56-1',
      geodata_check: { version: '202610292204', tag: '202610292204', checked: 1000 },
    }))
    const declare = vi.fn(() => invoke)
    vi.stubGlobal('window', { parent: { L: { require: async () => ({ declare }) } } })
    try {
      await checkGeodataVersion()
      expect(declare).toHaveBeenLastCalledWith(
        expect.objectContaining({ object: 'daed-router', method: 'maintenance', params: ['check_geodata'] }),
      )
      expect(invoke).toHaveBeenLastCalledWith('1')
      await readMaintenance()
      expect(declare).toHaveBeenLastCalledWith(expect.objectContaining({ method: 'maintenance', params: [] }))
      expect(invoke).toHaveBeenLastCalledWith()
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it('compares only fresh known rule batches and never mistakes a failed check for latest', () => {
    const status = {
      success: true,
      current: '0.6.56-1',
      geodata: { version: '202610282204', tag: '202610282204', checked: 1 },
      geodata_check: { version: '202610282204', tag: '202610282204', checked: 1000 },
    }
    expect(geodataVersionState(status, false, false, 1000000)).toBe('latest')
    expect(
      geodataVersionState(
        { ...status, geodata_check: { ...status.geodata_check, version: '202610292204', tag: '202610292204' } },
        false,
        false,
        1000000,
      ),
    ).toBe('available')
    expect(
      geodataVersionState(
        { ...status, geodata_check: { ...status.geodata_check, version: '202610272204', tag: '202610272204' } },
        false,
        false,
        1000000,
      ),
    ).toBe('ahead')
    expect(geodataVersionState({ ...status, geodata_custom: true }, false, false, 1000000)).toBe('custom')
    expect(geodataVersionState({ ...status, geodata: undefined }, false, false, 1000000)).toBe('unmanaged')
    expect(geodataVersionState(status, true, false, 1000000)).toBe('checking')
    expect(geodataVersionState(status, false, true, 1000000)).toBe('error')
    expect(
      geodataVersionState(
        { ...status, geodata_check: { ...status.geodata_check, code: 'download_tls' } },
        false,
        false,
        1000000,
      ),
    ).toBe('error')
    expect(geodataVersionState(status, false, false, 1300000)).toBe('unknown')
    expect(geodataVersionState(status, false, false, 939000)).toBe('unknown')
    expect(
      geodataVersionState(
        { ...status, geodata_check: { ...status.geodata_check, version: 'different' } },
        false,
        false,
        1000000,
      ),
    ).toBe('unknown')
    expect(geodataCheckExpired(undefined, 1000000)).toBe(true)
    expect(geodataCheckExpired({ ...status.geodata_check, checked: NaN }, 1000000)).toBe(true)
  })
  it('recognizes an installed target without hiding rollback or other maintenance failures', () => {
    const job = {
      id: 'previous',
      action: 'update' as const,
      state: 'error' as const,
      phase: 'finished',
      started: 1,
      updated: 2,
      target: 'openwrt-v0.6.49',
      code: 'download_failed',
    }
    expect(updateTargetInstalled(job, '0.6.49-1')).toBe(true)
    expect(updateTargetInstalled(job, '0.6.50-1')).toBe(true)
    expect(updateTargetInstalled(job, '0.6.48-1')).toBe(false)
    expect(updateTargetInstalled({ ...job, action: 'geodata' }, '0.6.49-1')).toBe(false)
    expect(updateTargetInstalled({ ...job, state: 'running' }, '0.6.49-1')).toBe(false)
    expect(updateTargetInstalled({ ...job, target: undefined }, '0.6.49-1')).toBe(false)
  })
  it('refreshes old release checks on open, including after clock correction', () => {
    const latest = { version: '0.6.49', tag: 'openwrt-v0.6.49', checked: 1000 }
    expect(releaseCheckExpired(latest, 1000000)).toBe(false)
    expect(releaseCheckExpired(latest, 1300001)).toBe(true)
    expect(releaseCheckExpired(latest, 930000)).toBe(true)
    expect(releaseCheckExpired(undefined)).toBe(true)
  })
  it('pauses statistics only during a live core switch and resumes on success or rollback', () => {
    const status = { success: true, current: '0.6.47' }
    const job = {
      id: 'switch',
      action: 'core_switch_dae' as const,
      state: 'running' as const,
      phase: 'switching',
      started: 1,
      updated: 2,
    }
    expect(coreTransitionActive()).toBe(false)
    expect(coreTransitionActive({ ...status, job })).toBe(true)
    expect(coreTransitionActive({ ...status, job: { ...job, action: 'core_check_dae' } })).toBe(false)
    expect(coreTransitionActive({ ...status, job: { ...job, state: 'error', code: 'core_rolled_back' } })).toBe(false)
    expect(coreTransitionActive({ ...status, job: { ...job, state: 'success' } })).toBe(false)
  })
  it('shows Chinese reports and safely translates legacy or unknown core errors', () => {
    expect(coreProblemLabel('节点 1：unsupported_node_option')).not.toContain('unsupported_')
    expect(coreProblemLabel('unknown_future_error')).toContain('原配置保留')
    expect(coreProblemLabel('unknown_future_error')).toContain('诊断码：unknown_future_error')
    expect(coreProblemLabel('unknown_future_error')).not.toContain('兼容性')
    expect(coreProblemLabel('core_network_check_failed')).toContain('策略路由')
    expect(coreProblemLabel('core_network_ownership_failed')).toContain('保护其他服务')
    expect(coreProblemLabel('core_starting')).toContain('启动或恢复')
    expect(coreProblemLabel('open /private/token=secret: denied')).not.toContain('secret')
    const report = '节点 2「AI」：UDP 开关格式异常（参数：udp）'
    expect(coreProblemLabel(report)).toBe(report)
  })
  it('distinguishes expired maintenance permission from a running job', async () => {
    const parent = {
      L: {
        require: async () => ({
          declare: () => async () => {
            throw Object.assign(new Error('RPC permission denied'), { code: 6 })
          },
        }),
      },
    }
    vi.stubGlobal('window', { parent })
    try {
      await readMaintenance()
      throw new Error('permission failure accepted')
    } catch (error) {
      expect(maintenanceAccessExpired(error)).toBe(true)
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it.each([
    Object.assign(new Error('Login session is expired'), { name: 'SessionError' }),
    Object.assign(new Error('failed with ubus code 6: Permission denied'), { name: 'RPCError' }),
  ])('recognizes native LuCI session/permission failures', async (failure) => {
    vi.stubGlobal('window', {
      parent: {
        L: {
          require: async () => ({
            declare: () => async () => {
              throw failure
            },
          }),
        },
      },
    })
    try {
      await expect(readMaintenance()).rejects.toSatisfy(maintenanceAccessExpired)
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it('orders numeric releases without downgrading or comparing package revisions', () => {
    expect(newerVersion('0.6.33', '0.6.32-1')).toBe(true)
    expect(newerVersion('0.6.9', '0.6.32-1')).toBe(false)
    expect(newerVersion('0.6.32', '0.6.32-1')).toBe(false)
    expect(newerVersion('not-a-version', '0.6.32')).toBe(false)
  })
  it('polls queued/running tasks and stops for terminal states', () => {
    expect(jobActive()).toBe(false)
    for (const state of ['queued', 'running', 'success', 'error'] as const)
      expect(jobActive({ id: 'a', action: 'update', state, phase: 'installing', started: 0, updated: 0 })).toBe(
        ['queued', 'running'].includes(state),
      )
  })
})
