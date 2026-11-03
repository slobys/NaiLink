import type { NodeLatencyProbeResult } from '~/apis'
import { describe, expect, it } from 'vitest'
import { mockNodes, mockSubscriptions } from '~/mocks/data'
import {
  buildNodeLibrary,
  defaultNodeFilters,
  filterNodeLibrary,
  nodeLatencyRank,
  orderNodeLibrary,
  reorderVisibleNodes,
} from './node-library'

describe('node library', () => {
  it('sorts only valid successful measurements ahead of untested and failed nodes', () => {
    const probe = (latencyMs: number, alive = true): NodeLatencyProbeResult => ({
      id: 'x',
      latencyMs,
      alive,
      testedAt: '',
    })
    expect(nodeLatencyRank(probe(0))).toBe(0)
    expect(nodeLatencyRank(probe(25))).toBe(25)
    for (const value of [undefined, probe(0, false), probe(-1), probe(NaN), probe(Infinity)])
      expect(nodeLatencyRank(value)).toBe(Infinity)
  })
  it('keeps manual and subscription views separate without copying subscription nodes', () => {
    const manual = buildNodeLibrary(mockNodes.nodes.edges, [])
    const subscription = buildNodeLibrary([], [mockSubscriptions.subscriptions[0]])
    expect(manual.map((entry) => entry.node.id)).toEqual(mockNodes.nodes.edges.map((node) => node.id))
    expect(manual.every((entry) => entry.editable && !entry.sources.length)).toBe(true)
    expect(
      subscription.every((entry) => !entry.editable && entry.sources[0].id === mockSubscriptions.subscriptions[0].id),
    ).toBe(true)
  })
  it('merges subscription nodes without losing manual edit permissions or duplicating IDs', () => {
    const nodes = mockNodes.nodes.edges
    const library = buildNodeLibrary(nodes, mockSubscriptions.subscriptions)
    expect(new Set(library.map((entry) => entry.node.id)).size).toBe(library.length)
    for (const node of nodes) expect(library.find((entry) => entry.node.id === node.id)?.editable).toBe(true)
    expect(library.length).toBeGreaterThanOrEqual(nodes.length)
  })
  it('keeps saved custom ordering, drops stale IDs and appends new nodes', () => {
    const library = buildNodeLibrary(mockNodes.nodes.edges, [])
    const ids = library.map((entry) => entry.node.id)
    expect(orderNodeLibrary(library, ['stale', ids[1], ids[1]]).map((entry) => entry.node.id)).toEqual([
      ids[1],
      ...ids.filter((id) => id !== ids[1]),
    ])
  })
  it('filters by source and never mutates the library when sorting measured latency', () => {
    const library = buildNodeLibrary(mockNodes.nodes.edges, mockSubscriptions.subscriptions)
    const ids = library.map((entry) => entry.node.id)
    const latencies: Record<string, NodeLatencyProbeResult> = {
      [ids[0]]: { id: ids[0], latencyMs: 0, alive: false, testedAt: '' },
      [ids[1]]: { id: ids[1], latencyMs: 15, alive: true, testedAt: '' },
    }
    expect(filterNodeLibrary(library, { ...defaultNodeFilters, sort: 'latency' }, latencies)[0].node.id).toBe(ids[1])
    expect(library.map((entry) => entry.node.id)).toEqual(ids)
    const source = mockSubscriptions.subscriptions[0].id
    const filtered = filterNodeLibrary(library, { ...defaultNodeFilters, source }, {})
    expect(filtered.length).toBeGreaterThan(0)
    expect(filtered.every((entry) => entry.sources.some((item) => item.id === source))).toBe(true)
    expect(filterNodeLibrary(library, { ...defaultNodeFilters, search: 'missing-node-query' }, {})).toEqual([])
  })
  it('reorders filtered nodes without moving hidden entries', () => {
    expect(reorderVisibleNodes(['a', 'hidden', 'b', 'c'], ['a', 'b', 'c'], 2, 0)).toEqual(['c', 'hidden', 'a', 'b'])
    expect(reorderVisibleNodes(['a'], ['a'], 0, 4)).toEqual(['a'])
  })
})
