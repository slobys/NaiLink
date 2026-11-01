# AdZero 与 mihomo 的 DNS 拒绝应答兼容

2026-11-01 在 AdZero 0.23.6、NaiLink 0.6.63、mihomo 1.19.33 的组合中确认：广告规则已命中，但客户端收到 SERVFAIL，AdZero 不将普通 DNS 故障算作拦截。

## 分段实测

- `doubleclick.net`、`googleadservices.com` 均命中当前已应用规则。
- 两个样本的 UDP／TCP 查询在 NaiLink 过滤桥返回 REFUSED、无答案；经过 mihomo 后返回 SERVFAIL、无答案。
- 同时测试的 `www.baidu.com` 在两个协议下均返回正常地址。
- 同一采样窗口内，LAN 请求与应答能完整关联；本次问题不是采集遗漏。

这证明所测广告域名被拒绝解析，但不能把全部 SERVFAIL 都解释为广告拦截，也不能据此判断所有网页广告的过滤效果。

## 修复

仅在 mihomo DNS 桥明确匹配 `reject` 策略时返回 NXDOMAIN。mihomo 会把普通上游的 REFUSED 当作解析故障；NXDOMAIN 则保留明确的终止应答。正常域名继续查询原有上游，真实上游故障仍返回 SERVFAIL。

AdZero 不需要把 SERVFAIL 补记为拦截，也不需要增加广告列表、清零计数或修改用户 DNS。更新后的 NaiLink 兼容层生效后，实际观察到的 NXDOMAIN 可按现有口径计入拦截。

## 验证与范围

- 旧实现的两个新增回归分别复现 REFUSED 与 SERVFAIL，未满足预期封禁应答。
- 修复后的本地真实 mihomo 1.19.33 和路由器 ARM64 独立测试均通过。
- 覆盖 A／AAAA、UDP／TCP、精确域名及子域拒绝、正常解析和真实上游失败；另有 DNS 策略与问题保留回归。
- 路由器独立测试只在临时目录使用回环随机端口，未安装候选软件包、重载当前代理或接管 LAN。测试前后管理进程、当前 mihomo 的启动时间及配置指纹一致，临时文件已清理。

独立运行用于验证目标设备上的封禁应答，完整软件包和 LAN 路径另按下面的验收记录确认。

## 原生 APK 与 LAN 验收

2026-11-01，在原设备使用最终 CI 的 ARM64 APK 完成 NaiLink 0.6.63 → 0.6.64 更新，AdZero 保持 0.23.6：

- [完整发布构建](https://github.com/slobys/NaiLink/actions/runs/41262427744)的 29 项检查全部通过。79 个发布资产的 GitHub 摘要与校验清单一致；实际使用的安装文件逐项核对 SHA-256，源码提交为 `d8489f1ab7bd6841cf71976ac481818c16409146`。
- 原生兼容性和依赖预检通过，更新前准备了与现用载荷一致的 0.6.63 回退包。安装器生成的停机一致性备份可读取，全部 11 张用户表与更新前、更新后内容一致；本轮没有执行故障回退。
- 30 个安装载荷文件及实际运行的管理程序均匹配 CI 包。原有 mihomo／TPROXY、待应用状态、DNS 配置、规则数据和托管规则指针保留，AdZero 继续开启，统计清零标识保持不变。
- 同一 LAN 客户端查询 `doubleclick.net`、`googleadservices.com`，UDP／TCP 共 4 次均返回 NXDOMAIN 且没有地址；`www.baidu.com` 的 UDP／TCP 两次均为 NOERROR 并有地址。
- 仅捕获本轮测试套接字的数据包，共关联 6 个查询及其应答，识别 4 次拦截、0 次匹配后的解析故障。生产计数在采样期间从 458 增至 463，其中还包含同期后台流量，不能把全部增量都归于本轮测试。
- 同一客户端的百度、GitHub HTTPS 请求均返回 HTTP 200。这里验证 DNS 应答和所测 HTTPS 路径，不把它等同于所有网页广告或所有客户端 DNS 路径的验收。
