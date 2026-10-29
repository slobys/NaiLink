#!/bin/sh
# SPDX-License-Identifier: MIT
# Native packages only. Never changes firmware, firewall or other proxy services.
# The rollback downloader intentionally changes these globals in a subshell.
# shellcheck disable=SC2030,SC2031
set -eu
umask 077

REPOSITORY=slobys/NaiLink
release=openwrt-v0.6.57
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
update_only=0
recoverable=0
packages_changed=0
rollback_ready=0
backup_verified=0
was_running=0
was_autostart=0
prepare_deadline=''
prepare_timeout=900

fail() { printf 'NaiLink: %s\n' "$*" >&2; exit 1; }
usage() {
	cat <<'EOF'
Usage: sh install.sh [--install | --uninstall | --reinstall] [--release TAG] [--asset-dir DIRECTORY] [--check | --dry-run]
  --menu        Open the Chinese install/update, uninstall and check menu.
  --install     Install or upgrade this script's release without the menu.
  --update-only Require an existing managed install; never install fresh.
  --prepare-timeout SECONDS  Online preparation budget, from 1 to 900 seconds.
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
download_clock() {
	# Linux uptime is monotonic even while NTP corrects the wall clock.
	if [ -r /proc/uptime ]; then
		IFS= read -r dl_uptime < /proc/uptime
		printf '%s\n' "${dl_uptime%%.*}"
	else
		date +%s
	fi
}
download_remaining() {
	dl_remaining=$((dl_deadline - $(download_clock)))
	[ "$dl_remaining" -le "$dl_timeout" ] || dl_remaining=$dl_timeout
	[ "$dl_remaining" -gt 0 ] || { dl_error=download_timeout; return 1; }
}
download_https() {
	dl_output=$1; dl_url=$2; dl_timeout=$3; dl_accept=${4:-}
	# The whole file shares a deadline, including client/family/origin fallback.
	# Giving each retry a fresh ten minutes can outlive the maintenance worker.
	dl_deadline=${5:-$(($(download_clock) + dl_timeout))}
	dl_error=download_failed
	case "$dl_url" in https://*) ;; *) return 1 ;; esac
	# Some firmware curl builds/resolvers fail while uclient-fetch still works.
	# Never keep a partial response or disable HTTPS certificate verification.
	if command -v curl >/dev/null 2>&1; then
		set -- --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
			--connect-timeout 10 --user-agent 'NaiLink-installer'
		[ -z "$dl_accept" ] || set -- "$@" --header "$dl_accept"
		dl_exit=0
		download_remaining || return 1
		curl "$@" --max-time "$dl_remaining" --output "$dl_output" "$dl_url" || dl_exit=$?
		if [ "$dl_exit" = 0 ] && [ -s "$dl_output" ]; then return 0; fi
		rm -f "$dl_output"
		case "$dl_exit" in
			6) dl_error=download_dns ;;
			28) dl_error=download_timeout ;;
			35|51|58|60|77|83|90|91) dl_error=download_tls ;;
			7|18|52|55|56) dl_error=download_connection ;;
			22) dl_error=download_http ;;
		esac
		case "$dl_exit" in 6|7|18|28|35|52|55|56)
			download_remaining || return 1
			if curl -4 "$@" --max-time "$dl_remaining" --output "$dl_output" "$dl_url" && [ -s "$dl_output" ]; then return 0; fi
			rm -f "$dl_output" ;;
		esac
	fi
	if command -v uclient-fetch >/dev/null 2>&1; then
		set -- -U 'NaiLink-installer'
		[ -z "$dl_accept" ] || set -- "$@" "--header=$dl_accept"
		download_remaining || return 1
		if uclient-fetch "$@" -T "$dl_remaining" -O "$dl_output" "$dl_url" && [ -s "$dl_output" ]; then return 0; fi
		rm -f "$dl_output"
		download_remaining || return 1
		if uclient-fetch -4 "$@" -T "$dl_remaining" -O "$dl_output" "$dl_url" && [ -s "$dl_output" ]; then return 0; fi
		rm -f "$dl_output"
	fi
	return 1
}

