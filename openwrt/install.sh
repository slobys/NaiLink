#!/bin/sh
# SPDX-License-Identifier: MIT
# Native packages only. Never changes firmware, firewall or other proxy services.
set -eu
umask 077

REPOSITORY=slobys/NaiLink
release=openwrt-v0.6.30
asset_dir=''
mode=install
action=install
stage=''
backup=''
stopped=0
menu=auto
explicit_action=0
explicit_release=0
latest_install=0

fail() { printf 'NaiLink: %s\n' "$*" >&2; exit 1; }
usage() {
	cat <<'EOF'
Usage: sh install.sh [--install | --uninstall | --reinstall] [--release TAG] [--asset-dir DIRECTORY] [--check | --dry-run]
  --menu        Open the Chinese install/update, uninstall and check menu.
  --install     Install or upgrade this script's release without the menu.
  --uninstall   Back up data, remove NaiLink packages and clear its config/data.
  --reinstall   Verify new assets, then back up/uninstall and install fresh.
  --check       Read-only compatibility check; no package/repository changes.
  --dry-run     Verify assets; APK also simulates dependencies using cached feeds.
  --asset-dir   Use downloaded release assets (dependencies still use firmware feeds).
With no action flag, a terminal opens the menu; non-interactive callers retain
the direct install behavior. Menu install/update fetches the latest installer
unless --release or --asset-dir is supplied. Uninstall works offline.
EOF
}
show_menu() {
	current='未安装或版本未知'
	if [ -r /usr/share/daed-router/version ]; then
		IFS= read -r current </usr/share/daed-router/version || :
		case "$current" in ''|*[!a-zA-Z0-9._+-]*) current='版本未知' ;; esac
	fi
	while :; do
		printf '\nNaiLink · 奶油互联\n当前版本：%s\n' "$current"
		if [ "$explicit_release" = 1 ] || [ -n "$asset_dir" ]; then
			printf '目标版本：%s\n' "${release#openwrt-v}"
		else
			printf '安装/更新时联网获取最新发布版；卸载可离线执行。\n'
		fi
		printf '\n  1) 安装 / 更新（保留节点和配置）\n  2) 卸载（先备份，再清理插件和配置）\n  3) 检查环境（不修改系统）\n  0) 退出\n\n请选择 [0-3]：'
		if ! IFS= read -r choice; then printf '\n已退出，未执行操作。\n'; exit 0; fi
		case "$choice" in
		1)
			action=install; mode=install
			if [ "$explicit_release" = 0 ] && [ -z "$asset_dir" ]; then latest_install=1; fi
			return ;;
		2)
			printf '\n卸载将停止服务，备份到 /root/daed-backups，再删除插件、节点和配置。\n已有备份和共享依赖将保留。确认卸载？[y/N]：'
			confirm=''
			IFS= read -r confirm || :
			case "$confirm" in y|Y|yes|YES) action=uninstall; mode=install; return ;;
			*) printf '已取消卸载。\n' ;; esac ;;
		3) action=install; mode=check; return ;;
		0|'') printf '已退出，未执行操作。\n'; exit 0 ;;
		*) printf '输入无效，请选择 0、1、2 或 3。\n' ;;
		esac
	done
}
download_https() {
	dl_output=$1; dl_url=$2; dl_timeout=$3; dl_accept=${4:-}
	case "$dl_url" in https://*) ;; *) return 1 ;; esac
	# Some firmware curl builds/resolvers fail while uclient-fetch still works.
	# Never keep a partial response or disable HTTPS certificate verification.
	if command -v curl >/dev/null 2>&1; then
		set -- --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
			--connect-timeout 10 --max-time "$dl_timeout" --user-agent 'NaiLink-installer'
		[ -z "$dl_accept" ] || set -- "$@" --header "$dl_accept"
		if curl "$@" --output "$dl_output" "$dl_url" && [ -s "$dl_output" ]; then return 0; fi
		rm -f "$dl_output"
	fi
	if command -v uclient-fetch >/dev/null 2>&1; then
		set -- -T "$dl_timeout" -U 'NaiLink-installer'
		[ -z "$dl_accept" ] || set -- "$@" "--header=$dl_accept"
		if uclient-fetch "$@" -O "$dl_output" "$dl_url" && [ -s "$dl_output" ]; then return 0; fi
		rm -f "$dl_output"
	fi
	return 1
}
install_latest() {
	stage=$(mktemp -d /tmp/daed-install.XXXXXX) || fail 'Cannot create staging directory.'
	url="https://raw.githubusercontent.com/$REPOSITORY/main/openwrt/install.sh"
	printf '\n正在获取最新安装脚本，现有服务和数据尚未改动…\n'
	if ! command -v curl >/dev/null 2>&1 && ! command -v uclient-fetch >/dev/null 2>&1; then
		fail 'Requires curl or uclient-fetch with working HTTPS certificate verification.'
	fi
	if ! download_https "$stage/install.sh" "$url" 60; then
		printf '主下载入口不可用，正在尝试 GitHub 官方 API 备用入口…\n'
		url="https://api.github.com/repos/$REPOSITORY/contents/openwrt/install.sh?ref=main"
		download_https "$stage/install.sh" "$url" 60 'Accept: application/vnd.github.raw+json' || \
			fail 'Cannot download the latest installer. 请检查路由器自身的 DNS 和 HTTPS 连接（raw.githubusercontent.com / api.github.com）。Existing service/data were not changed.'
	fi
	if [ ! -s "$stage/install.sh" ] || ! sh -n "$stage/install.sh"; then
		fail 'Downloaded installer is empty or invalid.'
	fi
	target=$(awk '/^release=openwrt-v[0-9]+\.[0-9]+\.[0-9]+$/ {sub(/^release=openwrt-v/, ""); print; count++} END {if(count!=1) exit 1}' "$stage/install.sh") || fail 'Downloaded installer has no unique release version.'
	installed_version=''
	if [ -r /usr/share/daed-router/version ]; then
		IFS= read -r installed_version </usr/share/daed-router/version || :
		installed_version=${installed_version%%-*}
	fi
	for minimum in "${release#openwrt-v}" "$installed_version"; do
		if ! awk -v target="$target" -v minimum="$minimum" 'BEGIN {
			if (minimum !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 0;
			split(target, newer, "."); split(minimum, older, ".");
			for (i=1; i<=3; i++) {
				if (newer[i]+0 > older[i]+0) exit 0;
				if (newer[i]+0 < older[i]+0) exit 1;
			}
		}' ; then
			fail 'Downloaded installer is older than this script/installation. Retry after the upstream cache refreshes; existing service/data were not changed.'
		fi
	done
	printf '目标版本：%s，开始安装/更新。\n' "$target"
	sh "$stage/install.sh" --install
}
cleanup() {
	result=$?
	trap - EXIT HUP INT TERM
	[ -z "$stage" ] || rm -rf "$stage"
	if [ "$result" -ne 0 ] && [ "$stopped" = 1 ]; then
		# Package maintainer scripts may have restarted the managed service.
		if [ -x /etc/init.d/daed ]; then /etc/init.d/daed stop >/dev/null 2>&1 || :; fi
		printf 'NaiLink: Operation did not complete. Service was stopped. Backup: %s\n' "${backup:-not created}" >&2
	fi
	exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ "$#" -gt 0 ]; do
	case "$1" in
	--release) [ "$#" -ge 2 ] || fail 'Missing release tag.'; release=$2; explicit_release=1; shift 2 ;;
	--asset-dir) [ "$#" -ge 2 ] || fail 'Missing asset directory.'; asset_dir=$2; shift 2 ;;
	--menu) menu=1; shift ;;
	--install|--uninstall|--reinstall) action=${1#--}; explicit_action=1; shift ;;
	--check) mode=check; explicit_action=1; shift ;;
	--dry-run) mode=dry-run; explicit_action=1; shift ;;
	--help|-h) usage; exit 0 ;;
	*) usage; fail "Unknown option: $1" ;;
	esac
