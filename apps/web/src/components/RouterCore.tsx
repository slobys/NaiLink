import { useIsMutating, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, Check, ChevronDown, Cpu, Download, LoaderCircle, RefreshCw, ShieldCheck } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '~/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '~/components/ui/dialog'
import { useGQLQueryClient } from '~/contexts'
import { useRouterMaintenance } from '~/hooks/useRouterMaintenance'
import { cn } from '~/lib/utils'
import { coreProblemLabel, maintenanceMessages } from '~/utils/router-maintenance'

const cores = ['dae', 'sing-box', 'mihomo'] as const
type CoreName = (typeof cores)[number]
interface LiveCore {
  mode?: 'tun' | 'tproxy'
  current: string
  runtime: string
  running?: boolean
  memory?: { backend_kib: number; native_kib?: number; total_kib: number; complete: boolean }
}
const descriptions = {
  dae: 'eBPF 接管',
  'sing-box': '多协议支持',
  mihomo: 'Clash 生态',
}
function problemLabel(value: string) {
  return (
    maintenanceMessages[value] ||
    (
      {
        unsupported_global_option: '有尚不能等价转换的全局选项（例如 MPTCP、TLS 分片）。',
        unsupported_dns_option: '当前 DNS 使用了尚未适配的响应规则、固定 TTL 或缓存容量设置。',
        unsupported_group_policy: '当前群组策略尚未适配。',
        unsupported_group_policy_parameter: '当前群组使用了尚未适配的策略参数。',
        invalid_fixed_group: '固定群组暂仅支持一个节点；自动选择群组可包含多个节点。原群组保持不变。',
        unsupported_multi_wan: '当前配置指定了多个 WAN 接口，暂不能等价转换。',
        geodata_missing: '找不到当前配置引用的规则库。',
        missing_lan_interface: '请先配置并应用需要接管的 LAN 接口。',
        core_starting: '当前内核尚未就绪，请恢复服务后再检查。',
        unsupported_dns_fallback: '当前 DNS 回退策略尚未适配。',
      } as Record<string, string>
    )[value] ||
    coreProblemLabel(value)
  )
}