# Alternate GitHub-owned asset route; keep the exact release, checksums and
# certificate verification. Cache only private metadata for this transaction.
download_api_asset() {
	command -v jsonfilter >/dev/null 2>&1 || return 1
	ra_metadata="$stage/.github-$release.json"
	if [ ! -s "$ra_metadata" ]; then
		if ! download_https "$ra_metadata" "https://api.github.com/repos/$REPOSITORY/releases/tags/$release" 60 'Accept: application/vnd.github+json' "$ra_deadline"; then return 1; fi
	fi
	if [ "$(jsonfilter -i "$ra_metadata" -e '@.tag_name' 2>/dev/null)" != "$release" ] || \
		[ "$(jsonfilter -i "$ra_metadata" -e '@.draft' 2>/dev/null)" != false ]; then
		dl_error=download_invalid; return 1
	fi
	ra_id=$(jsonfilter -i "$ra_metadata" -e "@.assets[@.name=\"$ra_name\"].id" 2>/dev/null) || ra_id=''
	case "$ra_id" in ''|0|*[!0-9]*) dl_error=download_asset_missing; return 1 ;; esac
	[ "${#ra_id}" -le 19 ] || { dl_error=download_invalid; return 1; }
	[ "$(jsonfilter -i "$ra_metadata" -e "@.assets[@.name=\"$ra_name\"].state" 2>/dev/null)" = uploaded ] || { dl_error=download_invalid; return 1; }
	download_https "$ra_output" "https://api.github.com/repos/$REPOSITORY/releases/assets/$ra_id" "$ra_timeout" 'Accept: application/octet-stream' "$ra_deadline"
}
download_release_asset() {
	ra_name=$1; ra_output=$2; ra_timeout=$3
	ra_deadline=$(($(download_clock) + ra_timeout))
	if [ -n "$prepare_deadline" ] && [ "$prepare_deadline" -lt "$ra_deadline" ]; then ra_deadline=$prepare_deadline; fi
	# Once the official API works, avoid re-probing a broken website for every
	# package. A failed preferred route still falls back to the other origin.
	if [ "${prefer_asset_api:-0}" = 1 ]; then
		if download_api_asset; then return 0; fi
	else
		if download_https "$ra_output" "https://github.com/$REPOSITORY/releases/download/$release/$ra_name" "$ra_timeout" '' "$ra_deadline"; then return 0; fi
		if download_api_asset; then prefer_asset_api=1; return 0; fi
		return 1
	fi
	download_https "$ra_output" "https://github.com/$REPOSITORY/releases/download/$release/$ra_name" "$ra_timeout" '' "$ra_deadline"
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
	if [ "$result" -ne 0 ] && [ "$stopped" = 1 ]; then
		# Package maintainer scripts may have restarted the managed service.
		if [ -x /etc/init.d/daed ]; then /etc/init.d/daed stop >/dev/null 2>&1 || :; fi
		if [ "$recoverable" = 1 ] && restore_previous_install; then
			printf 'NaiLink: Update failed; previous installation and service state restored. Backup: %s\n' "${backup:-not needed}" >&2
		else
			if [ -x /etc/init.d/daed ]; then /etc/init.d/daed stop >/dev/null 2>&1 || :; fi
			printf 'NaiLink: Operation did not complete. Service was stopped. Backup: %s\n' "${backup:-not created}" >&2
		fi
	fi
	[ -z "$stage" ] || rm -rf "$stage"
	exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ "$#" -gt 0 ]; do
	case "$1" in
	--release) [ "$#" -ge 2 ] || fail 'Missing release tag.'; release=$2; explicit_release=1; shift 2 ;;
	--asset-dir) [ "$#" -ge 2 ] || fail 'Missing asset directory.'; asset_dir=$2; shift 2 ;;
	--menu) menu=1; shift ;;
	--update-only) update_only=1; explicit_action=1; shift ;;
	--prepare-timeout)
		[ "$#" -ge 2 ] || fail 'Missing preparation timeout.'
		if ! printf '%s\n' "$2" | grep -Eq '^[1-9][0-9]{0,2}$' || [ "$2" -gt 900 ]; then fail 'Invalid preparation timeout.'; fi
		prepare_timeout=$2; shift 2 ;;
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
[ "$update_only" != 1 ] || [ "$action" = install ] || fail '--update-only requires install/update mode.'
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