done
[ "$menu" != 1 ] || [ "$explicit_action" = 0 ] || fail '--menu cannot be combined with action/check/dry-run flags.'
if [ "$menu" = auto ]; then
	menu=0
	if [ "$explicit_action" = 0 ] && [ -t 0 ]; then menu=1; fi
fi
[ "$mode" != check ] || [ "$action" = install ] || fail '--check cannot be combined with uninstall/reinstall.'
case "$release" in ''|*[!a-zA-Z0-9._-]*) fail 'Invalid release tag.' ;; esac
[ "$(id -u)" = 0 ] || fail 'Run on the router as root.'
if [ ! -r /etc/openwrt_release ] || ! command -v uci >/dev/null 2>&1; then
	fail 'Requires OpenWrt / ImmortalWrt / iStoreOS.'
fi
[ "$menu" != 1 ] || show_menu
if [ "$latest_install" = 1 ]; then install_latest; exit 0; fi
# Kernel-held lock: it also releases on interruption/SIGKILL. Never unlink the
# file, otherwise a second process could lock a different inode at the same path.
if [ "$mode" = install ]; then
	command -v flock >/dev/null 2>&1 || fail 'Requires the firmware-provided flock utility.'
	mkdir -p /var/lock
	exec 9>/var/lock/daed-install.lock
	flock -n 9 || fail 'Another NaiLink installation/removal is running. Try again after it finishes.'
