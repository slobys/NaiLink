#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Generate a detached BTF from reviewed dae stubs and a matching OpenWrt SDK.
set -euo pipefail
if [[ $# != 8 ]]; then
    echo "Usage: $0 SDK SDK_SHA256 KERNEL_RELEASE KERNEL_ARCH CROSS_PREFIX STUB_DIRECTORY PAHOLE OUTPUT" >&2
    exit 2
fi
sdk=$(realpath "$1")
expected=$2
kernel_release=$3
kernel_arch=$4
prefix=$5
stubs=$(realpath "$6")
pahole=$(realpath "$7")
output=$(realpath -m "$8")
[[ $expected =~ ^[a-f0-9]{64}$ ]]
printf '%s  %s\n' "$expected" "$sdk" | sha256sum -c -
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/sdk"
tar -xf "$sdk" -C "$work/sdk" --strip-components=1
mapfile -t configs < <(find "$work/sdk/build_dir" -path '*/include/generated/autoconf.h')
[[ ${#configs[@]} == 1 ]]
kernel_dir=${configs[0]%/include/generated/autoconf.h}
grep -Fxq "#define UTS_RELEASE \"$kernel_release\"" "$kernel_dir/include/generated/utsrelease.h"
cc=$(find "$work/sdk/staging_dir" -path "*/bin/${prefix}gcc" -print -quit)
[[ -x $cc ]]
export STAGING_DIR="$work/sdk/staging_dir"
flags=(-nostdinc -isystem "$("$cc" -print-file-name=include)" -D__KERNEL__ -include "$kernel_dir/include/linux/kconfig.h")
for header in compiler-version.h compiler_types.h; do
    if [[ -f "$kernel_dir/include/linux/$header" ]]; then
        flags+=(-include "$kernel_dir/include/linux/$header")
    fi
done
for dir in "arch/$kernel_arch/include" "arch/$kernel_arch/include/generated" include include/generated \
    "arch/$kernel_arch/include/uapi" "arch/$kernel_arch/include/generated/uapi" include/uapi include/generated/uapi .; do
    flags+=("-I$kernel_dir/$dir")
done
flags+=(-std=gnu11 -funsigned-char -fshort-wchar -fno-common -fno-PIE -O2 -g -gdwarf-4 -fno-eliminate-unused-debug-types -Wno-error -Wno-unused-function -Wno-unused-variable)
if [[ $kernel_arch == x86 ]]; then
    flags+=(-m64 -mno-red-zone)
fi
objects=()
for unit in core modules private; do
    "$cc" "${flags[@]}" -c "$stubs/shadow_btf_$unit.c" -o "$work/$unit.o"
    objects+=("$work/$unit.o")
done
"${cc%gcc}ld" -r -o "$work/shadow_btf.o" "${objects[@]}"
"$pahole" --btf_encode_detached="$output" "$work/shadow_btf.o"
sha256sum "$output"