restore_previous_install() {
	# Never restart a partially replaced installation. Online updates stage the
	# old native packages before stopping; manual installs retain their backup.
	if [ "$packages_changed" = 1 ]; then
		[ "$rollback_ready" = 1 ] && [ "$backup_verified" = 1 ] || return 1
		printf 'NaiLink: Restoring verified previous native packages…\n' >&2
		uci set daed.main.enabled=0 && uci commit daed || return 1
		if [ "$format" = apk ]; then
			apk add --allow-untrusted "$rollback_runtime" "$rollback_plugin" || return 1
		else
			opkg --force-downgrade install "$rollback_runtime" "$rollback_plugin" || return 1
		fi
		/etc/init.d/daed stop >/dev/null 2>&1 || return 1
		waited=0
		while pidof daed >/dev/null 2>&1; do
			[ "$waited" -lt 35 ] || return 1
			sleep 1; waited=$((waited + 1))
		done
		installed daed-router && installed luci-app-daed-router || return 1
		[ "$(cat /usr/share/daed-router/version)" = "$previous_version" ] || return 1
		# Preserve the failed attempt too, including any database/WAL written by
		# package scripts. Refuse to overwrite it if this extra backup fails.
		failed_backup="/root/daed-backups/failed-update-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
		set -- etc/config/daed
		[ ! -d /etc/daed ] || set -- "$@" etc/daed
		tar -czf "$failed_backup" -C / "$@" && tar -tzf "$failed_backup" >/dev/null || return 1
		chmod 600 "$failed_backup" || return 1
		# Do not replay a WAL produced by the failed/new process over the old DB.
		if tar -tzf "$backup" | grep -q '^etc/daed/wing.db$'; then
			rm -f /etc/daed/wing.db-wal /etc/daed/wing.db-shm
		fi
		tar -xzf "$backup" -C / || return 1
		/etc/init.d/rpcd reload >/dev/null 2>&1 || :
	fi
	if [ "$was_autostart" = 1 ]; then /etc/init.d/daed enable || return 1
	else /etc/init.d/daed disable || return 1; fi
	if [ "$was_running" = 1 ]; then
		/etc/init.d/daed start || return 1
		listen=$(/usr/libexec/daed/check --listen) || return 1
		waited=0
		until pidof daed >/dev/null 2>&1 && uclient-fetch -T 2 -O /dev/null "http://$listen/" >/dev/null 2>&1; do
			[ "$waited" -lt 20 ] || return 1
			sleep 1; waited=$((waited + 1))
		done
	fi
	return 0
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
	[ ! -x /usr/libexec/daed/nailink-maintenance ] || /usr/libexec/daed/nailink-maintenance stop || fail 'Cannot stop NaiLink maintenance before uninstall.'
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
	rm -rf /var/run/daed-maintenance
	rm -f /usr/libexec/daed/nailink-maintenance
	rm -f /etc/config/daed /etc/config/daed.apk-new /etc/config/daed.opkg-dist /etc/init.d/daed /usr/bin/daed /usr/sbin/daed-router-setup /usr/sbin/nailink
	rm -f /usr/libexec/daed/check /usr/libexec/daed/btf-probe /usr/share/daed-router/version
	rm -rf /usr/libexec/daed/components
	rm -f /usr/libexec/daed/cores/sing-box /usr/libexec/daed/cores/mihomo /usr/libexec/daed/cores/mihomo.previous-*
	rmdir /usr/libexec/daed/cores 2>/dev/null || :
	rm -f /usr/libexec/daed/luci-proxy /www/cgi-bin/daed-router
	for link in /etc/rc.d/[SK][0-9][0-9]daed; do [ ! -L "$link" ] || rm -f "$link"; done
	/etc/init.d/rpcd reload >/dev/null 2>&1 || :
	printf 'NaiLink: Uninstalled. Configuration/database backup: %s. Existing BTF retained.\n' "${backup:-no existing data}"
	stopped=0
}

