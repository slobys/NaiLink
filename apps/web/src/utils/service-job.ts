import type { GQLClientInterface } from '~/contexts'
import type { ServiceProfile, ServiceResult } from '~/hooks/useServiceChecks'

interface Snapshot {
  id: string
  phase: string
  code?: string
  completed: number
  cursor: number
  results: ServiceResult[]
}
// Reuse an operation on a lost response. Cancel by operation, including when
// the browser never received the job ID. Never start a replacement job.
export async function runServiceJob(
  client: GQLClientInterface,
  ids: string[],
  profile: ServiceProfile,
  force: boolean,
  update: (value: Snapshot) => void,
  stopped: () => boolean,
  wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)),
) {
  const operation = Array.from(crypto.getRandomValues(new Uint8Array(16)), (v) => v.toString(16).padStart(2, '0')).join(
    '',
  )
  const request = async (query: string, variables: Record<string, unknown>, busyRetry = false) => {
    for (let attempt = 0; ; attempt++) {
      const controller = new AbortController()
      const deadline = setTimeout(() => controller.abort(), 15_000)
      try {
        return await client.request<Record<string, string>>(query, variables, controller.signal)
      } catch (error) {
        const response = (error as { response?: { status?: number; errors?: unknown[] } })?.response
        const busy = busyRetry && String(error).includes('latency_job_busy')
        if (
          attempt >= (busy ? 5 : 2) ||
          response?.status === 401 ||
          response?.status === 403 ||
          (response?.errors?.length && !busy)
        )
          throw error
        if (stopped() && busy) throw error
        await wait(busy ? 750 : 250 * (attempt + 1))
      } finally {
        clearTimeout(deadline)
      }
    }
  }
  let finished = false
  try {
    const started = await request(
      'mutation($ids:[ID!]!,$profile:String!,$operation:String!,$force:Boolean){startServiceTest(ids:$ids,profile:$profile,operation:$operation,force:$force)}',
      { ids, profile, operation, force },
      true,
    )
    let value = JSON.parse(started.startServiceTest) as Snapshot
    if (!/^[a-f0-9]{32}$/.test(value.id)) throw new Error('invalid_test_job')
    for (;;) {
      if (stopped()) return
      update(value)
      if (['completed', 'cancelled', 'failed'].includes(value.phase)) {
        finished = true
        if (value.phase === 'failed') throw new Error(value.code || 'test_job_failed')
        return
      }
      await wait(800)
      if (stopped()) return
      const data = await request('query($id:String!,$after:Int){latencyTest(id:$id,after:$after)}', {
        id: value.id,
        after: value.cursor,
      })
      value = JSON.parse(data.latencyTest) as Snapshot
    }
  } finally {
    if (!finished)
      await request('mutation($operation:String!){stopServiceTest(operation:$operation)}', { operation }).catch(
        () => {},
      )
  }
}
