import type {RuntimeGroups} from './runtime-nodes';
import { describe, expect, it } from 'vitest'
import { activeNodeGroups  } from './runtime-nodes'

describe('current node markers', () => {
  const runtime: RuntimeGroups = {
    core: 'dae',
    ready: true,
    selectionAvailable: false,
    groups: [
      { name: 'proxy', nodes: ['name'], selectedNodeId: 'id', selectionKind: 'single' },
      { name: 'AI', nodes: ['name'], selectedNodeId: 'id', selectionKind: 'native' },
      { name: 'auto', nodes: ['a', 'b'] },
    ],
  }
  it('uses explicit runtime IDs and supports the same exit in multiple groups', () => {
    expect([...activeNodeGroups(runtime)]).toEqual([['id', ['proxy', 'AI']]])
  })
  it('never turns a candidate, old response or stopped core into current use', () => {
    expect(activeNodeGroups().size).toBe(0)
    expect(activeNodeGroups({ ...runtime, groups: null } as unknown as RuntimeGroups).size).toBe(0)
    expect(activeNodeGroups({ ...runtime, ready: false }).size).toBe(0)
    expect(activeNodeGroups({ ...runtime, groups: [{ name: 'draft', nodes: ['name'], selected: 'name' }] }).size).toBe(
      0,
    )
    expect(
      activeNodeGroups({
        ...runtime,
        groups: [{ name: 'bad', nodes: ['name'], selectedNodeId: 'id', selectionKind: 'guessed' }],
      }).size,
    ).toBe(0)
  })
})
