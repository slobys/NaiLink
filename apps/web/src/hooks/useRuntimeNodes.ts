import type {RuntimeGroups} from '~/utils/runtime-nodes';
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useGQLQueryClient } from '~/contexts'
import { jobActive } from '~/utils/router-maintenance'
import { routerMode } from '~/utils/router-mode'
import { activeNodeGroups  } from '~/utils/runtime-nodes'
import { useRouterMaintenanceQuery } from './useRouterMaintenance'

// Shared by every visible node surface. Hidden tabs do not poll; failures clear
// active markers instead of presenting an old exit as current.
export function useRuntimeNodes(enabled = true, poll = false) {
  const gql = useGQLQueryClient()
  const maintenance = useRouterMaintenanceQuery(false)
  const changing = jobActive(maintenance.data?.job) || maintenance.data?.cores?.phase === 'switching'
  const query = useQuery({
    queryKey: ['core-runtime-groups'],
    enabled: routerMode && enabled && poll && !changing,
    staleTime: 5000,
    refetchInterval: poll ? 10000 : false,
    refetchIntervalInBackground: false,
    retry: false,
    queryFn: async () => {
      const result = await gql.request<{ coreRuntimeGroups: string }>('{coreRuntimeGroups}')
      return JSON.parse(result.coreRuntimeGroups) as RuntimeGroups
    },
  })
  const active = useMemo(
    () => activeNodeGroups(query.isError || changing ? undefined : query.data),
    [query.data, query.isError, changing],
  )
  return { ...query, active }
}
