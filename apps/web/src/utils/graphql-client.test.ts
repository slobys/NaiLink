import { GraphQLClient } from 'graphql-request'
import { describe, expect, it, vi } from 'vitest'
import { cancellableGraphQLClient, graphQLNotice } from './graphql-client'

describe('router graphQL notices', () => {
  it('keeps transient overview failures local while translating real core failures', () => {
    expect(graphQLNotice({ message: 'core_not_ready', path: ['general', 'runtimeOverview'] }, true)).toBeUndefined()
    for (const message of ['core_api_invalid', 'core_api_response_too_large', 'unrelated statistics failure']) {
      expect(graphQLNotice({ message, path: ['general', 'runtimeOverview'] }, true)).toBeUndefined()
    }
    expect(
      graphQLNotice({ message: 'statistics field failed', path: ['general', 'runtimeOverview', 'samples'] }, true),
    ).toBeUndefined()
    expect(graphQLNotice({ message: 'core_api_invalid', path: ['switchCore'] }, true)).toContain('异常数据')
    expect(graphQLNotice({ message: 'core_not_ready', path: ['switchCore'] }, true)).toContain('就绪')
    expect(graphQLNotice({ message: 'core_rolled_back', path: ['switchCore'] }, true)).toContain('恢复')
    expect(graphQLNotice({ message: 'unrelated failure' }, true)).toBe('unrelated failure')
    expect(graphQLNotice({ message: 'core_not_ready', path: ['general', 'runtimeOverview'] }, false)).toBe(
      'core_not_ready',
    )
  })
})

function waitingClient() {
  let activeSignal: AbortSignal | undefined
  const client = new GraphQLClient('https://fixture.invalid/graphql', {
    fetch: async (_url, init) => {
      activeSignal = init?.signal as AbortSignal
      const signal = activeSignal
      return await new Promise<Response>((_resolve, reject) => {
        if (signal.aborted) reject(signal.reason)
        else signal.addEventListener('abort', () => reject(signal.reason), { once: true })
      })
    },
  })
  return { client, signal: () => activeSignal }
}

describe('graphQL polling request lifetime', () => {
  it('cancels the real transport when a query is abandoned', async () => {
    const fixture = waitingClient()
    const controller = new AbortController()
    const promise = cancellableGraphQLClient(fixture.client).request('query { status }', undefined, controller.signal)
    const check = expect(promise).rejects.toMatchObject({ name: 'AbortError' })
    controller.abort()
    await check
    expect(fixture.signal()?.aborted).toBe(true)
  })

  it('times out a hung query and removes its timer', async () => {
    vi.useFakeTimers()
    try {
      const fixture = waitingClient()
      const promise = cancellableGraphQLClient(fixture.client, 100).request(
        'query { status }',
        undefined,
        new AbortController().signal,
      )
      const check = expect(promise).rejects.toMatchObject({ name: 'TimeoutError' })
      await vi.advanceTimersByTimeAsync(100)
      await check
      expect(fixture.signal()?.aborted).toBe(true)
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('retains unsignalled mutations and cleans up successful query timers', async () => {
    vi.useFakeTimers()
    try {
      let mutationSignal: RequestInit['signal']
      const transport: typeof fetch = async (_url, init) => {
        mutationSignal = init?.signal
        return new Response(JSON.stringify({ data: { status: 'ok' } }), {
          headers: { 'content-type': 'application/json' },
        })
      }
      const client = cancellableGraphQLClient(
        new GraphQLClient('https://fixture.invalid/graphql', { fetch: transport }),
      )
      expect(await client.request('mutation { status }')).toEqual({ status: 'ok' })
      expect(mutationSignal).toBeUndefined()
      expect(await client.request('query { status }', undefined, new AbortController().signal)).toEqual({
        status: 'ok',
      })
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })
})