fi
if [ "$action" != uninstall ]; then
	[ -r /www/luci-static/resources/luci.js ] || fail 'Install the firmware-provided LuCI first.'
	machine=$(uname -m)
	case "$machine" in x86_64|aarch64|armv7|armv7l|mips|mipsel|mipsle) ;;
	*) fail "Unsupported CPU: $machine. This release provides x86_64, ARM64 and experimental ARMv7/MIPS32/MIPSel32 packages; ARMv5/v6 and MIPS64 are not supported." ;; esac
	for tool in jsonfilter sha256sum awk mktemp; do
		command -v "$tool" >/dev/null 2>&1 || fail "Missing tool: $tool"
	done
	[ -r /usr/share/libubox/jshn.sh ] || fail 'Install the firmware-provided jshn package first.'
fi

if command -v apk >/dev/null 2>&1; then
	format=apk
	if [ "$action" != uninstall ]; then
		apk_version=$(apk --version) || fail 'Cannot read apk-tools version.'
		printf '%s\n' "$apk_version" | grep -Eq '^apk-tools v?3\.' || fail 'These APK packages require apk-tools 3.x. Use compatible firmware; no package manager or firmware is replaced.'
		# --print-arch returns APK's compile-time default, not the database's
		# accepted architectures (e.g. aarch64 vs OpenWrt aarch64_generic).
		architectures=''
		if [ -e /etc/apk/arch ]; then
			[ -r /etc/apk/arch ] || fail 'Cannot read /etc/apk/arch.'
			architectures=$(awk 'NF {$1=$1; if (NF!=1 || $1!~/^[a-zA-Z0-9_+-]+$/) exit 1; print $1}' /etc/apk/arch) || fail 'Invalid APK architecture configuration.'
		fi
		# APK itself falls back to its default only when this list is empty.
		if [ -z "$architectures" ]; then
			architectures=$(apk --print-arch) || fail 'Cannot read apk architecture.'
		fi
	fi
elif command -v opkg >/dev/null 2>&1; then
	format=ipk
	architectures=$(opkg print-architecture | awk '$1=="arch" && $2!="all" && $2!="noarch" {print $3, $2}' | sort -rn | awk '{print $2}')
else
	fail 'Neither opkg nor apk is available.'
fi

installed() {
	if [ "$format" = apk ]; then apk info --exists "$1" >/dev/null 2>&1
	else opkg status "$1" 2>/dev/null | grep -q '^Status:.* installed'; fi
}

# Some firmware images keep a stale record in the opkg status database after a
# package was removed (Status: install prefer,user not-installed). opkg then
# prefers that missing version and never falls back to the version the feed
# provides, so the dependency resolution fails with "not available from any
# configured src" and the whole installation aborts. Drop only the shadowing
# "not-installed" record of the named package; installed packages are never
# touched and the original file is kept until the replacement is complete.
purge_stale_opkg_record() {
	name=$1
	status=/usr/lib/opkg/status
	[ -f "$status" ] && [ -w "$status" ] || return 0
	awk -v name="$name" '
	function flush(   i) {
		if (!drop) { for (i = 1; i <= n; i++) print buf[i] }
		n = 0; drop = 0; pkg = 0
	}
	$0 == "" { flush(); print ""; next }
	{
		buf[++n] = $0
		if ($1 == "Package:" && $2 == name) pkg = 1
		if (pkg && $1 == "Status:" && $0 ~ /not-installed/) drop = 1
	}
	END { flush() }
	' "$status" > "$status.tmp" 2>/dev/null || return 0
	[ -s "$status.tmp" ] || { rm -f "$status.tmp"; return 0; }
	if cmp -s "$status.tmp" "$status"; then rm -f "$status.tmp"; return 0; fi
	cat "$status.tmp" > "$status"
	rm -f "$status.tmp"
}