export function RouterCore() {
  const [open, setOpen] = useState(false)
  const [target, setTarget] = useState<CoreName>('dae')
  const [mode, setMode] = useState<'tun' | 'tproxy'>('tproxy')
  const { query, mutation, busy } = useRouterMaintenance()
  const pendingMutations = useIsMutating()
  const core = query.data?.cores
  const client = useQueryClient()
  const gql = useGQLQueryClient()
  const live = useQuery({
    queryKey: ['router-core-live'],
    enabled: open,
    queryFn: async () => {
      const value = await gql.request<{ coreStatus: string }>('{coreStatus}')
      return JSON.parse(value.coreStatus) as LiveCore
    },
    retry: false,
    refetchInterval: open ? (busy ? 2000 : 10000) : false,
  })
  const lastCore = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (!core?.current) return
    const selection = `${core.current}:${core.mode || 'tun'}`
    if (lastCore.current && lastCore.current !== selection) client.invalidateQueries()
    lastCore.current = selection
  }, [client, core?.current, core?.mode])
  const current = cores.includes(core?.current as CoreName) ? (core!.current as CoreName) : undefined
  const plan =
    core?.plan?.target === target && (target === 'dae' || (core.plan.mode || 'tun') === mode) ? core.plan : undefined
  const usingSelection = target === current && (target === 'dae' || mode === (core?.mode || 'tun'))
  const selectedCore =
    target === 'dae'
      ? undefined
      : core?.engines?.[target] ||
        (target === 'sing-box' && core
          ? { installed: core.installed, supported: core.supported, version: core.version }
          : undefined)
  const repairCore = plan?.problems?.includes(
    target === 'mihomo' ? 'mihomo_version_mismatch' : 'singbox_version_mismatch',
  )
  const installed = target === 'dae' || !!selectedCore?.installed
  const job = query.data?.job
  const suffix = target === 'sing-box' ? 'singbox' : target
  const selectionSuffix =
    target === 'dae' ? 'dae' : target === 'sing-box' ? (`singbox_${mode}` as const) : (`mihomo_${mode}` as const)
  const coreJob =
    job?.action.startsWith('core_') &&
    (busy ||
      job.action === `core_prepare_${mode}` ||
      job.action.endsWith(`_${selectionSuffix}`) ||
      job.action.endsWith(`_${suffix}`) ||
      (target === 'sing-box' && job.action === 'core_install_extra'))
      ? job
      : undefined
  const ready = !!plan?.compatible && /^[a-f0-9]{64}$/i.test(plan.revision)
  const blocked =
    busy ||
    pendingMutations > 0 ||
    query.isError ||
    !core ||
    !current ||
    (target !== 'dae' && !selectedCore?.supported) ||
    (open && (live.isPending || live.isError || live.data?.runtime !== 'ready' || live.data?.current !== current))
  const memory = live.data?.runtime === 'ready' && live.data.current === current ? live.data.memory : undefined
  const resident =
    memory?.complete && Number.isFinite(memory.total_kib) && memory.total_kib >= 0
      ? (memory.total_kib / 1024).toFixed(0)
      : undefined
  const coreMessage =
    coreJob?.state === 'error'
      ? maintenanceMessages[coreJob.code || ''] || maintenanceMessages.operation_failed
      : coreJob?.state === 'success'
        ? coreJob.action.startsWith('core_switch_')
          ? `切换完成，当前内核：${current || '读取中'}${current && current !== 'dae' ? (core?.mode === 'tproxy' ? ' · 非 TUN' : ' · TUN') : ''}`
          : coreJob.action === 'core_install_extra'
            ? '扩展协议包已安装，现有节点和配置保留。'
            : coreJob.action.startsWith('core_install_')
              ? '内核已安装，请检查兼容性后再切换。'
              : coreJob.action.startsWith('core_prepare_')
                ? '接管依赖已安装，请重新检查兼容性。'
                : undefined
        : coreJob
          ? '正在处理，请勿断电。关闭窗口后，后台任务仍会继续。'
          : undefined
  const refresh = () => {
    query.refetch()
    live.refetch()
  }
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        disabled={query.isPending}
        aria-busy={query.isPending}
        aria-label={`代理内核 ${current || '未知'}，检查和切换`}
        onClick={() => {
          setTarget(current || 'dae')
          setMode(core?.mode || 'tun')
          setOpen(true)
          query.refetch()
        }}
      >
        <Cpu className="h-4 w-4" aria-hidden="true" />
        内核 {current || '未知'}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          className="flex max-h-[90dvh] flex-col gap-0 overflow-hidden rounded-2xl p-0 sm:max-w-[600px]"
          data-core-dialog
        >
          <DialogHeader className="shrink-0 border-b px-5 py-5 text-left sm:px-6">
            <DialogTitle className="text-xl tracking-tight">代理内核</DialogTitle>
            <DialogDescription className="pr-5 text-sm leading-relaxed">
              选择适合当前节点的内核，检查通过后再切换。
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 space-y-5 overflow-y-auto px-5 py-5 sm:px-6" data-core-dialog-body>
            <section className="rounded-xl bg-muted/45 p-4" aria-label="当前内核运行状态">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm text-muted-foreground">
                  当前内核：<strong className="ml-1 text-base text-foreground">{current || '读取中'}</strong>
                  {current && current !== 'dae' && (
                    <span className="ml-2 text-xs">{core?.engines?.[current]?.version || core?.version}</span>
                  )}
                </p>
                <span
                  className={cn(
                    'inline-flex items-center gap-1.5 text-xs font-medium',
                    live.data?.runtime === 'ready' ? 'text-emerald-700 dark:text-emerald-300' : 'text-muted-foreground',
                  )}
                >
                  <span className="size-1.5 rounded-full bg-current" aria-hidden="true" />
                  {busy && coreJob?.action.startsWith('core_switch_')
                    ? '内核切换中'
                    : live.isPending
                      ? '正在读取'
                      : live.isError
                        ? '状态未确认'
                        : live.data?.runtime === 'ready'
                          ? live.data?.running === false
                            ? '代理未启用'
                            : '运行就绪'
                          : '等待恢复'}
                </span>
              </div>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground" data-core-memory>
                {resident
                  ? `常驻内存约 ${resident} MiB · ${current === 'dae' ? '包含内置内核和管理服务' : '管理服务与当前内核合计'}`
                  : '安装的其他内核不使用时不会运行。'}
              </p>
            </section>
            {current !== 'dae' && (
              <p className="text-xs text-muted-foreground" data-core-current-mode>
                当前接管：{core?.mode === 'tproxy' ? '非 TUN · TPROXY' : 'TUN'}
              </p>
            )}
            <section aria-label="选择目标内核">
              <div className="mb-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>选择内核</span>
                <span>每次仅运行一个代理内核</span>
              </div>
              <div
                className="grid grid-cols-3 gap-1 rounded-xl border bg-muted/40 p-1"
                role="group"
                aria-label="目标内核"
              >
                {cores.map((name) => {
                  const optional =
                    name === 'dae'
                      ? undefined
                      : core?.engines?.[name] ||
                        (name === 'sing-box' && core
                          ? { installed: core.installed, supported: core.supported, version: core.version }
                          : undefined)
                  const unsupported = name !== 'dae' && optional?.supported === false
                  const selected = target === name
                  return (
                    <button
                      key={name}
                      type="button"
                      aria-label={name}
                      aria-pressed={selected}
                      aria-describedby={selected ? 'core-selection-detail' : undefined}
                      title={unsupported ? `${name}：当前架构不支持` : undefined}
                      disabled={busy || unsupported}
                      className={cn(
                        'flex h-11 min-w-0 items-center justify-center gap-0.5 rounded-lg px-0.5 text-[11px] font-semibold transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transition-none sm:gap-1.5 sm:px-1 sm:text-sm',
                        selected
                          ? 'bg-primary text-primary-foreground shadow-sm hover:bg-primary/95'
                          : 'text-muted-foreground hover:bg-background hover:text-foreground',
                      )}
                      onClick={() => setTarget(name)}
                      data-core-option={name}
                    >
                      {selected && <Check className="size-2.5 shrink-0 sm:size-3.5" aria-hidden="true" />}
                      <span className="whitespace-nowrap">{name}</span>
                      {unsupported && <span className="sr-only">，当前架构不支持</span>}
                    </button>
                  )
                })}
              </div>
              <p
                id="core-selection-detail"
                className="mt-2 text-xs leading-relaxed text-muted-foreground"
                aria-live="polite"
                data-core-selection
              >
                {target} · {descriptions[target]} ·{' '}
                {usingSelection
                  ? '正在使用'
                  : target !== 'dae' && selectedCore?.supported === false
                    ? '当前架构不支持'
                    : installed
                      ? '已选择，待检查'
                      : '需先下载安装'}
              </p>
            </section>
            {target !== 'dae' && (
              <section aria-label="接管方式" className="space-y-2">
                <div className="flex items-center justify-between text-xs text-muted-foreground">
                  <span>接管方式</span>
                  <span>设备无需单独设置代理</span>
                </div>
                <div
                  role="group"
                  aria-label="接管方式"
                  className="grid grid-cols-2 gap-1 rounded-xl border bg-muted/40 p-1"
                >
                  {(['tproxy', 'tun'] as const).map((value) => (
                    <button
                      key={value}
                      type="button"
                      data-core-mode={value}
                      aria-pressed={mode === value}
                      disabled={busy}
                      onClick={() => setMode(value)}
                      className={cn(
                        'flex min-h-11 items-center justify-center gap-1 rounded-lg px-2 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50',
                        mode === value
                          ? 'bg-primary text-primary-foreground shadow-sm'
                          : 'text-muted-foreground hover:bg-background hover:text-foreground',
                      )}
                    >
                      {mode === value && <Check className="size-3 shrink-0" aria-hidden="true" />}
                      {value === 'tproxy' ? '非 TUN · TPROXY' : 'TUN · 备用'}
                    </button>
                  ))}
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {mode === 'tproxy'
                    ? '通过防火墙接管 TCP / UDP，无需虚拟网卡；需固件支持 TPROXY。'
                    : '通过虚拟网卡接管。非 TUN 模式不兼容时可切回此模式。'}
                </p>
              </section>
            )}
            {target !== 'dae' && (!installed || repairCore) && (
              <section className="space-y-2 text-sm" aria-label="安装可选内核">
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {target} · {selectedCore?.version || '—'}，安装前请预留 180 MiB 可用存储。下载失败不会停止当前代理。
                </p>
                <Button
                  variant="outline"
                  className="min-h-11 w-full"
                  disabled={blocked || current === target}
                  onClick={() => mutation.mutate(target === 'mihomo' ? 'core_install_mihomo' : 'core_install_singbox')}
                >
                  <Download className="size-4" aria-hidden="true" />
                  {repairCore ? `重新安装 ${target}` : `下载并安装 ${target}`}
                </Button>
              </section>
            )}
            {plan && (
              <section className="space-y-2 text-sm" aria-label="内核兼容性报告" aria-live="polite">
                <p className={cn('flex items-center gap-2 font-medium', !plan.compatible && 'text-destructive')}>
                  <ShieldCheck className="size-4 shrink-0" aria-hidden="true" />
                  {plan.compatible ? '兼容性检查通过' : '暂不能切换'}
                  <span className="text-xs font-normal text-muted-foreground">
                    {plan.nodes} 个节点 · {plan.groups} 个群组
                  </span>
                </p>
                {!!plan.problems?.length && (
                  <ul className="space-y-1 break-words rounded-lg bg-destructive/5 p-3 text-xs leading-relaxed text-destructive">
                    {plan.problems.map((value, index) => (
                      <li key={index}>{problemLabel(value)}</li>
                    ))}
                  </ul>
                )}
                {target !== 'dae' &&
                  plan.problems.some(
                    (value) =>
                      value === 'tproxy_environment_unsupported' ||
                      value === 'tproxy_bridge_unsupported' ||
                      value === 'core_environment_unsupported' ||
                      value === 'core_tun_unavailable',
                  ) && (
                    <Button
                      variant="outline"
                      className="min-h-11 w-full"
                      disabled={blocked}
                      onClick={() => mutation.mutate(`core_prepare_${mode}`)}
                    >
                      安装接管依赖
                    </Button>
                  )}
                {!!plan.warnings?.length && (
                  <details className="rounded-lg border px-3 py-2 text-xs" data-core-behavior>
                    <summary className="cursor-pointer py-1 font-medium">
                      行为差异与注意事项（{plan.warnings.length}）
                    </summary>
                    <ul className="mt-2 list-disc space-y-2 pl-4 leading-relaxed text-muted-foreground">
                      {plan.warnings.map((value, index) => (
                        <li key={index}>{value}</li>
                      ))}
                    </ul>
                  </details>
                )}
              </section>
            )}
            <details className="text-xs" data-core-details>
              <summary className="flex min-h-8 cursor-pointer list-none items-center gap-1 text-muted-foreground">
                安装、资源与切换说明
                <ChevronDown className="size-3.5" aria-hidden="true" />
              </summary>
              <div className="mt-2 space-y-3 leading-relaxed text-muted-foreground">
                <p>
                  保留节点、订阅、群组和未应用的修改，仅切换当前运行配置。切换会短暂断连，失败自动尝试恢复原内核，请勿断电。
                </p>
                <p>
                  dae 使用 eBPF；sing-box、mihomo 可选非 TUN（TPROXY）或 TUN，目前支持
                  x86_64、ARM64。安装程序占用存储空间，不等于同时占用运行内存。
                </p>
                <p>
                  常驻内存来自管理服务和当前内核的
                  RSS，共享页可能重复计数，合计为近似值，不包含系统可回收文件缓存。节点测试的临时进程可能另占内存，任务结束后释放。Tor
                  运行时暂不显示不完整的合计。
                </p>
                {target === 'sing-box' && (
                  <section className="space-y-2 border-t pt-3" aria-label="扩展协议组件">
                    <p className="font-medium text-foreground">Naive · WireGuard · Tor</p>
                    <p>
                      {core?.extra ? `扩展协议包已安装 · ${core.extra_version}` : '扩展协议包尚未安装'}。仅 x86_64 /
                      ARM64，下载约 30 MiB，预留 400 MiB 存储。不使用时不启动 Tor；Tor 仅支持
                      TCP，首次引导可能需要数分钟。
                    </p>
                    {!core?.extra && (
                      <Button
                        variant="outline"
                        className="min-h-11 w-full whitespace-normal"
                        disabled={blocked || current === 'sing-box'}
                        onClick={() => mutation.mutate('core_install_extra')}
                      >
                        {current === 'sing-box' ? '请先切回其他内核再安装扩展包' : '按需安装扩展协议包'}
                      </Button>
                    )}
                  </section>
                )}
              </div>
            </details>
            {core?.code && core.code !== coreJob?.code && (
              <p role="status" className="text-xs leading-relaxed text-muted-foreground">
                {maintenanceMessages[core.code] || coreProblemLabel(core.code)}
              </p>
            )}
            {coreMessage && (
              <div
                role={coreJob?.state === 'error' ? 'alert' : 'status'}
                className={cn(
                  'flex items-start gap-2 rounded-lg p-3 text-xs leading-relaxed',
                  coreJob?.state === 'error' ? 'bg-destructive/5 text-destructive' : 'bg-muted/50',
                )}
              >
                {busy && (
                  <LoaderCircle
                    className="mt-0.5 size-4 shrink-0 animate-spin motion-reduce:animate-none"
                    aria-hidden="true"
                  />
                )}
                {coreMessage}
              </div>
            )}
            {(query.isError || mutation.isError || (!busy && live.isError)) && (
              <p role="alert" className="break-words text-xs text-destructive">
                {query.error?.message || mutation.error?.message || '无法确认当前内核运行状态，请重新读取状态。'}
              </p>
            )}
            {!busy && pendingMutations > 0 && (
              <p role="status" className="text-xs text-muted-foreground">
                请等待节点测试或其他保存操作完成后再切换。
              </p>
            )}
          </div>
          <div className="shrink-0 space-y-3 border-t bg-background px-5 py-4 sm:px-6">
            <p
              className="flex items-center justify-between gap-1 text-[11px] text-muted-foreground sm:justify-start sm:gap-2 sm:text-xs"
              aria-label="切换步骤"
            >
              <span>选择内核</span>
              <ArrowRight className="size-3" aria-hidden="true" />
              <span className={ready ? 'text-foreground' : undefined}>检查兼容性</span>
              <ArrowRight className="size-3" aria-hidden="true" />
              <span>确认切换</span>
            </p>
            <div className="grid grid-cols-[44px_minmax(0,1fr)_minmax(0,1fr)] gap-2">
              <Button
                variant="outline"
                className="min-h-11 px-0"
                aria-label="重新读取状态"
                title="重新读取状态"
                disabled={query.isFetching || live.isFetching}
                onClick={refresh}
              >
                <RefreshCw
                  className={cn(
                    'size-4',
                    (query.isFetching || live.isFetching) && 'animate-spin motion-reduce:animate-none',
                  )}
                  aria-hidden="true"
                />
              </Button>
              <Button
                variant="outline"
                className="h-auto min-h-11 min-w-0 whitespace-normal px-2 py-2 text-xs sm:text-sm"
                disabled={blocked || !installed}
                onClick={() => mutation.mutate(`core_check_${selectionSuffix}`)}
              >
                检查兼容性
              </Button>
              <Button
                className="h-auto min-h-11 min-w-0 whitespace-normal px-2 py-2 text-xs sm:text-sm"
                disabled={blocked || !ready || usingSelection}
                onClick={() => mutation.mutate(`core_switch_${selectionSuffix}`)}
              >
                {usingSelection
                  ? '正在使用'
                  : target === current
                    ? mode === 'tproxy'
                      ? '启用非 TUN'
                      : '启用 TUN'
                    : `切换到 ${target}`}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}
