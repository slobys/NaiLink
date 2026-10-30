export type MaintenanceAction =
  | 'check'
  | 'update'
  | 'geodata'
  | 'core_check_dae'
  | 'core_check_singbox'
  | 'core_check_mihomo'
  | 'core_install_singbox'
  | 'core_install_mihomo'
  | 'core_install_extra'
  | 'core_switch_dae'
  | 'core_switch_singbox'
  | 'core_switch_mihomo'
  | 'core_prepare_tproxy'
  | 'core_prepare_tun'
  | `core_${'check' | 'switch'}_${'singbox' | 'mihomo'}_${'tun' | 'tproxy'}`
export interface CorePlan {
  mode?: 'tun' | 'tproxy'
  target: 'dae' | 'sing-box' | 'mihomo'
  revision: string
  compatible: boolean
  installed: boolean
  warnings: string[]
  problems: string[]
  nodes: number
  groups: number
}
export interface CoreStatus {
  mode?: 'tun' | 'tproxy'
  engines?: Record<string, { installed: boolean; supported: boolean; version: string }>
  current: string
  phase: string
  code?: string
  installed: boolean
  supported: boolean
  version: string
  extra?: boolean
  extra_version?: string
  plan?: CorePlan
}
export interface MaintenanceJob {
  id: string
  action: MaintenanceAction
  state: 'queued' | 'running' | 'success' | 'error'
  phase: string
  code?: string
  target?: string
  started: number
  updated: number
}
export interface MaintenanceStatus {
  success: boolean
  code?: string
  current: string
  latest?: { version: string; tag: string; checked: number }
  geodata?: { version: string; tag: string; checked: number }
  geodata_check?: { version?: string; tag?: string; checked: number; code?: string }
  geodata_custom?: boolean
  job?: MaintenanceJob
  cores?: CoreStatus
}

export function coreTransitionActive(status?: MaintenanceStatus) {
  return (
    status?.cores?.phase === 'switching' ||
    (!!status?.job && jobActive(status.job) && status.job.action.startsWith('core_switch_'))
  )
}

export function geodataCheckExpired(check?: MaintenanceStatus['geodata_check'], now = Date.now()) {
  const age = now / 1000 - (check?.checked || 0)
  return !check || !Number.isFinite(check.checked) || check.checked <= 0 || age < -60 || age >= 300
}

export function geodataVersionState(
  status?: MaintenanceStatus,
  checking = false,
  requestFailed = false,
  now = Date.now(),
) {
  if (status?.geodata_custom) return 'custom'
  if (checking) return 'checking'
  if (requestFailed || status?.geodata_check?.code) return 'error'
  const check = status?.geodata_check
  if (geodataCheckExpired(check, now) || !check?.tag || check.version !== check.tag) return 'unknown'
  const current = status?.geodata?.tag
  if (!current) return 'unmanaged'
  if (current === check.tag) return 'latest'
  if (/^\d{12}$/.test(current) && /^\d{12}$/.test(check.tag) && current > check.tag) return 'ahead'
  return 'available'
}

