# dae 安装所用的备用 BTF

此目录只收录与官方固件构建对应的精简 BTF。发布清单用固件名称、版本、源码修订、目标、包架构、包格式、内核版本和已安装的内核包版本共同匹配。安装时优先读取系统 BTF，其次使用本固件的软件源，最后才使用发布附件。候选文件通过 BTF 解析和 eBPF 探针后才会写入内核加载器的标准搜索路径。

| 官方 OpenWrt 25.12.6 目标 | 架构 | 官方 SDK SHA-256 |
| --- | --- | --- |
| [rockchip/armv8](https://downloads.openwrt.org/releases/25.12.6/targets/rockchip/armv8/) | aarch64_generic | `2a45cbfa7e6cce588d006039205afba03bd242e90d2df4dc442920716a642ce0` |
| [x86/64](https://downloads.openwrt.org/releases/25.12.6/targets/x86/64/) | x86_64 | `1864152619092e781fa36a401960c8e9ae42d6a364c44ee5f0b197ca5f3c0fcc` |

生成使用官方 SDK 中的 Linux 6.12.112 内核头文件、配置和交叉编译器，并用 pahole 1.30 将 [dae 上游的三个 BTF 源文件](https://github.com/daeuniverse/openwrt-packages/tree/e104a4930c5fc4f625f3355257626495685424f5/src) 编译为独立 BTF。将固定提交里的 `shadow_btf_core.c`、`shadow_btf_modules.c` 和 `shadow_btf_private.c` 放在同一目录后，在 Linux 上运行

```sh
bash openwrt/build-kernel-btf.sh SDK SDK_SHA256 6.12.112 KERNEL_ARCH CROSS_PREFIX STUB_DIRECTORY /path/to/pahole OUTPUT
```

rockchip 使用 `arm64` 和 `aarch64-openwrt-linux-musl-`。x86 使用 `x86` 和 `x86_64-openwrt-linux-musl-`。配套内核构建选项和源码来自表中对应的官方目标目录及 SDK；发布校验以 `index.json` 的文件摘要为准。

其他 OpenWrt 目标、iStoreOS 和 ImmortalWrt 继续使用各自固件的 BTF 或软件源。增加备用附件时需要对应目标的 SDK、内核 ABI 和真实内核验收。ARMv7、MIPS 大端及小端保持实验支持，同一架构的不同固件仍可能缺少 dae 所需的内核能力。
