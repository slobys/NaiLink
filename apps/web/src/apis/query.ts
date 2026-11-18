import type { NodeLatencyProbeResult } from './mutation'
import type { GQLClientInterface } from '~/contexts'

import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  QUERY_KEY_CONFIG,
  QUERY_KEY_DNS,
  QUERY_KEY_GENERAL,
  QUERY_KEY_GROUP,
  QUERY_KEY_NODE,
  QUERY_KEY_NODE_LATENCY,
  QUERY_KEY_ROUTING,
  QUERY_KEY_STORAGE,
  QUERY_KEY_SUBSCRIPTION,
  QUERY_KEY_TRAFFIC,
  QUERY_KEY_USER,
} from '~/constants'
import { useGQLQueryClient } from '~/contexts'
import { graphql } from '~/schemas/gql'
import { latencyResultFields } from '~/utils/latency'
import { routerMode } from '~/utils/router-mode'
import { trafficHistoryDue, trafficOverviewInterval } from '~/utils/traffic-overview'

export function getModeRequest(gqlClient: GQLClientInterface) {
  return async () => {
    const { jsonStorage } = await gqlClient.request(
      graphql(`
        query Mode($paths: [String!]) {
          jsonStorage(paths: $paths)
        }
      `),
      {
        paths: ['mode'],
      },
    )

    return jsonStorage[0]
  }
}

export function getDefaultsRequest(gqlClient: GQLClientInterface) {
  return async () => {
    const data = await gqlClient.request(
      graphql(`
        query Defaults($paths: [String!]) {
          jsonStorage(paths: $paths)
        }
      `),
      {
        paths: ['defaultConfigID', 'defaultRoutingID', 'defaultDNSID', 'defaultGroupID'],
      },
    )

    const [defaultConfigID, defaultRoutingID, defaultDNSID, defaultGroupID] = data.jsonStorage

    return {
      defaultConfigID,
      defaultRoutingID,
      defaultDNSID,
      defaultGroupID,
    }
  }
}

export function getInterfacesRequest(gqlClient: GQLClientInterface) {
  return () =>
    gqlClient.request(
      graphql(`
        query Interfaces($up: Boolean) {
          general {
            interfaces(up: $up) {
              name
              ifindex
              ip
              flag {
                default {
                  gateway
                }
              }
            }
          }
        }
      `),
      {
        up: true,
      },
    )
}

