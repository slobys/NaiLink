import type { LibraryNode } from '~/utils/node-library'
import { ArrowLeftRight, Check } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useImportNodesMutation, useUpdateNodeMutation } from '~/apis'
import { NodeName } from '~/components/NodeName'
import { Button } from '~/components/ui/button'
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from '~/components/ui/command'
import { Dialog, DialogDescription, DialogTitle } from '~/components/ui/dialog'
import { Input } from '~/components/ui/input'
import {
  ScrollableDialogBody,
  ScrollableDialogContent,
  ScrollableDialogFooter,
  ScrollableDialogHeader,
} from '~/components/ui/scrollable-dialog'
import { cn } from '~/lib/utils'
import { nodeProtocolLabel } from '~/utils/node-protocol-label'

const SEPARATOR = ' -> '
type Hop = 'entry' | 'landing'
interface Choice {
  link: string
  name: string
  source: string
}
type Chain = Record<Hop, Choice | undefined>
const roles = ['entry', 'landing'] as const
const endpoint = (link: string) => link.split('#', 1)[0]

export function isChainProxy(link: string) {
  return link.includes('->')
}

export function isEditableChainProxy(link: string) {
  return link.split('->').length === 2
}

export function ChainProxyDialog({
  nodes,
  editing,
  onClose,
}: {
  nodes: LibraryNode[]
  editing?: { id: string; link: string }
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [activeHop, setActiveHop] = useState<Hop>('entry')
  const [search, setSearch] = useState('')
  const [tag, setTag] = useState('')
  const [error, setError] = useState('')
  const importNodes = useImportNodesMutation()
  const updateNode = useUpdateNodeMutation()
  const saving = importNodes.isPending || updateNode.isPending
  const choices = nodes
    .filter(({ node }) => /^[a-z][a-z0-9+.-]*:\/\//.test(node.link) && !node.link.includes('->'))
    .map(({ node, sources }) => ({
      id: node.id,
      link: node.link,
      name: node.tag || node.name || nodeProtocolLabel(node.protocol),
      protocol: nodeProtocolLabel(node.protocol),
      source: sources.length ? sources.map(({ name }) => name).join(' · ') : t('panel.manual'),
    }))
  const [chain, setChain] = useState<Chain>(() => {
    const [landing, entry] = editing?.link.split('->').map((hop) => hop.trim()) || []
    const savedChoice = (role: Hop, link?: string) =>
      link
        ? choices.find((choice) => choice.link === link) || { link, name: t(`chainProxy.saved.${role}`), source: '' }
        : undefined
    return { entry: savedChoice('entry', entry), landing: savedChoice('landing', landing) }
  })
  const { entry, landing } = chain
  const otherHop = activeHop === 'entry' ? 'landing' : 'entry'
  const excluded = chain[otherHop]?.link
  const link = entry && landing ? landing.link + SEPARATOR + entry.link : ''
  const invalidTag = /[:'\r\n\0]/.test(tag)

  function chooseHop(role: Hop) {
    setActiveHop(role)
    setSearch('')
  }

  async function save() {
    if (saving || !link || invalidTag) return
    setError('')
    try {
      if (editing) {
        await updateNode.mutateAsync({ id: editing.id, newLink: link })
      } else {
        const result = await importNodes.mutateAsync([{ link, tag: tag.trim() || undefined }])
        if (!result.importNodes[0]?.node) {
          setError(t('chainProxy.duplicate'))
          return
        }
      }
      toast.success(t('chainProxy.savedSuccess'))
      onClose()
    } catch {
      setError(t('chainProxy.saveFailed'))
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !saving && onClose()}>
      <ScrollableDialogContent
        size="xl"
        className="node-picker-dialog chain-proxy-dialog"
        showCloseButton={!saving}
        aria-describedby="chain-proxy-description"
      >
        <ScrollableDialogHeader className="px-4 py-3 pr-12 text-left">
          <DialogTitle>{t(editing ? 'chainProxy.edit' : 'chainProxy.add')}</DialogTitle>
          <DialogDescription id="chain-proxy-description">{t('chainProxy.description')}</DialogDescription>
        </ScrollableDialogHeader>
        <ScrollableDialogBody className="node-picker-body space-y-3">
          <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-stretch gap-2">
            {roles.map((role) => (
              <div key={role} className="contents">
                {role === 'landing' && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={saving || !entry || !landing}
                    className="size-11 self-center"
                    aria-label={t('chainProxy.swap')}
                    title={t('chainProxy.swap')}
                    onClick={() => setChain({ entry: landing, landing: entry })}
                  >
                    <ArrowLeftRight aria-hidden="true" className="size-4" />
                  </Button>
                )}
                <button
                  type="button"
                  disabled={saving}
                  aria-pressed={activeHop === role}
                  onClick={() => chooseHop(role)}
                  className={cn(
                    'min-h-11 min-w-0 rounded-lg border px-2.5 py-2 text-left transition-colors',
                    activeHop === role ? 'border-primary bg-primary/10' : 'bg-card hover:bg-accent',
                  )}
                >
                  <span className="block text-xs text-muted-foreground">{t(`chainProxy.${role}`)}</span>
                  <span className="chain-hop-name block text-sm font-medium">
                    {chain[role] ? <NodeName name={chain[role].name} showRegion /> : t('chainProxy.choose')}
                  </span>
                </button>
              </div>
            ))}
          </div>

          <Command label={t(`chainProxy.${activeHop}`)} className="h-auto gap-2 overflow-visible bg-transparent">
            <CommandInput
              value={search}
              onValueChange={setSearch}
              placeholder={t('panel.searchPlaceholder')}
              disabled={saving}
            />
            <CommandList className="max-h-64 scroll-py-2 px-1 py-1 sm:max-h-80">
              <CommandEmpty>{t('chainProxy.noCandidates')}</CommandEmpty>
              {choices.map((choice) => {
                const selected = chain[activeHop]?.link === choice.link
                if (excluded && endpoint(excluded) === endpoint(choice.link)) return null
                return (
                  <CommandItem
                    key={choice.id}
                    value={choice.id}
                    keywords={[choice.name, choice.protocol, choice.source]}
                    disabled={saving}
                    data-name-card
                    data-active={selected}
                    className="subscription-node-tile chain-node-option"
                    onSelect={() => {
                      setChain({ ...chain, [activeHop]: choice })
                      setError('')
                      if (!chain[otherHop]) chooseHop(otherHop)
                    }}
                  >
                    <span className="node-title-line w-full">
                      <NodeName name={choice.name} showRegion />
                      {selected && (
                        <span className="node-active-mark" aria-hidden="true">
                          <Check className="text-current" />
                        </span>
                      )}
                    </span>
                    <span className="chain-node-meta flex w-full min-w-0 items-center gap-1.5">
                      <span className="shrink-0 font-medium">{choice.protocol}</span>
                      <span className="truncate" title={choice.source}>
                        {choice.source}
                      </span>
                    </span>
                  </CommandItem>
                )
              })}
            </CommandList>
          </Command>

          {!editing && (
            <label className="block space-y-1 text-sm">
              <span>{t('chainProxy.name')}</span>
              <Input
                maxLength={80}
                value={tag}
                disabled={saving}
                aria-invalid={invalidTag || undefined}
                placeholder={t('chainProxy.namePlaceholder')}
                onChange={(event) => setTag(event.target.value)}
              />
            </label>
          )}
          <p className="text-xs text-muted-foreground">{t('chainProxy.snapshot')}</p>
          {(error || invalidTag) && (
            <p role="alert" className="text-sm text-destructive">
              {invalidTag ? t('chainProxy.invalidTag') : error}
            </p>
          )}
        </ScrollableDialogBody>
        <ScrollableDialogFooter className="node-picker-footer flex-row justify-end">
          <Button variant="outline" disabled={saving} onClick={onClose}>
            {t('actions.cancel')}
          </Button>
          <Button loading={saving} disabled={saving || !link || invalidTag || link === editing?.link} onClick={save}>
            {t(editing ? 'chainProxy.save' : 'chainProxy.create')}
          </Button>
        </ScrollableDialogFooter>
      </ScrollableDialogContent>
    </Dialog>
  )
}
