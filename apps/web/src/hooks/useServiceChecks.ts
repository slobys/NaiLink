import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useGQLQueryClient } from '~/contexts'
import { routerMode } from '~/utils/router-mode'
import { runServiceJob } from '~/utils/service-job'

export type ServiceProfile = '' | 'proxy' | 'ai' | 'netflix'
export interface GroupService {
  id: string
  profile: ServiceProfile
  fallbackName?: string
}
export interface ServiceResult {
  id: string
  alive: boolean
  message?: string
  testedAt: string
  latencyMs?: number
}
const serviceKey = ['group-services']
export function useGroupServices(enabled = true) {
  const client = useGQLQueryClient()
  return useQuery({
    queryKey: serviceKey,
    enabled: routerMode && enabled,
    staleTime: 30_000,
    queryFn: async ({ signal }) => {
      const data = await client.request<{ groupServices: string }>('query{groupServices}', undefined, signal)
      return JSON.parse(data.groupServices) as GroupService[]
    },
    retry: false,
  })
}
export function useGroupServiceMutation() {
  const client = useGQLQueryClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async (value: { id: string; profile: ServiceProfile }) =>
      client.request('mutation($id:ID!,$profile:String!){setGroupService(id:$id,profile:$profile)}', value),
    onSuccess: () => cache.invalidateQueries({ queryKey: serviceKey }),
  })
}
export function useServiceChecks(opened: boolean, groupId: string, ids: string[], blocked = false) {
  const client = useGQLQueryClient()
  const groups = useGroupServices(opened)
  const change = useGroupServiceMutation()
  const profile: ServiceProfile = groups.data?.find((g) => g.id === groupId)?.profile || ''
  const signature = [...new Set(ids)].sort().join(',')
  const candidates = useMemo(() => (signature ? signature.split(',') : []), [signature])
  const [results, setResults] = useState<Record<string, ServiceResult>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [completed, setCompleted] = useState(0)
  const [automatic, setAutomatic] = useState(true)
  const [onlyPassed, setOnlyPassed] = useState(false)
  const [generation, setGeneration] = useState(0)
  const owner = useRef(0)
  const stop = useRef(false)
  const force = useRef(false)
  const cancel = useCallback(() => {
    stop.current = true
    setAutomatic(false)
  }, [])
  const retry = useCallback(() => {
    force.current = true
    setAutomatic(true)
    setGeneration((v) => v + 1)
  }, [])
  useEffect(() => {
    setOnlyPassed(false)
    setResults({})
    setCompleted(0)
    setError('')
    setAutomatic(true)
  }, [groupId, profile])
  useEffect(() => {
    if (!opened || !routerMode || !profile || !candidates.length || blocked || !automatic) return
    const token = ++owner.current
    const controller = new AbortController()
    stop.current = false
    setBusy(true)
    setError('')
    const active = () => owner.current === token && !controller.signal.aborted
    const request = <T>(query: string, variables?: Record<string, unknown>, signal?: AbortSignal) =>
      client.request<T>(query, variables, signal)
    const update = (values: ServiceResult[]) => {
      if (active()) setResults((prev) => ({ ...prev, ...Object.fromEntries(values.map((r) => [r.id, r])) }))
    }
    ;(async () => {
      const cached = await request<{ serviceResults: string }>(
        'query($ids:[ID!]!,$profile:String!){serviceResults(ids:$ids,profile:$profile)}',
        { ids: candidates, profile },
        controller.signal,
      )
      const saved = JSON.parse(cached.serviceResults) as ServiceResult[]
      if (!active()) return
      update(saved)
      setCompleted(saved.length)
      if (saved.length === candidates.length && !force.current) return
      const refresh = force.current
      force.current = false
      await runServiceJob(
        client,
        candidates,
        profile,
        refresh,
        (snapshot) => {
          if (active()) {
            update(snapshot.results)
            setCompleted(snapshot.completed)
          }
        },
        () => !active() || stop.current,
      )
    })()
      .catch((e) => {
        if (active())
          setError(
            String(e).includes('latency_job_busy')
              ? '已有节点测试正在运行，稍后重新检测。'
              : '检测暂未完成，已保留结果，可以重试。',
          )
      })
      .finally(() => {
        if (owner.current === token) setBusy(false)
      })
    return () => {
      controller.abort()
    }
  }, [opened, profile, signature, blocked, automatic, generation, client, candidates])
  return {
    available: routerMode,
    profile,
    results,
    busy: opened && busy,
    error,
    completed,
    total: candidates.length,
    automatic,
    onlyPassed,
    setOnlyPassed,
    cancel,
    retry,
    changeProfile: (next: ServiceProfile) => change.mutate({ id: groupId, profile: next }),
    changing: change.isPending,
    profileError: groups.isError || change.isError,
  }
}
