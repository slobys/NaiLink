import type { MaintenanceJob, MaintenanceStatus } from './router-maintenance'
import {
  jobActive,
  maintenanceAccessExpired,
  maintenanceMessages,
  MaintenanceRejectedError,
  readMaintenance,
  startConfigurationMaintenance,
} from './router-maintenance'

export const configurationJob = (job?: MaintenanceJob) =>
  !!job && (job.action === 'config_apply' || job.action === 'config_stop')

export const configurationPendingMessage =
  '应用结果暂未确认，后台任务可能仍在处理。请重新读取状态，请勿重复提交；节点与配置保留。'
export class ConfigurationPendingError extends Error {
  constructor(readonly operation: string) {
    super(configurationPendingMessage)
  }
}

// Only a matching durable receipt confirms the outcome. Never infer success
// from running/modified flags, or retry a mutation whose response was lost.
export async function applyRouterConfiguration(
  dry: boolean,
  publish: (status: MaintenanceStatus) => void,
  io = {
    start: startConfigurationMaintenance,
    read: readMaintenance,
    wait: () => new Promise<void>((resolve) => setTimeout(resolve, 2000)),
    now: () => performance.now(),
    timeout: 12 * 60_000,
    id: () =>
      Array.from(crypto.getRandomValues(new Uint8Array(12)), (byte) => byte.toString(16).padStart(2, '0')).join(''),
  },
) {
  const action = dry ? 'config_stop' : 'config_apply'
  let status: MaintenanceStatus | undefined
  const id = io.id()
  const deadline = io.now() + io.timeout
  const acceptanceDeadline = io.now() + 15_000
  try {
    status = await io.start(action, id)
    if (status.job?.id !== id || status.job.action !== action) throw new ConfigurationPendingError(id)
  } catch (error) {
    if (maintenanceAccessExpired(error) || error instanceof MaintenanceRejectedError) throw error
    // The start response may be lost after acceptance. Recover only our
    // client-generated ID, never another user's task, and never resubmit.
    status = undefined
  }
  while (true) {
    if (status) {
      const job = status.job
      if (job?.id !== id || job.action !== action) throw new ConfigurationPendingError(id)
      publish(status)
      if (job.state === 'success') return { run: 1 }
      if (!jobActive(job)) {
        throw new Error(maintenanceMessages[job.code || ''] || configurationPendingMessage)
      }
    } else if (io.now() >= acceptanceDeadline) {
      throw new ConfigurationPendingError(id)
    }
    if (io.now() >= deadline) throw new ConfigurationPendingError(id)
    await io.wait()
    try {
      status = await io.read()
    } catch (error) {
      if (maintenanceAccessExpired(error)) throw error
      // A brief route/DNS transition must not turn an accepted task into a
      // failure or cause a second application. Keep reading its original ID.
    }
  }
}