uninstall_daed() {
	# Only package names belonging to NaiLink are eligible; never recurse through
	# reverse dependencies or force removal of another application's packages.
	if [ "$format" = apk ]; then names=$(apk info); else names=$(opkg list-installed | awk '{print $1}'); fi
	remove=''
	for package in $names; do
		case "$package" in luci-i18n-daed-*) remove="$remove $package" ;; esac
	done
	# opkg processes removals in order: remove dependents before their runtime.
	for package in luci-app-daed-router luci-app-daed daed-router daed; do
		if installed "$package"; then remove="$remove $package"; fi
	done
	if [ "$mode" = dry-run ]; then
		printf 'NaiLink: Uninstall dry run; eligible packages:%s. Data would be backed up and cleared.\n' "${remove:- none}"
		return
	fi
	for directory in /etc/daed /root/daed-backups; do
		[ ! -L "$directory" ] || fail "Refusing to clear a symlinked directory: $directory"
	done
	if [ -x /etc/init.d/daed ]; then
		stopped=1
		/etc/init.d/daed stop || fail 'Cannot stop NaiLink before uninstall.'
	elif pidof daed >/dev/null 2>&1; then
		stopped=1
		killall -TERM daed || fail 'Cannot stop unmanaged NaiLink.'
	fi
	waited=0
	while pidof daed >/dev/null 2>&1; do
		[ "$waited" -lt 35 ] || fail 'NaiLink is still running; uninstall and backup were not attempted.'
		sleep 1; waited=$((waited + 1))
	done
	set --
	for path in etc/daed etc/config/daed etc/init.d/daed usr/bin/daed usr/share/daed-router/version; do
		if [ -e "/$path" ] || [ -L "/$path" ]; then set -- "$@" "$path"; fi
	done
	if [ "$#" -gt 0 ]; then
		mkdir -p /root/daed-backups
		chmod 700 /root/daed-backups
		backup="/root/daed-backups/uninstall-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
		tar -czf "$backup" -C / "$@" || fail 'Backup failed; NaiLink was not removed.'
		chmod 600 "$backup"
		tar -tzf "$backup" >/dev/null || fail 'Backup verification failed; NaiLink was not removed.'
	fi
	if [ -n "$remove" ]; then
		if [ "$format" = apk ]; then
			# APK automatically removes orphan dependencies. Keep existing BTF
			# and networking/runtime dependencies in world before deleting NaiLink.
			keep=''
			for package in vmlinux-btf ca-bundle jshn jsonfilter uclient-fetch kmod-sched-core kmod-sched-bpf kmod-veth v2ray-geoip v2ray-geosite luci-base luci-compat rpcd rpcd-mod-luci uhttpd uhttpd-mod-ubus; do
				if installed "$package"; then keep="$keep $package"; fi
			done
			# Each argument is a fixed installed package name, never an option.
			# shellcheck disable=SC2086
			[ -z "$keep" ] || apk add --no-network --no-scripts $keep || fail 'Cannot retain existing runtime dependencies; nothing was uninstalled.'
			# shellcheck disable=SC2086
			apk del $remove || fail 'Package removal failed; backup and remaining data were retained.'
		else
			# shellcheck disable=SC2086
			opkg remove $remove || fail 'Package removal failed; backup and remaining data were retained.'
		fi
		for package in $remove; do
			if installed "$package"; then fail "Package is still installed: $package. Data was retained."; fi
		done
	fi
	[ ! -x /etc/init.d/daed ] || /etc/init.d/daed disable
	# Package scripts must not have restarted NaiLink before data is cleared.
	if pidof daed >/dev/null 2>&1; then fail 'NaiLink restarted during removal; data was retained.'; fi
	# Fixed paths only; never follow data directory symlinks or touch other apps.
	rm -rf /etc/daed
	rm -f /etc/config/daed /etc/config/daed.apk-new /etc/config/daed.opkg-dist /etc/init.d/daed /usr/bin/daed /usr/sbin/daed-router-setup /usr/sbin/nailink
	rm -f /usr/libexec/daed/check /usr/libexec/daed/btf-probe /usr/share/daed-router/version
	rm -f /usr/libexec/daed/luci-proxy /www/cgi-bin/daed-router
	for link in /etc/rc.d/[SK][0-9][0-9]daed; do [ ! -L "$link" ] || rm -f "$link"; done
	/etc/init.d/rpcd reload >/dev/null 2>&1 || :
	printf 'NaiLink: Uninstalled. Configuration/database backup: %s. Existing BTF retained.\n' "${backup:-no existing data}"
	stopped=0
}

