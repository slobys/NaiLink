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

这次独立运行证明目标设备上封禁应答的行为，不等同于完整软件包升级、全部代理内核切换和恢复流程的验收。公开发布仍需完成候选包验证。
