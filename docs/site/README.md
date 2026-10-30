# NaiLink 在线文档

线上地址：<https://slobys.github.io/NaiLink/>

参考 [GroupGuard](https://github.com/slobys/telegram-group-manager) 的使用文档组织方式，按安装、设置、管理、维护组织。独立静态站，没有新增第三方依赖，不打包进路由器插件。

## 本地预览

在仓库根目录运行（Python 3.10+）：

```sh
python3 docs/site/build.py
python3 -m http.server 4173 --directory .build/docs-site
```

Windows 使用 `python`。浏览器打开 <http://localhost:4173>；请通过 HTTP 预览，文件方式打开会影响搜索。

## 内容与发布

- `site.json`：唯一文档版本、发布标签、页面和侧栏顺序。
- `content/*.html`：正文，二、三级标题必须有唯一 `id`。站内链接使用 `page.html#section`。
- 正文的 `$version` / `$release` 在构建时替换；若文字需包含美元符号，写作 `$$`。
- `template.html` / `site.css` / `site.js`：布局、本地中文搜索、命令复制、主题、手机目录。
- `assets/`：仅公开的文档资源。不要放用户截图、配置、密钥或归档。
- `BRAND.md`：Logo 说明和生成提示词。

构建自动检查图片、站内链接、锚点、搜索目标。CI 还检查版本同步、输出范围和损坏链接拒绝。发布目录固定为 `.build/docs-site`，不上传仓库根目录。

`content/cores.html` 是面向使用者的内核与接管教程，`docs/core-switching.md` 保留参数、构建组件等参考细节。内核功能改变时，同时核对两处，避免把仅 TUN 的早期限制写成当前限制。

```sh
python3 -m unittest discover -s tests/docs -p "test_*.py"
```

电脑 / 手机浏览器检查使用仓库已有的 Playwright，无新依赖：先运行预览服务器，再执行 `node tests/docs/browser.mjs`。首次使用需准备 Playwright Chromium，也可设置 `DOCS_BROWSER_CHANNEL=chrome` 使用本机 Chrome。截图在 `.build/docs-check/`；`DOCS_TEST_URL` 可指定含仓库子路径的站点地址。

`.github/workflows/docs.yml` 在默认分支相关文件更新时自动发布，PR 仅构建检查。首次发布需在 GitHub 仓库 Pages 设置选择 GitHub Actions。更新安装版本时，同步根 README 和 `site.json`；不要把实验架构写成已验证可用。