if [ "$action" = uninstall ]; then uninstall_daed; exit 0; fi
stage=$(mktemp -d /tmp/daed-install.XXXXXX) || fail 'Cannot create staging directory.'
if [ -n "$asset_dir" ]; then
	[ -d "$asset_dir" ] || fail 'Asset directory does not exist.'
	asset_dir=$(cd "$asset_dir" && pwd -P)
fi

fetch() {
	name=$1
	case "$name" in ''|.*|*[!a-zA-Z0-9._-]*) fail 'Invalid asset filename.' ;; esac
	if [ -n "$asset_dir" ]; then
		cp "$asset_dir/$name" "$stage/$name" || fail "Missing local asset: $name"
	else
		download_https "$stage/$name" "https://github.com/$REPOSITORY/releases/download/$release/$name" 180 || \
			fail "Cannot download $name. Check router DNS, HTTPS access, CA certificates and release assets. Existing service/data were not changed."
	fi
}

# A source checkout can run --check before the first release is published.
source_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
if [ "$mode" = check ] && [ -r "$source_dir/files/daed-check" ]; then
	sh "$source_dir/files/daed-check" --check
	exit $?
fi
fetch manifest.tsv
[ "$(head -n 1 "$stage/manifest.tsv")" = '# daed-openwrt-manifest-v1' ] || fail 'Unknown manifest format.'
awk -F '\t' '!/^#/ && NF && (NF!=6 || $1!~/^(ipk|apk|support)$/ || $3!~/^(daed-router|luci-app-daed-router|check|btf-probe)$/) {bad=1} END {exit bad}' "$stage/manifest.tsv" || fail 'Malformed manifest.'

record() {
	awk -F '\t' -v fmt="$1" -v arch="$2" -v pkg="$3" '$1==fmt && $2==arch && $3==pkg {line=$0; count++} END {if(count!=1) exit 1; print line}' "$stage/manifest.tsv"
}
verify_record() {
	row=$1
	file=$(printf '%s\n' "$row" | cut -f4)
	hash=$(printf '%s\n' "$row" | cut -f5)
	if [ "${#hash}" != 64 ] || ! printf '%s\n' "$hash" | grep -Eq '^[a-f0-9]+$'; then fail 'Invalid checksum.'; fi
	fetch "$file"
	printf '%s  %s\n' "$hash" "$stage/$file" | sha256sum -c - >/dev/null || fail "Checksum mismatch: $file"
}
support=$(record support all check) || fail 'Manifest has no unique compatibility checker.'
[ "$(printf '%s\n' "$support" | cut -f4)" = daed-check ] || fail 'Unexpected checker filename.'
verify_record "$support"
# Linux often reports 'mips' for both byte orders. Select the native package
# architecture first, rather than guessing endianness from uname alone.
runtime=''
selected_arch=''
probe_cpu=''
for architecture in $architectures; do
	case "$machine:$architecture" in
	x86_64:x86_64) candidate_cpu=x86_64 ;;
	aarch64:aarch64|aarch64:aarch64_*) candidate_cpu=arm64 ;;
	armv7:arm_cortex-a*|armv7l:arm_cortex-a*) candidate_cpu=armv7 ;;
	mips:mipsel_*|mipsel:mipsel_*|mipsle:mipsel_*) candidate_cpu=mipsle ;;
	mips:mips_*) candidate_cpu=mips ;;
	*) continue ;;
	esac
	if runtime=$(record "$format" "$architecture" daed-router); then
		selected_arch=$architecture; probe_cpu=$candidate_cpu; break
	fi