if [ "$action" = uninstall ]; then uninstall_daed; exit 0; fi
stage=$(mktemp -d /tmp/daed-install.XXXXXX) || fail 'Cannot create staging directory.'
[ "$update_only" != 1 ] || prepare_deadline=$(($(download_clock) + prepare_timeout))
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
		asset_timeout=180
		case "$name" in *.ipk|*.apk) asset_timeout=600 ;; esac
		if ! download_release_asset "$name" "$stage/$name" "$asset_timeout"; then
			printf 'NAILINK_DOWNLOAD_ERROR=%s\n' "$dl_error" >&2
			fail "Cannot download $name. Both official GitHub routes were tried when available. Check router DNS, HTTPS access, CA certificates and system time. Existing service/data were not changed."
		fi
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
prepare_rollback() {
	for original in /etc/daed /etc/config/daed /etc/daed/wing.db /etc/daed/wing.db-wal /etc/daed/wing.db-shm; do
		[ ! -L "$original" ] || fail 'Cannot prepare rollback for symlinked configuration/database. Existing service/data were not changed; back up the external data before a manual upgrade.'
	done
	previous_version=$(cat /usr/share/daed-router/version)
	printf '%s\n' "$previous_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$' || fail 'Cannot prepare rollback: installed version is unknown.'
	rollback_dir="$stage/rollback"
	mkdir "$rollback_dir"
	# Fetch in a subshell so the current manifest/release/filenames do not change.
	(
		stage=$rollback_dir
		release="openwrt-v${previous_version%-*}"
		if [ -n "$asset_dir" ]; then asset_dir="$asset_dir/rollback"; fi
		fetch manifest.tsv
		[ "$(head -n 1 "$stage/manifest.tsv")" = '# daed-openwrt-manifest-v1' ] || fail 'Invalid rollback manifest.'
		for role in daed-router luci-app-daed-router; do
			arch=$selected_arch
			[ "$role" != luci-app-daed-router ] || arch=all
			old=$(record "$format" "$arch" "$role") || fail 'Rollback package is unavailable for this firmware architecture.'
			[ "$(printf '%s\n' "$old" | cut -f6)" = "${previous_version%-*}" ] || fail 'Rollback version mismatch.'
			case "$(printf '%s\n' "$old" | cut -f4)" in *."$format") ;; *) fail 'Rollback package format mismatch.' ;; esac
			verify_record "$old"
			printf '%s\n' "$(printf '%s\n' "$old" | cut -f4)" > "$stage/$role.name"
		done
	) || fail 'Cannot prepare verified rollback packages. Existing service/data were not changed.'
	rollback_runtime="$rollback_dir/$(cat "$rollback_dir/daed-router.name")"
	rollback_plugin="$rollback_dir/$(cat "$rollback_dir/luci-app-daed-router.name")"
	rollback_ready=1
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
	fail 'An existing unmanaged NaiLink installation was found. Back it up and migrate explicitly; it will not be overwritten.'
fi
if [ "$update_only" = 1 ]; then
	[ "$managed" = 1 ] || fail 'Existing managed install required for online update; no fresh installation was performed.'
	online_current=$(cat /usr/share/daed-router/version)
	online_current=${online_current%%-*}
	awk -v target="$version" -v current="$online_current" 'BEGIN {
		if (target !~ /^[0-9]+\.[0-9]+\.[0-9]+$/ || current !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1;
		split(target,a,"."); split(current,b,".");
		for (i=1;i<=3;i++) {if(a[i]+0>b[i]+0)exit 0;if(a[i]+0<b[i]+0)exit 1;}
	}' || fail 'Online update refused: installed version changed or downgrade requested. Check again.'
