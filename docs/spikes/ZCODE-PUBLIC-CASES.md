# ZCode 公开案例：发现方式、合成回放与防护边界

核查日期：2026-09-18。对应 Laodi `0.1.2-dev`、解析器 `zcode-3.12.3.7463-v3`。

本文将原作者公开报告、已安装客户端的静态契约、Laodi 的合成验证分开。没有向任何服务发送真实或合成仓库；没有要求用户主动诱发真实敏感信息上传。网页原文部分无法直接打开，取证描述使用搜索引擎收录的原始页面长摘录，并交叉核对本机公开程序的字段语义。没有拿到原作者原始磁盘或服务器记录。

## 人们怎么发现

共同路径是：磁盘/流量异常，或看到其他人的报告 → 检查客户端数据目录 → 用明文 manifest 判断打包范围 → 用 state 和客户端代码判断所处阶段。模型是否执行 `git log`、是否承认能读取文件，不是这条后台路径的审计依据。

| 原始案例 | 发现入口 | 报告中的证据 | 不能据此断言 |
|---|---|---|---|
| [ferstar](https://blog.ferstar.org/posts/zcode-silent-workspace-snapshot-upload/) | 清理磁盘发现 `.zcode` 超过 700 MB | 约 313 MB 待处理加密包；约 42,411 条清单；Git 对象、LFS 和 reflog 占大头；失败计数 564 | 564 次完整网络上传、特定密钥确已泄露、该待处理包已上传成功 |
| [Vonng](https://vonng.com/ai/zcode-upload/) | 看到 ferstar 报告后在自己的 Mac 复查；此前留意到流量消耗 | silo/mc 的清单主要为 Git；pgnls 有客户端接受记录但不含 `.git`；pgdoc 超过 1 GiB 上限，失败计数 102 | 所有列出的仓库都传成功、102 次上传、`.git` 是唯一值得监测的范围 |
| [V2EX 1242997 的 Windows 复测](https://www.v2ex.com/t/1242997) | 读曝光后让 Agent 检查本机产物和应用代码 | 151 条清单中一个 257,535,221 字节 Git pack；待处理包及 attempt/failure 字段；extra-manifest 包含设置和 skills 元数据 | Agent 辅助报告等于独立抓包；配置文件名证明里面有密钥；Windows 服务集成已由 Laodi 验证 |

[V2EX 1242973](https://v2ex.com/t/1242973) 的零散自查反馈可用来寻找线索；没有具体状态字段的“已成功上传”描述，不作为检测器的成功判据。网上转述的流量估算和失败次数不会写成产品事实。

## 按案例修正产品

1. **尽早记录已知快照清单。** 不等目录膨胀到几百 MB，不读取或解密 `.enc`，不因没有密文文件就判断没发生过快照。
2. **三类对象独立可见。** Git 历史对象、其他非空工作区清单、附加全局配置。Git 家族和普通工作区家族互斥；不把只有 HEAD/config/reflog 的清单称为“完整 Git 历史”。
3. **每份清单分别关联阶段。** manifest → 尝试流程 → 客户端 HTTP 成功记录。accepted 必须关联自己的 hash，不能套到更新的 pending；active 与旧 pending 别名不重复计数。
4. **不把失败计数当流量。** `failureCount`、压缩体积、索引开关都不能升级为上传成功。`attemptCount` 在请求前就可能增加，仍须保留“是否发出网络请求未知”。
5. **首次接入就能看到旧证据。** `check` 无需安装服务即可检查；`setup --apply` 成功后立即输出一次只读检查结果。后台初次基线不刷通知；既有事件保留供查询，不能说成刚刚发生。
6. **提醒依据是客户端额外快照。** 真实项目里的普通 Git 操作、用户主动请求的提交摘要、模型上下文不因本功能自动产生告警。快照是否是用户主动启用 Wiki 的结果也不能从这些字段推断，提示不能一律写成“未经授权偷传”。

## 合成覆盖矩阵

测试源：[public_cases_test.go](../../internal/laodi/public_cases_test.go)。测试借用公开案例的形状，文件名和工作区是合成值；阶段演进是测试构造，不表示原作者观察到了每个阶段。大体积只作为 JSON 元数据，普通文件列表缩小后保留关键分类。

| 场景 | 应有结果 |
|---|---|
| Git 对象＋LFS＋reflog，failure=564，attempt=0 | 两个对象路径计数；清单线索可见；不报已发请求或成功 |
| Windows 大 pack＋global-configs | 两类分别记录并升级阶段；不读取任何源码、配置或归档 |
| 无 Git 的普通工作区有 accepted | 普通工作区客户端接受记录；不谎称 Git 历史或密钥外泄 |
| 超过 1 GiB、failure=102 | 保留清单线索；不将大小或失败计数解释为传输 |
| 旧普通清单 accepted＋新 Git 清单 pending | 只给旧清单接受结论；新 Git 保持自己的阶段 |
| 索引设置关闭但清单仍在 | 继续报告实际证据，不以开关作为安全证明 |
| pending 槽/包已清理，accepted 和关联清单保留 | 仍识别接受记录，不依赖待传目录非空 |
| active/旧 pending 别名与 latest pending | 别名去重，新待传组独立处理 |
| 42,411 条合成清单 | 在预算内完成分类；后续未变化扫描命中缓存 |
| 首次 CLI 检查已有接受记录 | 可见且脱敏，不安装服务、不创建 Laodi 状态目录 |

现有回归另覆盖：未知构建/格式降级、半写恢复、路径和符号链接、预算公平轮转、通知失败、静默基线、重启去重、覆盖失效恢复。正常 Git 并行验证使用真正的 Git 和 Go 监测器，但仍不是 ZCode GUI 全链路测试。

## 本轮最终验证结果

最终二进制 SHA-256：`d5f93ba20b0ade05004c15941d084ad62067f677b59b0485da2023073dd6fe56`，版本 `0.1.2-dev`；下列二进制测量期间文件均未变化。

- Go全套 `go test -race ./...`、`go vet ./...` 和构建通过。包含9项公开案例主测试、3项升级迁移主测试、首次只读检查与现有回归。
- 通知helper的严格警告构建、Clang静态分析通过；没有请求通知权限或发送通知。
- [实际二进制合成回放](v1-public-case-replay-results.json)：默认2秒轮询，三次阶段变更在约1.99–2.01秒后持久化；7条预期事件，0条错误Git接受记录，摘要脱敏。这里测的是**本地事件保存**，不是系统横幅延迟、全局实时性或事前拦截。
- [并行Git验证](v1-monitor-workflow-results.json)：12次status/diff/add/commit全部完成，源HEAD/index保持预期，重启无重复事件。不等于所有真实客户端长任务已验收。
- [100份小清单资源短测](v1-public-case-resource-results.json)：RSS中位约9.03MiB，OS峰值约11.63MiB，FD 7–9，22秒累计CPU约0.098秒。
- [一份42,411条清单资源短测](v1-public-case-large-resource-results.json)：RSS中位约11.22MiB，OS峰值约11.28MiB，FD 7–9，22秒累计CPU约0.029秒。

资源结果只覆盖Go监测进程、一次启动后基本不变的合成数据；不含通知helper，不证明持续高频快照、休眠恢复、72小时稳定性或长期CPU≤0.2%。本轮最终只读检查本机仍返回`no_evidence_directory`，没有检测到支持的快照根，不能据此确认没有其他上传。

实现变更集中于[扫描分类](../../internal/laodi/scanner.go)、[升级基线存储](../../internal/laodi/store.go)、[通知派发](../../internal/laodi/runner.go)、[脱敏摘要](../../internal/laodi/summary.go)、[CLI初次检查](../../cmd/laodi/main.go)及[固定通知文案](../../platform/macos/notifier/main.m)。测试、回放脚本和说明同步更新。沿用单Go进程与原有读取预算，没有新运行依赖、解密/复制归档、全盘扫描或客户端补丁。

## 能发现，不代表能抢在上传前阻止

默认每 2 秒巡检，有预算上限和操作系统调度延迟；不能承诺 2 秒内必达通知。清单和状态如果在两次检查间产生并消失，或者客户端直接从内存上传，可能没有可读证据。未知版本、其他数据根、已删除记录也会形成缺口。

新清单仍在时可以在下一次成功巡检发现；进入尝试或成功之后才首次看到它，就必须报告当时已有的最高证据，不能显示“已阻止”。系统通知还依赖用户许可和系统呈现设置。合成回放证明的是分类、关联与事件流程，不能证明真实客户端一定留下这些信号。

## 为什么不自动套用网上的阻断方法

| 方法 | 本次决定与依据 |
|---|---|
| 禁读整个 `.git` | S0 已实测破坏 status/diff/log/commit 等正常功能，不作为默认方式 |
| 锁住整个 checkpoints 目录 | 本版本 GitCheckpointStore 与 RepoSnapshot 共用该根；可能损伤回滚/检查点 |
| 删除 pending 或把目录换成文件 | 有竞态，不能撤回已打开的流/已发送字节，还可能触发重建并破坏证据 |
| 设置 `repoSnapshotIndexingEnabled=false` | 已核对调用链没有以它作为采集门禁，不能据此显示“保护成功” |
| 封整个 API/OSS 域名 | API 域名共享正常功能；OSS host 由凭据下发，不能假定唯一目的地且无副作用 |
| 修改 ASAR、依赖提示词/工具 hook | 客户端补丁的版本、签名、升级风险；模型工具 hook 覆盖不到宿主后台路径 |

[NodeLoc 防御帖](https://www.nodeloc.com/t/topic/109590)和[LINUX SB 转述](https://linux.sb/topic/22646)中的单机处理经验不等于 Laodi 的长任务兼容验证。因此本轮没有对用户执行 chmod、ACL、不可变标记、删包、ASAR 修改、退出登录或网络变更。

值得继续验证的狭窄入口是 `GET /api/v1/snapshot/upload-credential`：已安装版本先取得凭据再扫描；无凭据则返回。静态定位为 host index.js 中凭据 URL 构造约 1827369、采集前获取约 2254913、凭据请求约 2271552、OSS 目标构造约 2269216，程序身份见[本地契约](ZCODE-3.12.3-CONTRACT.md)。这只是研究入口：HTTPS 的路径不可由 DNS/hosts 或普通 CONNECT 代理选择性拦截；改变 API origin 会涉及认证、回调和所有 API；已经获得的凭据也不被撤销。尚未验证的网关不能作为当前防护功能宣传。

## 与本机实际观察的关系

[用户授权的只读观察](ZCODE-LIVE-OBSERVATION.md)中，已知 checkpoints 根尚未出现；这只能说明当时未发现该证据，不能证明所有上传不存在或客户端已经修复。用户无需继续在真实项目中诱发行为。本轮测试只在临时合成目录运行，不启动 ZCode、不调用模型、不修改代理、不发送系统通知。

最终发布还缺真实客户端合成环境对照、macOS 通知可见性、正式签名分发和长期资源/升级恢复验收。当前是开发版监测器，不能称为已完成的上传阻断产品。

## 检索台账

- OpenCLI/X：`ZCode (.git OR checkpoints OR lastAcceptedManifestHash OR 上传)`，1次搜索；读到首发作者链接和大量转述，技术结论回到原作者正文与客户端代码。未发帖、回复或联系作者。
- Web检索：本轮5次搜索调用，16个查询。关键词如下。原站直接打开多次失败；ferstar/Vonng使用原站索引的连续主文长摘录，V2EX只覆盖主帖/相关复测片段，不能称完整评论审查。部分查询无有效结果，不作为依据。
- 同一研究链路早先Grok查询因登录不可用未取得结果，本轮不重试。

```text
ZCode "上传" "lastAcceptedManifestHash"
ZCode ".git" "1311"
site:blog.ferstar.org/posts/zcode-silent-workspace-snapshot-upload/
site:vonng.com/ai/zcode-upload/
site:v2ex.com/t/1242997
site:v2ex.com/t/1242973
ZCode "vonng" "pgnls"
ZCode "257535221"
ZCode "151" ".git"
"ZCode" "1242973"
"怎么看待" "ZCode 静默上传" "Windows"
site:linux.sb/topic/22646 Zcode
"解包了 ZCode 上传的加密包"
"有用 zcode 的，马上关闭" "1311"
site:locdd.com/t/topic/92358
site:nodeloc.com/t/topic/109590 ZCode
```