export const maintenanceMessages: Record<string, string> = {
  invalid_core_mode: '接管模式无效，请重新选择。',
  core_tun_unavailable: 'TUN 设备不可用。可安装固件匹配的 kmod-tun，或选择非 TUN；现有代理保留。',
  tproxy_fw4_required: '非 TUN 接管需要 firewall4；当前旧式防火墙不会被自动改造，请使用兼容固件或原有模式。',
  tproxy_environment_unsupported: '非 TUN 接管检查未通过。请安装固件匹配的 kmod-nft-tproxy，或选择 TUN；现有代理保留。',
  tproxy_bridge_unsupported:
    'LAN 桥接过滤需要 nftables broute 支持。请准备非 TUN 依赖（含 kmod-nft-bridge），仍不支持时选择 TUN；保留 Docker 桥接设置。',
  tproxy_bridge_rpf: '当前 LAN 桥接端口的反向路径检查与非 TUN 模式不兼容，请选择 TUN；保留系统安全设置。',
  core_dns_session_failed: '处理旧 DNS 连接跟踪失败，请查看日志后重试；节点与配置保留。',
  tproxy_bridge_vlan_unsupported: '当前带 VLAN 的 LAN 桥接过滤拓扑暂不支持非 TUN，请选择 TUN；原有配置保留。',
  tproxy_tor_unsupported: '当前配置包含 Tor，非 TUN 模式暂不接管 Tor 子进程，请选择 TUN。节点与配置保留。',
  busy: '已有维护或安装任务正在执行，请稍后再试。',
  check_required: '请先检查最新版本，再开始更新。',
  already_latest: '当前已是最新版本。',
  permission_denied: '当前账户没有维护权限，请使用管理员账户重新登录。',
  worker_unavailable: '维护任务未能启动，请检查插件安装和 procd 服务。',
  download_failed: '下载失败，请检查路由器自身的 DNS、HTTPS 连接和系统时间。原有数据保留。',
  download_dns:
    '路由器无法解析 GitHub 下载地址。请检查路由器自身的 DNS；电脑能上网不代表路由器能下载。原有服务和数据保留。',
  download_tls:
    'HTTPS 证书或 TLS 握手失败。请检查路由器时间、CA 证书和网络拦截；不要关闭证书验证。原有服务和数据保留。',
  download_timeout:
    '下载连接或传输超时，已尝试备用下载方式。请稍后重试；仍失败可按在线文档手动更新，原有服务和数据保留。',
  download_connection: '下载连接中断，重试后仍未完成。请检查路由器到 GitHub 下载服务器的连接。原有服务和数据保留。',
  download_rate_limited: 'GitHub 拒绝了下载请求或触发访问限流，请稍后再试。原有服务和数据保留。',
  download_asset_missing: '目标版本的下载文件不存在或尚未发布完整，请重新检查更新。原有服务和数据保留。',
  download_http: 'GitHub 下载服务器返回异常响应，请稍后重试或按在线文档手动更新。原有服务和数据保留。',
  download_redirect: '下载跳转到未授权地址，已停止更新。请检查网络拦截；原有服务和数据保留。',
  download_invalid: '下载内容为空、过大或与目标发布不一致，已停止更新。原有服务和数据保留。',
  invalid_release: '没有找到完整、可用的发布版本，请稍后重试。',
  checksum_failed: '文件校验失败，已停止更新。',
  invalid_installer: '安装脚本版本或格式异常，已停止更新。',
  install_failed: '安装未完成，节点和配置备份已保留，请查看运行日志并按安装文档恢复。',
  update_rolled_back: '更新未完成，已恢复原安装及服务状态；节点和配置备份保留。请检查日志后再试。',
  install_missing: '现有插件已被移除，在线更新已停止，没有重新安装。',
  package_feed_failed: '固件软件源刷新失败，插件尚未停止。请检查固件自身的软件源和网络后重试，现有数据保留。',
  package_preflight_failed: '软件包依赖检查未通过，插件尚未停止。请检查固件软件源及依赖，现有数据保留。',
  preparation_timeout: '安装准备耗时过长，已在停服前退出。请稍后重试或按文档手动更新，现有服务和数据保留。',
  version_changed: '安装期间版本已发生变化，请重新检查更新。',
  unsupported: '当前固件、架构或内核未通过兼容性检查，未强制安装。',
  no_space: '可用存储空间不足，请清理临时文件或备份后重试。',
  backup_failed: '配置备份失败，已停止更新。',
  service_not_ready: '新版本管理服务未就绪，请查看运行日志；现有节点和备份保留。',
  custom_geodata: '检测到自定义 GeoIP/GeoSite，已保留原文件。本入口仅更新 NaiLink 独立管理的规则库。',
  invalid_geodata: '规则库格式或内容校验失败，未替换旧版。',
  configuration_modified: '当前有未应用配置，请先应用或恢复这些配置，再更新规则库。',
  apply_failed: '新规则库加载失败，已恢复旧版。',
  apply_unconfirmed: '规则库加载结果尚未确认，两份数据均已保留。请稍后查看状态或运行日志，勿重复更新。',
  rollback_failed: '自动回退未完成，服务已停止，原配置备份保留。请按安装文档修复，避免卸载清除数据。',
  backend_unavailable: '无法读取或重新加载代理配置，请确认服务正常后重试。',
  unsafe_state: '维护目录状态异常，已停止操作以保护现有文件。',
  state_failed: '无法保存维护状态，请检查存储空间和文件权限。',
  interrupted: '维护任务被中断，请重新检查当前版本与运行日志后再重试。',
  operation_failed: '维护操作未完成，请查看运行日志后重试。',
  extra_not_installed: '该节点需要扩展协议包，请在代理内核窗口按需安装。',
  extra_dependency_failed: '扩展组件依赖检查失败，原组件、节点和配置保留。',
  extra_rollback_failed: '组件指针恢复未确认，旧版文件和配置保留，请检查日志。',
  core_incompatible: '兼容性检查未通过，原内核和配置保留。请查看检查报告。',
  core_starting: '内核正在启动或恢复，请稍后重新读取状态；原配置保留。',
  core_configuration_changed: '运行配置或规则库已变化，请重新检查兼容性再切换。',
  mihomo_not_installed: '请先按需安装 mihomo 内核，当前内核和配置保留。',
  mihomo_version_mismatch: 'mihomo 版本校验未通过，请切换到其他内核后重新安装。',
  mihomo_configuration_invalid: 'mihomo 配置检查未通过，当前内核继续运行。请查看兼容性报告。',
  mihomo_protocol_unsupported: '此协议暂未适配 mihomo，节点保留；可选择其他内核。',
  mihomo_option_unsupported: '节点包含尚不能等价转换为 mihomo 的参数，原节点保留。',
  mihomo_rule_unsupported: '当前路由匹配不能安全转换为 mihomo，原内核和规则保留。',
  mihomo_dns_unsupported: '当前 DNS 规则不能安全转换为 mihomo，原内核和 DNS 配置保留。',
  singbox_not_installed: '请先安装可选 sing-box 内核。',
  singbox_version_mismatch: 'sing-box 版本校验未通过，请重新安装管理的固定版本。',
  singbox_configuration_invalid: 'sing-box 配置校验未通过，原内核仍在运行。',
  core_configuration_check_timeout: '内核配置检查超时，原内核仍在运行。规则库较大时请稍后重试，并检查 CPU 和内存状态。',
  core_environment_unsupported: '系统能力检查未通过，请查看高级设置中的兼容性信息。',
  core_network_conflict: '检测到已有 TUN、路由标记或其他代理接管，未强制切换。',
  core_network_check_failed: '透明代理的防火墙或策略路由检查失败，请查看恢复结果与运行日志；原配置保留。',
  core_network_ownership_failed: '接管资源与 NaiLink 的记录不一致，已停止自动修改以保护其他服务；原配置保留。',
  core_api_invalid: '内核管理接口返回异常数据，请重新读取状态并查看运行日志；原配置保留。',
  core_recovery_snapshot_missing: '缺少内核恢复快照，已停止自动恢复；节点和配置保留，请查看日志后修复服务。',
  core_recovery_snapshot_invalid: '内核恢复快照无法读取，已停止自动恢复；节点和配置保留，请查看日志后修复服务。',
  core_rolled_back: '目标内核未能就绪，已恢复原内核、节点和运行配置。',
  core_rollback_failed: '原内核自动恢复未完成，配置和恢复快照已保留。请重新启动服务并查看日志，避免卸载。',
  core_cleanup_failed: '目标内核资源清理未确认，为避免双重接管已停止切换。请重启服务并检查日志。',
  core_stop_failed: '原内核尚未确认停止，未启动目标内核。请重启服务后再试。',
  core_state_failed: '无法保存内核状态，请检查剩余存储和文件权限。',
  core_snapshot_failed: '无法保存当前运行配置，未停止原内核。',
  core_start_failed: '目标内核未能启动，正在检查原内核恢复结果。',
  core_ports_unavailable: '内核监听端口冲突或不足，请稍后重试；现有节点和配置保留。',
  core_not_ready: '目标内核未通过就绪检查，请查看恢复结果和日志。',
  core_request_failed: '内核操作未获后台确认，请重新读取状态，并检查运行日志；现有配置保留。',
  core_dns_failed: '目标内核 DNS 查询未通过检查，正在恢复原内核。',
  core_restarted: '内核意外退出后已自动重启，并通过连接检查。',
  core_recovery_failed: '内核自动恢复失败，管理面板和原有配置仍保留。请检查日志后重启服务。',
  core_startup_rolled_back: '所选内核启动失败，已恢复原可用内核和运行配置。',
  core_interrupted_recovered: '上次切换被中断，已恢复原内核和运行配置。',
  core_reload_rolled_back: '新配置未能就绪，已恢复原运行配置。',
  core_connectivity_failed: '目标内核的实际连接检查失败，已尝试恢复原内核。',
  core_dependency_failed: '无法安装固件匹配的 TUN 依赖，原内核未停止；不会替换系统内核或软件源。',
  nftables_unavailable: '需要可用的 nftables / fw4，未改动其他代理规则。',
  core_exited: '内核进程已退出，请重新启动服务并查看日志。',
  core_not_running: '当前内核未就绪，请先恢复服务。',
}