export function useDefaultsQuery() {
  const gqlClient = useGQLQueryClient()

  const { data } = useQuery({
    queryKey: QUERY_KEY_STORAGE,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query JsonStorage($paths: [String!]) {
            jsonStorage(paths: $paths)
          }
        `),
        {
          paths: ['defaultConfigID', 'defaultRoutingID', 'defaultDNSID', 'defaultGroupID'],
        },
      ),
  })

  if (!data) {
    return
  }

  const [defaultConfigID, defaultRoutingID, defaultDNSID, defaultGroupID] = data.jsonStorage

  return {
    defaultConfigID,
    defaultRoutingID,
    defaultDNSID,
    defaultGroupID,
  }
}

export function useGeneralQuery() {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_GENERAL,
    queryFn: async ({ signal }) =>
      gqlClient.request(
        graphql(`
          query General($up: Boolean) {
            general {
              dae {
                running
                modified
                version
              }
              interfaces(up: $up) {
                name
                ifindex
                ip
                flag {
                  default {
                    gateway
                  }
                }
              }
            }
          }
        `),
        {
          up: true,
        },
        signal,
      ),
  })
}

export interface TrafficOverviewQueryData {
  updatedAt: string
  uploadRate: number
  downloadRate: number
  uploadTotal: string
  downloadTotal: string
  activeConnections: number
  udpSessions: number
  historyFetchedAt?: number
  historyUpdatedAt?: string
  samples: Array<{
    timestamp: string
    uploadRate: number
    downloadRate: number
  }>
}

export function useTrafficOverviewQuery(
  windowSec: number,
  maxPoints: number,
  enabled = true,
  native = false,
  showHistory = true,
) {
  const gqlClient = useGQLQueryClient()
  const queryClient = useQueryClient()
  const queryKey = [...QUERY_KEY_TRAFFIC, windowSec, maxPoints, showHistory]

  return useQuery({
    queryKey,
    enabled,
    queryFn: async ({ signal }) => {
      const previous = queryClient.getQueryData<TrafficOverviewQueryData>(queryKey)
      const history = showHistory && trafficHistoryDue(windowSec, previous?.historyFetchedAt, Date.now())
      const data = await gqlClient.request<
        {
          general: {
            runtimeOverview: Omit<TrafficOverviewQueryData, 'samples'> & {
              samples?: TrafficOverviewQueryData['samples']
            }
          }
        },
        { windowSec: number; maxPoints: number; includeHistory: boolean }
      >(
        `
          query TrafficOverview($windowSec: Int!, $maxPoints: Int!, $includeHistory: Boolean!) {
            general {
              runtimeOverview(windowSec: $windowSec, maxPoints: $maxPoints) {
                updatedAt
                uploadRate
                downloadRate
                uploadTotal
                downloadTotal
                activeConnections
                udpSessions
                samples @include(if: $includeHistory) {
                  timestamp
                  uploadRate
                  downloadRate
                }
              }
            }
          }
        `,
        {
          windowSec: history ? windowSec : 1,
          maxPoints: history ? maxPoints : 2,
          includeHistory: history,
        },
        signal,
      )

      const snapshot = data.general.runtimeOverview
      return {
        ...snapshot,
        samples: history ? (snapshot.samples ?? []) : (previous?.samples ?? []),
        historyFetchedAt: history ? Date.now() : previous?.historyFetchedAt,
        historyUpdatedAt: history ? snapshot.updatedAt : previous?.historyUpdatedAt,
      }
    },
    placeholderData: (previousData) => previousData,
    retry: false,
    refetchInterval: (query) =>
      enabled ? trafficOverviewInterval(native, query.state.data?.activeConnections ?? 0, !!query.state.error) : false,
  })
}

export function useNodeLatenciesQuery(refetchIntervalMs: number, enabled = true) {
  const gqlClient = useGQLQueryClient()
  const safeInterval = Math.max(1_000, refetchIntervalMs)

  return useQuery({
    queryKey: QUERY_KEY_NODE_LATENCY,
    enabled,
    queryFn: async ({ signal }) => {
      const data = await gqlClient.request<{ nodeLatencies: NodeLatencyProbeResult[] }>(
        `
          query NodeLatencies {
            nodeLatencies {
              ${latencyResultFields(routerMode)}
            }
          }
        `,
        undefined,
        signal,
      )

      return data.nodeLatencies
    },
    placeholderData: (previousData) => previousData,
    refetchInterval: safeInterval,
    refetchIntervalInBackground: false,
  })
}

export function useNodesQuery(enabled = true) {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_NODE,
    enabled,
    staleTime: 15_000,
    queryFn: async ({ signal }) =>
      gqlClient.request(
        graphql(`
          query Nodes {
            nodes {
              edges {
                id
                name
                link
                address
                protocol
                tag
              }
            }
          }
        `),
        undefined,
        signal,
      ),
  })
}

export function useSubscriptionsQuery(enabled = true) {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_SUBSCRIPTION,
    enabled,
    staleTime: 15_000,
    queryFn: async ({ signal }) =>
      gqlClient.request(
        graphql(`
          query Subscriptions {
            subscriptions {
              id
              tag
              status
              link
              info
              updatedAt
              cronExp
              cronEnable
              nodes {
                edges {
                  id
                  name
                  protocol
                  link
                }
              }
            }
          }
        `),
        undefined,
        signal,
      ),
  })
}

export function useSubscriptionNamesQuery(enabled: boolean) {
  const gqlClient = useGQLQueryClient()
  return useQuery({
    queryKey: [...QUERY_KEY_SUBSCRIPTION, 'names'],
    enabled,
    staleTime: 15_000,
    queryFn: ({ signal }) =>
      gqlClient.request<{ subscriptions: Array<{ id: string; tag: string }> }>(
        'query SubscriptionNames { subscriptions { id tag } }',
        undefined,
        signal,
      ),
  })
}

export function useConfigsQuery() {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_CONFIG,
    staleTime: 15_000,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query Configs {
            configs {
              id
              name
              selected
              global {
                logLevel
                tproxyPort
                allowInsecure
                checkInterval
                checkTolerance
                lanInterface
                wanInterface
                udpCheckDns
                tcpCheckUrl
                bootstrapResolver
                fallbackResolver
                dialMode
                tcpCheckHttpMethod
                disableWaitingNetwork
                autoConfigKernelParameter
                sniffingTimeout
                tlsImplementation
                utlsImitate
                tproxyPortProtect
                soMarkFromDae
                pprofPort
                enableLocalTcpFastRedirect
                mptcp
                bandwidthMaxTx
                bandwidthMaxRx
              }
            }
          }
        `),
      ),
  })
}

export function useGroupsQuery(enabled = true) {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_GROUP,
    enabled,
    staleTime: 15_000,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query Groups {
            groups {
              id
              name
              nodes {
                id
                link
                name
                address
                protocol
                tag
                subscriptionID
              }
              subscriptions {
                nameFilterRegex
                matchedCount
                subscription {
                  id
                  updatedAt
                  tag
                  link
                  status
                  info
                }
                matchedNodes {
                  id
                  link
                  name
                  address
                  protocol
                  tag
                  subscriptionID
                }
              }
              policy
              policyParams {
                key
                val
              }
            }
          }
        `),
      ),
  })
}

export function useRoutingsQuery() {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_ROUTING,
    staleTime: 15_000,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query Routings {
            routings {
              id
              name
              selected
              routing {
                string
              }
            }
          }
        `),
      ),
  })
}

export function useDNSsQuery() {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_DNS,
    staleTime: 15_000,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query DNSs {
            dnss {
              id
              name
              dns {
                string

                routing {
                  request {
                    string
                  }
                  response {
                    string
                  }
                }
              }
              selected
            }
          }
        `),
      ),
  })
}

export function useUserQuery() {
  const gqlClient = useGQLQueryClient()

  return useQuery({
    queryKey: QUERY_KEY_USER,
    queryFn: async () =>
      gqlClient.request(
        graphql(`
          query User {
            user {
              username
              name
              avatar
            }
          }
        `),
      ),
  })
}
