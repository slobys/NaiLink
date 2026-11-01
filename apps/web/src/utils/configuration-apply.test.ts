import type { MaintenanceStatus } from './router-maintenance'
import { describe, expect, it, vi } from 'vitest'
import { applyRouterConfiguration, configurationPendingMessage } from './configuration-apply'

function receipt(state: 'running' | 'success' | 'error', id = 'original', code?: string): MaintenanceStatus {
  return {
    success: true,
    current: 'fixture',
    job: { id, action: 'config_apply', state, code, phase: 'applying', started: 1, updated: 1 },
  }
}

describe('configuration task acknowledgement', () => {
  it('waits beyond the CGI limit, tolerates missing status replies, and submits exactly once', async () => {
    let time = 0
    const io = {
      start: vi.fn(async () => receipt('running')),
      read: vi.fn(async () => {
        if (time === 2000) throw new TypeError('network changed')
        return receipt(time >= 42000 ? 'success' : 'running')
      }),
      wait: async () => {
        time += 2000
      },
      now: () => time,
      timeout: 60_000,
      id: () => 'original',
    }
    const publish = vi.fn()
    expect(await applyRouterConfiguration(false, publish, io)).toEqual({ run: 1 })
    expect(time).toBe(42000)
    expect(io.start).toHaveBeenCalledExactlyOnceWith('config_apply', 'original')
    expect(publish).toHaveBeenLastCalledWith(receipt('success'))
  })

  it('does not infer success from a different operation or resubmit a lost request', async () => {
    const io = {
      start: vi.fn(async () => receipt('running')),
      read: async () => receipt('success', 'other'),
      wait: async () => {},
      now: () => 0,
      timeout: 100,
      id: () => 'original',
    }
    await expect(applyRouterConfiguration(false, () => {}, io)).rejects.toThrow(configurationPendingMessage)
    expect(io.start).toHaveBeenCalledTimes(1)
    io.start.mockRejectedValueOnce(new TypeError('response lost'))
    await expect(applyRouterConfiguration(false, () => {}, io)).rejects.toThrow(configurationPendingMessage)
    expect(io.start).toHaveBeenCalledTimes(2)
  })

  it('recovers a lost acceptance response only by its exact client ID', async () => {
    const io = {
      start: vi.fn(async () => {
        throw new TypeError('accepted response lost')
      }),
      read: vi.fn(async () => receipt('success')),
      wait: async () => {},
      now: () => 0,
      timeout: 100,
      id: () => 'original',
    }
    expect(await applyRouterConfiguration(false, () => {}, io)).toEqual({ run: 1 })
    expect(io.start).toHaveBeenCalledExactlyOnceWith('config_apply', 'original')
    expect(io.read).toHaveBeenCalledTimes(1)
  })

  it('reports a confirmed failure and bounds an unconfirmed wait without replay', async () => {
    let time = 0
    const io = {
      start: vi.fn(async () => receipt('running')),
      read: vi.fn(async () => receipt('error', 'original', 'configuration_apply_failed')),
      wait: async () => {
        time += 100
      },
      now: () => time,
      timeout: 300,
      id: () => 'original',
    }
    await expect(applyRouterConfiguration(false, () => {}, io)).rejects.toThrow('配置应用未完成')
    io.read.mockRejectedValue(new TypeError('offline'))
    await expect(applyRouterConfiguration(false, () => {}, io)).rejects.toThrow(configurationPendingMessage)
    expect(io.start).toHaveBeenCalledTimes(2)
  })
})