done
[ -n "$selected_arch" ] || fail "No $format runtime for CPU $machine and the firmware's package architectures: $architectures"
probe_record=$(record support "$probe_cpu" btf-probe) || fail 'Manifest has no unique BTF parser for this CPU.'
[ "$(printf '%s\n' "$probe_record" | cut -f4)" = "btf-probe-$probe_cpu" ] || fail 'Unexpected BTF parser filename.'
[ "$(printf '%s\n' "$probe_record" | cut -f6)" = "$(printf '%s\n' "$support" | cut -f6)" ] || fail 'BTF parser and checker versions do not match.'
verify_record "$probe_record"
chmod 700 "$stage/btf-probe-$probe_cpu"
DAED_BTF_PROBE="$stage/btf-probe-$probe_cpu" sh "$stage/daed-check" --check || fail 'Compatibility check failed. No packages or network settings were changed.'
[ "$mode" != check ] || exit 0

plugin=$(record "$format" all luci-app-daed-router) || fail 'Manifest has no unique LuCI package.'
version=$(printf '%s\n' "$runtime" | cut -f6)
if [ "$version" != "$(printf '%s\n' "$plugin" | cut -f6)" ] || \
	[ "$version" != "$(printf '%s\n' "$support" | cut -f6)" ]; then fail 'Runtime, LuCI and checker versions do not match.'; fi
verify_record "$runtime"
runtime_file=$(printf '%s\n' "$runtime" | cut -f4)
verify_record "$plugin"
plugin_file=$(printf '%s\n' "$plugin" | cut -f4)
case "$runtime_file:$plugin_file" in *.$format:*.$format) ;; *) fail 'Package extensions do not match the package manager.' ;; esac

managed=0
if [ "$action" = reinstall ]; then
	printf 'NaiLink: Fresh reinstall requested. Existing NaiLink will be backed up, removed and reset.\n'
elif installed daed-router; then
	[ -r /usr/share/daed-router/version ] || fail 'Managed runtime marker is missing; repair the existing installation first.'
	managed=1
elif [ -e /usr/bin/daed ] || [ -e /etc/config/daed ] || [ -e /etc/init.d/daed ] || [ -d /etc/daed ]; then
	fail 'An existing unmanaged NaiLink installation was found. Run again with --reinstall to back it up and migrate; it will not be overwritten.'
fi
if [ "$action" != reinstall ] && { installed daed || installed luci-app-daed; }; then fail 'Another NaiLink package is installed; use --reinstall for a backed-up fresh installation.'; fi
printf 'NaiLink: %s / %s / %s, release %s\n' "$machine" "$format" "$selected_arch" "$version"
case "$probe_cpu" in armv7|mips|mipsle)
	printf 'NaiLink: 32 位版本仅为实验构建；已测常见内核无法加载必需的 BPF 回调。最小内核探针通过后，仍需完整透明代理和长期运行验证。\n' ;;
esac

# Native package managers resolve kmods against the router's own feeds/kernel ABI.
# Never force dependencies or add a feed for a different firmware.
if [ "$mode" != dry-run ]; then
	if [ "$format" = apk ]; then apk update; else opkg update; fi
fi
# APK resolves architecture and dependencies before stopping/uninstalling NaiLink.
# --simulate opens the database read-only and uses existing index caches.
# Do not add --no-network: APK would mask uncached packages in HTTP(S) feeds.
if [ "$format" = apk ]; then
	apk add --simulate --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file" || fail 'Package preflight failed. NaiLink was not stopped or uninstalled. Check firmware feeds/dependencies; dry runs require cached indexes.'
fi
[ "$mode" != dry-run ] || { printf 'NaiLink: Dry run passed; no packages, configuration or services changed.\n'; exit 0; }
if [ "$action" = reinstall ]; then uninstall_daed; stopped=1; fi
was_running=0
was_autostart=0
if [ "$managed" = 1 ]; then
	/etc/init.d/daed enabled >/dev/null 2>&1 && was_autostart=1
	if pidof daed >/dev/null 2>&1; then was_running=1; fi
	/etc/init.d/daed stop || fail 'Cannot stop existing service for a consistent backup.'
	stopped=1
	waited=0
	while pidof daed >/dev/null 2>&1; do
		[ "$waited" -lt 35 ] || fail 'NaiLink is still running; database backup was not attempted.'
		sleep 1; waited=$((waited + 1))
	done
	mkdir -p /root/daed-backups
	chmod 700 /root/daed-backups
	backup="/root/daed-backups/$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
	set -- etc/config/daed usr/share/daed-router/version
	[ ! -d /etc/daed ] || set -- "$@" etc/daed
	tar -czf "$backup" -C / "$@" || fail 'Cannot back up existing data.'
	chmod 600 "$backup"