fi
if [ "$action" != reinstall ] && { installed daed || installed luci-app-daed; }; then fail 'Another NaiLink package is installed; use --reinstall for a backed-up fresh installation.'; fi
printf 'NaiLink: %s / %s / %s, release %s\n' "$machine" "$format" "$selected_arch" "$version"
case "$probe_cpu" in armv7|mips|mipsle)
	printf 'NaiLink: 32 位版本仅为实验构建；已测常见内核无法加载必需的 BPF 回调。最小内核探针通过后，仍需完整透明代理和长期运行验证。\n' ;;
esac

# Native package managers resolve kmods against the router's own feeds/kernel ABI.
# Never force dependencies or add a feed for a different firmware.
apk_offline=0
if [ "$format" = apk ] && apk add --no-network --simulate --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file" >"$stage/preflight.log" 2>&1; then
	# Installed/cached dependencies need no feed refresh or late network access.
	# A new dependency still takes the normal firmware-feed path below.
	apk_offline=1
	cat "$stage/preflight.log"
fi
if [ "$mode" != dry-run ] && [ "$apk_offline" = 0 ]; then
	if [ "$format" = apk ]; then
		apk update || fail 'Firmware package feed refresh failed. Existing service/data were not changed.'
	else
		opkg update || fail 'Firmware package feed refresh failed. Existing service/data were not changed.'
	fi
fi
# APK resolves architecture and dependencies before stopping/uninstalling NaiLink.
# --simulate opens the database read-only and uses existing index caches.
# Do not add --no-network: APK would mask uncached packages in HTTP(S) feeds.
if [ "$format" = apk ] && [ "$apk_offline" = 0 ]; then
	apk add --simulate --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file" || fail 'Package preflight failed. NaiLink was not stopped or uninstalled. Check firmware feeds/dependencies; dry runs require cached indexes.'
fi
[ "$mode" != dry-run ] || { printf 'NaiLink: Dry run passed; no packages, configuration or services changed.\n'; exit 0; }
[ "$update_only" != 1 ] || prepare_rollback
if [ -n "$prepare_deadline" ] && [ "$(download_clock)" -ge "$prepare_deadline" ]; then
	fail 'Update preparation timed out. Existing service/data were not changed.'
fi
if [ "$action" = reinstall ]; then uninstall_daed; stopped=1; fi
was_running=0
was_autostart=0
if [ "$managed" = 1 ]; then
	/etc/init.d/daed enabled >/dev/null 2>&1 && was_autostart=1
	if pidof daed >/dev/null 2>&1; then was_running=1; fi
	recoverable=1
	stopped=1
	/etc/init.d/daed stop || fail 'Cannot stop existing service for a consistent backup.'
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
	tar -tzf "$backup" >/dev/null || fail 'Cannot verify existing data backup.'
	backup_verified=1
fi
packages_changed=1
if [ "$format" = apk ]; then
	# Local release packages are unsigned; this flag is scoped to this transaction.
	if [ "$apk_offline" = 1 ]; then
		apk add --no-network --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file"
	else
		apk add --allow-untrusted "$stage/$runtime_file" "$stage/$plugin_file"
	fi
else
	opkg install "$stage/$runtime_file" "$stage/$plugin_file"
fi
if ! installed daed-router || ! installed luci-app-daed-router; then fail 'Native packages were not both installed.'; fi
if [ ! -x /usr/bin/daed ] || [ ! -x /usr/libexec/daed/check ] || \
	[ ! -x /usr/libexec/daed/luci-proxy ] || [ ! -x /www/cgi-bin/daed-router ]; then fail 'Installed runtime or LuCI gateway is incomplete.'; fi
for asset in geosite.dat geoip.dat; do
	if [ ! -s "/etc/daed/$asset" ] && [ ! -s "/usr/local/share/daed/$asset" ] && [ ! -s "/usr/share/daed/$asset" ]; then
		fail "Installed rule data is unavailable: $asset. Repair the firmware's v2ray-geosite/v2ray-geoip packages and daed-router search-path links."
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
