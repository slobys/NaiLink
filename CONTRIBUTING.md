# 参与 NaiLink 开发

NaiLink 基于 daed 二次开发并独立维护。请在 [NaiLink Issues](https://github.com/slobys/NaiLink/issues) 反馈问题或建议，不要提交到上游。

反馈时说明固件、CPU、NaiLink 版本、复现步骤和错误日志；不要上传密码、订阅链接、数据库或登录信息。

## 开发环境

使用 Linux 或 WSL：Node.js 22、pnpm 10.24.0、Go 1.26.0、Python 3、Make、Clang / LLVM 15+。

```sh
git clone --recurse-submodules https://github.com/slobys/NaiLink.git
cd NaiLink
corepack enable
corepack prepare pnpm@10.24.0 --activate
pnpm install --frozen-lockfile
pnpm dev
```

模拟数据预览运行 `pnpm dev:mock`，不连接真实路由器。完整编译及 Docker 见[构建说明](docs/build.md)，路由器打包见 [OpenWrt 开发说明](openwrt/README.md#开发)。

## 提交前

- 从功能分支提交 PR，说明问题、修改和验证结果。
- 运行 `pnpm test`、`pnpm check-types` 及相关回归。`pnpm lint` 会自动修改格式，提交前检查差异。
- 修改安装、DNS、路由或后端时，按 [OpenWrt 验证记录](openwrt/VALIDATION.md) 补充适用测试。模拟环境通过不等于真实透明代理或公网线路验证通过。
- 仓库维护与文档检查：`python3 -m unittest discover -s tests/repository -v`、`python3 -m unittest discover -s tests/docs -v`。
- 提交标题采用 `fix:`、`feat:`、`docs:`、`chore:` 等前缀，参考[提交规范](docs/commit-msg-guide.md)。

PR 目标为本仓库 `main`。不要直接更新固定后端子模块；增量位于 `openwrt/wing/`，由准备工具核对后应用。保留上游版权和许可证，不要替换模块名、协议名或内部兼容路径。

## 构建与发布

默认分支运行仓库、文档和 Docker 检查。OpenWrt 工作流保留完整回归、五种 CPU 构建与十组 SDK 打包。

发布时从 `main` 手动运行 **OpenWrt plugin**，填写与源码一致的版本号；默认只生成 artifact。勾选 `publish` 后，全部检查成功才创建 GitHub 测试版草稿，上传安装包、校验清单和完整源码。维护者检查草稿后发布。

准备新版本和草稿时，文档展示版本及固定发行链接保留在已发布版本，不要求 `docs/site/site.json` 与源码构建版本相同。主安装入口保持固定 URL，由 `--latest` 查询最新公开版本。维护者发布后，先匿名下载该标签的 `install.sh`，确认成功，再同步文档版本、固定下载 URL 和备用命令的 `ref` / `--release` 参数；不要把未发布草稿作为固定版本入口。

同名已发布版本和不匹配的标签不会被覆盖；同一提交的失败草稿可重试。此流程无需创建发布 PR，也不依赖 Docker Hub、Quay 或原项目组织权限。在线文档只由 **Online documentation** 部署。
