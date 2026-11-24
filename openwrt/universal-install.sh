#!/bin/sh
# SPDX-License-Identifier: MIT
set -eu
directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
case "$#:${1:-}" in
	0:) set -- --menu ;;
	1:--menu|1:--install|1:--check|1:--dry-run|1:--help) ;;
	*) printf '用法：sh install.sh [--menu | --install | --check | --dry-run]\n' >&2; exit 2 ;;
esac
exec sh "$directory/assets/install.sh" --asset-dir "$directory/assets" --local-cores "$@"