fi
if [ "$format" = apk ]; then
	# Local release packages are unsigned; this flag is scoped to this transaction.
	apk add --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file"
else
	# Clear shadowing "not-installed" records before dependencies are resolved.
	for dependency in v2ray-geoip v2ray-geosite; do
		purge_stale_opkg_record "$dependency"
	done
	opkg install "$stage/$runtime_file" "$stage/$plugin_file" || fail 'Package installation failed. See the opkg error above; a stale "not-installed" record in /usr/lib/opkg/status can shadow the version the feed provides.'
fi
if ! installed daed-router || ! installed luci-app-daed-router; then fail 'Native packages were not both installed.'; fi
if [ ! -x /usr/bin/daed ] || [ ! -x /usr/libexec/daed/check ] || \
	[ ! -x /usr/libexec/daed/luci-proxy ] || [ ! -x /www/cgi-bin/daed-router ]; then fail 'Installed runtime or LuCI gateway is incomplete.'; fi
for asset in geosite.dat geoip.dat; do
	if [ ! -s "/etc/daed/$asset" ] && [ ! -s "/usr/local/share/daed/$asset" ] && [ ! -s "/usr/share/daed/$asset" ]; then
		# Missing rule data only disables geodata rules. Keep the installation
		# usable instead of discarding an otherwise complete setup.
		printf 'NaiLink: Warning: rule data is unavailable: %s. Geodata rules will not apply. Repair the firmware v2ray-geosite/v2ray-geoip packages and daed-router search-path links.\n' "$asset" >&2
	fi
done
if [ "$managed" = 0 ]; then
	uci set daed.main.enabled=1
	uci commit daed
	/etc/init.d/daed enable
	was_running=1
elif [ "$was_autostart" = 1 ]; then
	/etc/init.d/daed enable
else
	/etc/init.d/daed disable
fi
/etc/init.d/rpcd reload
if [ "$was_running" = 1 ]; then
	/etc/init.d/daed start
	listen=$(/usr/libexec/daed/check --listen) || fail 'Installed listen address is invalid.'
	waited=0
	until /usr/libexec/daed/check | jsonfilter -e '@.running' | grep -q '^true$' && \
		uclient-fetch -T 2 -O /dev/null "http://$listen/" >/dev/null 2>&1; do
		[ "$waited" -lt 20 ] || { /etc/init.d/daed stop; fail 'Management HTTP did not become ready. Check Services > NaiLink > Logs.'; }
		sleep 1; waited=$((waited + 1))
	done
	# Management HTTP does not imply that restored domain routes, listeners and
	# the DNS upstream are ready. Keep the new service running if WAN/DNS is
	# still recovering: a readiness warning must not undo a successful upgrade.
	stopped=0
	waited=0
	ready=0
	phase=unavailable
	while [ "$waited" -lt 20 ]; do
		status=$(uclient-fetch -q -T 2 -O - "http://$listen/health/ready" 2>/dev/null) || status='{}'
		phase=$(printf '%s' "$status" | jsonfilter -e '@.phase' 2>/dev/null) || phase=unavailable
		if [ "$(printf '%s' "$status" | jsonfilter -e '@.ready' 2>/dev/null)" = true ]; then ready=1; break; fi
		sleep 1; waited=$((waited + 1))
	done
	if [ "$ready" = 1 ]; then
		if [ "$phase" = inactive ]; then
			printf 'NaiLink: 管理界面已就绪；尚未启用代理配置。\n'
		else
			printf 'NaiLink: 透明代理监听、DNS 上游初始化及缓存路由恢复已就绪；这不代表每个节点或网站均可访问。\n'
		fi
	else
		case "$phase" in ''|*[!a-z_]* ) phase=unavailable ;; esac
		printf 'NaiLink: 更新已安装，代理仍在恢复（%s）。服务继续运行，节点和配置已保留；请查看运行日志或执行 nailink --check-network。\n' "$phase" >&2
	fi
fi
stopped=0
printf 'NaiLink: Installed. Refresh LuCI and open Services > NaiLink. Management readiness does not prove LAN proxy traffic.\n'
[ -z "$backup" ] || printf 'NaiLink: Configuration backup (contains secrets): %s\n' "$backup"
