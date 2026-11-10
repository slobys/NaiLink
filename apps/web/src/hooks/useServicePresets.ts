import { useMutation, useQueryClient } from '@tanstack/react-query'
import { QUERY_KEY_GENERAL, QUERY_KEY_GROUP, QUERY_KEY_ROUTING } from '~/constants'
import { useGQLQueryClient } from '~/contexts'

export function useServicePresets() {
  const client = useGQLQueryClient()
  const cache = useQueryClient()
  return useMutation({
    mutationFn: async ({ proxy, select = false }: { proxy: string; select?: boolean }) => {
      const data = await client.request<{ ensureServicePresets: string }>(
        'mutation($proxy:ID!,$name:String!,$select:Boolean!){ensureServicePresets(proxy:$proxy,name:$name,select:$select)}',
        { proxy, name: '分流', select },
      )
      return JSON.parse(data.ensureServicePresets) as {
        routingId: string
        name: string
        createdGroups: string[]
        selected: boolean
        updatedRouting?: boolean
      }
    },
    onSuccess: async () => {
      await Promise.all(
        [QUERY_KEY_GENERAL, QUERY_KEY_GROUP, QUERY_KEY_ROUTING, ['group-services']].map((queryKey) =>
          cache.invalidateQueries({ queryKey }),
        ),
      )
    },
  })
}