export function jobActive(job?: MaintenanceJob) {
  return job?.state === 'queued' || job?.state === 'running'
}

export function updateTargetInstalled(job: MaintenanceJob | undefined, current: string) {
  if (job?.action !== 'update' || job.state !== 'error' || !/^openwrt-v\d+\.\d+\.\d+$/.test(job.target || ''))
    return false
  const target = job.target!.slice('openwrt-v'.length)
  const installed = current.split('-')[0]
  return installed === target || newerVersion(installed, target)
}

export function releaseCheckExpired(latest: MaintenanceStatus['latest'], now = Date.now()) {
  return (
    !latest ||
    !Number.isFinite(latest.checked) ||
    now - latest.checked * 1000 > 300000 ||
    latest.checked * 1000 > now + 60000
  )
}

export function coreProblemLabel(value: string) {
  if (maintenanceMessages[value]) return maintenanceMessages[value]
  if (/unsupported_node_option/.test(value)) {
    const node = value.match(/^节点\s*\d+/)?.[0]
    return `${node ? `${node}：` : ''}节点包含尚未适配的附加参数，请重新检查兼容性以读取完整中文报告。`
  }
  if (/[\u3400-\u9FFF]/.test(value)) return value
  const code = /^[a-z][a-z0-9_]{0,63}$/.test(value) ? `（诊断码：${value}）` : ''
  return `操作未完成${code}，请查看运行日志；原配置保留。`
}

