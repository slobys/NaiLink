import type { GQLClientInterface } from '~/contexts'
import { describe, expect, it } from 'vitest'
import { runServiceJob } from './service-job'
const id = 'a'.repeat(32)
describe('purpose checks', () => {
  it('recovers one operation after a lost start response and incremental poll', async () => {
    let starts = 0
    let operation = ''
    const calls: string[] = []
    const client = {
      request: async (query: string, variables: Record<string, unknown>) => {
        calls.push(query)
        if (query.includes('startServiceTest')) {
          if (!starts++) {
            operation = String(variables.operation)
            throw new Error('lost response')
          }
          expect(variables.operation).toBe(operation)
          return {
            startServiceTest: JSON.stringify({
              id,
              phase: 'running',
              cursor: 1,
              completed: 1,
              results: [{ id: 'one', alive: true, message: 'service_accessible' }],
            }),
          }
        }
        return {
          latencyTest: JSON.stringify({
            id,
            phase: 'completed',
            cursor: 2,
            completed: 2,
            results: [{ id: 'two', alive: false, message: 'service_challenge' }],
          }),
        }
      },
    } as unknown as GQLClientInterface
    const found: string[] = []
    await runServiceJob(
      client,
      ['one', 'two'],
      'ai',
      false,
      (value) => found.push(...value.results.map((r) => r.id)),
      () => false,
      async () => {},
    )
    expect(found).toEqual(['one', 'two'])
    expect(starts).toBe(2)
    expect(calls.some((q) => q.includes('stopServiceTest'))).toBe(false)
  })
  it('cancels by operation when every start response is lost', async () => {
    const operations: string[] = []
    let cancelled = ''
    const client = {
      request: async (query: string, args: Record<string, unknown>) => {
        if (query.includes('startServiceTest')) {
          operations.push(String(args.operation))
          throw new Error('lost response')
        }
        cancelled = String(args.operation)
        return { stopServiceTest: 1 }
      },
    } as unknown as GQLClientInterface
    await expect(
      runServiceJob(
        client,
        ['one'],
        'netflix',
        false,
        () => {},
        () => false,
        async () => {},
      ),
    ).rejects.toThrow('lost response')
    expect(operations).toHaveLength(3)
    expect(new Set([...operations, cancelled]).size).toBe(1)
  })
  it('closing a picker cancels its own job without accepting results', async () => {
    let cancelled = false
    const client = {
      request: async (query: string) =>
        query.includes('startServiceTest')
          ? { startServiceTest: JSON.stringify({ id, phase: 'running', cursor: 0, completed: 0, results: [] }) }
          : ((cancelled = true), { stopServiceTest: 1 }),
    } as unknown as GQLClientInterface
    await runServiceJob(
      client,
      ['one'],
      'proxy',
      false,
      () => {
        throw new Error('updated closed picker')
      },
      () => true,
      async () => {},
    )
    expect(cancelled).toBe(true)
  })
})