export class MaintenanceAccessError extends Error {}
export function maintenanceAccessExpired(error: unknown) {
  return error instanceof MaintenanceAccessError
}
export function newerVersion(next: string, current: string) {
  const a = next.split('.').map(Number)
  const b = current.split('-')[0].split('.').map(Number)
  if (a.length !== 3 || b.length !== 3 || [...a, ...b].some((value) => !Number.isFinite(value))) return false
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i]
  }
  return false
}

async function maintenanceRPC(method: 'maintenance' | 'maintain', action?: string): Promise<MaintenanceStatus> {
  const host = window.parent as Window & {
    L?: {
      require: (
        name: string,
      ) => Promise<{ declare: (spec: object) => (...args: string[]) => Promise<MaintenanceStatus> }>
    }
  }
  if (host === window || !host.L?.require) throw new Error('请从路由器后台进入维护功能。')
  const rpc = await host.L.require('rpc')
  let result: MaintenanceStatus
  try {
    result = await rpc.declare({
      object: 'daed-router',
      method,
      params: method === 'maintain' ? ['action'] : action ? ['check_geodata'] : [],
      expect: {},
      raise: true,
      reject: true,
    })(...(action ? [action] : []))
  } catch (error) {
    const failure = error as { code?: number; name?: string; message?: string }
    if (
      failure?.code === 6 ||
      failure?.name === 'SessionError' ||
      (failure?.name === 'RPCError' &&
        /\b(?:ubus code 6|error -32002|HTTP error (?:401|403))\b/.test(failure.message || ''))
    )
      throw new MaintenanceAccessError('LuCI 登录已失效或没有维护权限，请重新登录后台。')
    throw new Error('维护接口暂时不可用，请稍后重试；持续失败时重新登录后台。')
  }
  if (!result || typeof result.success !== 'boolean') throw new Error('维护接口不可用，请更新插件并重新登录后台。')
  if (!result.success) throw new Error(maintenanceMessages[result.code || ''] || maintenanceMessages.operation_failed)
  return result
}
export const readMaintenance = () => maintenanceRPC('maintenance')
export const checkGeodataVersion = async () => {
  const result = await maintenanceRPC('maintenance', '1')
  if (!result.geodata_check) throw new Error('规则版本检查接口尚未就绪，请刷新后台后重试。')
  return result
}
export const startMaintenance = (action: MaintenanceAction) => maintenanceRPC('maintain', action)
