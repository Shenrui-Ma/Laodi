# 工具事件检测：ZCode 与 Claude Code

版本：`0.2.1-dev`。本轮实现第一类“工具读取及输出中的敏感信息”和第三类中的两个产品适配。**不代表覆盖两款产品的全部后台行为，也不确认内容已发送。** 纯内存直传检测另列未来TODO。

## 已实现的行为

| 输入 | 处理 | 提醒 |
|---|---|---|
| Read或可保守解析的cat/head/tail/sed请求读取`.env`、部分凭据/私钥路径 | 记录访问请求类别，未确认读取成功 | 只记本地事件，避免正常配置读取刷屏 |
| Bash/Read支持的输出字段出现疑似凭据 | 本机检测固定规则，不验证凭据有效性 | 非阻塞系统通知；任务继续 |
| 工具失败，但错误文本含疑似凭据 | 保留发现，并注明工具失败 | 同上，不称执行成功或上传成功 |
| 输入过大/损坏、输出结构未知、明确截断或部分不可检查 | 记录检测缺口 | 限频提醒；不影响Agent |
| 同一份输出既有疑似凭据，又有截断 | 两类证据都保存 | 优先提醒凭据，不被缺口通知覆盖 |
| 队列满、争用导致观察丢失 | 保留固定历史缺口标记 | 提醒部分事件可能未被检查 |

规则覆盖：私钥文本块、部分有明确格式的提供商令牌、明确凭据字段名对应的高熵赋值。明确API_KEY/SECRET_KEY下的高熵十六进制值也检查；普通hash、UUID、裸base64、已识别占位符/脱敏值不因此报警。结果只能称“疑似”。规则不验证账户、不向任何网站发送候选值。

仅列出`.ssh`目录、正常Git日志、Write工具的输入源码、会话字段或任意metadata不作为敏感输出扫描。复杂shell不试图还原全部执行行为；通过Python或间接脚本读取的路径可能识别不了，但受支持的Bash输出仍会检查。

## 接入

核心二进制与无窗口通知helper的构建沿用[开发说明](DEVELOPMENT.md)。运行中的旧版后台需要用户在合适时机更新；本次开发没有替用户重启Agent、安装服务或修改真实配置。

先预览，确认生成的仅为老底自己的条目：

```sh
./bin/laodi hooks install --adapter zcode
./bin/laodi hooks install --adapter claude-code
```

用户决定接入后，对需要的产品执行：

```sh
./bin/laodi hooks install --adapter zcode --apply
./bin/laodi hooks install --adapter claude-code --apply
```

程序写入对应用户配置：ZCode的`~/.zcode/cli/config.json`、Claude Code的`~/.claude/settings.json`。保留其他字段和Hook；重复安装不叠加。只用新会话验证接入，当前长任务不重启。Hook配置成功不等于真实客户端已经发来事件。

**需要运行监测器消费事件。** ZCode快照与工具事件可共用一个进程：

```sh
./bin/laodi watch
# 或用户选择后台接入时：
./bin/laodi setup --apply
```

只使用工具检测、不使用ZCode快照时：

```sh
./bin/laodi watch --hooks-only
# 或：
./bin/laodi setup --hooks-only --apply
```

`--hooks-only`不要求安装ZCode。两种监测模式使用不同根身份，不能对同一已初始化状态目录偷偷切换；需保持模式一致或显式使用不同`--state-dir`，对应Hook也须使用同一目录。已安装服务的参数变化仍按原所有权规则拒绝静默重启。

系统通知接入仍依[通知说明](NOTIFICATIONS.md)：用户一次授权，并在watch/setup中传`--notifier`的helper绝对路径。未接入通知时照常保存事件，不会临时请求权限。

```sh
./bin/laodi hooks status
./bin/laodi status --format agent-summary
./bin/laodi incidents --format agent-summary
./bin/laodi check --hooks-only --format agent-summary
```

`hooks status`只查看队列是否存在、待处理数量和历史缺口。队列为空可能是已处理完，也可能是无命中或Hook未生效，**不能当作健康证明**。`check --hooks-only`只读抽查排队记录，历史仍看incidents。服务停止后，Hook可暂存脱敏事件，但通知需等消费者恢复。

移除时只删除准确匹配的自有条目，保留其他配置和事件：

```sh
./bin/laodi hooks uninstall --adapter zcode --apply
./bin/laodi hooks uninstall --adapter claude-code --apply
```

保护边界：配置符号链接、重复JSON键、JSONC、超限配置及不明确的归属不自动修复。ZCode已有其他禁用Hook时不会擅自开启它们；Claude的全局禁用也不被覆盖。配置和收据各自原子写入，但不是跨文件事务，异常中断可能留下需要检查的不完整安装；外部同时编辑设置的竞态不能完全消除。

## 隐私和资源约束

- CLI入口 `laodi hook --adapter … --state-dir …` 通过stdin接收JSON，stdout/stderr保持空，不返回allow/deny、不改工具输入、不注入模型上下文。生成的命令异步执行并在失败时保持成功退出。
- 工具原文只在当前进程内存中检查。不读取transcript_path、源文件或原始日志，不把疑似秘密放进argv、通知、临时文件、stdout或事件库。
- 输入最多1MiB、32层、16,384个JSON节点；检查该范围内所有候选，输出每类计数最多32。stdin超时2秒会记录缺口；这不是磁盘I/O的硬实时上限。
- 正常无命中调用不落盘。有命中时只提交固定分类、计数、未知项、客户端名；原始会话/调用ID使用私有随机密钥HMAC关联。
- 队列目录0700，文件0600；最多256条、每条16KiB。独立短锁等待最多约100ms；保存事件和去重状态后才确认消费，崩溃可重放。
- Hook独立保存最近2,048个去重键，不挤占快照预算，不因高频工具事件达到4,096条就退出。窗口外重放可能再次记录；事件列表仍最多256条。
- 同一证据组只通知最高优先级；同类限频10分钟，记录仍保留。历史gap持续可查，不等于当前所有检测都失效。
- 每次Hook由宿主启动一个短命进程；每次输入和队列有界，不代表任意数量宿主并发进程的总内存有固定上限。不能将0.1.2快照版资源数字当作新Hook并发资源承诺。

## 验证与真实支持范围

[检测器](../internal/laodi/hooks.go)、[私有队列](../internal/laodi/hook_inbox.go)、[安装器](../internal/laodi/hook_config.go)、[监测接入](../internal/laodi/runner.go)均为Go标准库实现，没有新运行依赖。

适配协议以[ZCode官方Hooks](https://zcode.z.ai/en/docs/hooks)和[Claude Code官方Hooks](https://code.claude.com/docs/en/hooks)为依据。已核查本机ZCode构建内的事件schema。安装器默认匹配Bash和Read；Grep、MCP、自定义工具、宿主索引和后台上传不因此被覆盖。检测器兼容ZCode的read_file输入，但默认配置未启用该别名。

```sh
go test -race ./...
go vet ./...
python3 scripts/dev/verify_tool_hooks.py \
  --binary bin/laodi --output docs/spikes/v02-tool-hook-results.json
```

回放使用临时HOME，实际安装/卸载合成配置，执行生成的shell Hook命令与Go监测器，检查四路并发、正常反例、截断、失败输入、重启、秘密不落盘及fake通知helper。它不会启动真实Agent、调用模型或发送系统通知。真实ZCode/Claude Code回调触发、宿主取消时的行为与系统横幅送达仍须单列验收，不能将协议回放等同实机完整通过。

本轮最终验证（2026-09-19）：

- 全套`go test -race ./...`、`go vet ./...`通过；通知helper构建和Clang静态分析通过。
- [实际生成命令的端到端结果](spikes/v02-tool-hook-results.json)：17次调用、四路并发，得到2条敏感访问请求、9条疑似凭据输出、2条覆盖缺口；正常反例不产生事件，重启无重复，原配置保留且卸载恢复。Hook输出流为空，持久化文件没有合成秘密或原始会话/调用/业务路径标识。
- 该小样本中Hook命令耗时中位约17ms、最大约64ms，包含启动与本地队列写入；不是宿主回调时延保证。
- [新构建的守护进程短测](spikes/v02-go-resource-results.json)：100份合成快照、22秒，RSS中位约10.72MiB，OS峰值约11.81MiB，FD 7–8，不含Hook子进程和通知helper。
- [两个独立Hook进程的短测](spikes/v02-hook-resource-results.json)：205字节和约0.91MB输入，峰值分别约7.30MiB、14.74MiB。不是任意并发总内存上限。
- [并行Git回归](spikes/v02-monitor-workflow-results.json)：12次Git操作完成，源HEAD/index符合预期。

二进制SHA-256统一为`95b85355f5216b468e3c992621e6cdbd06cd5b23e59e473852f98def6cc96919`；以上资源与功能结果对应同一构建。工具接入从“只观察快照元数据”扩展到“本机短暂检查输出内容”，已明确披露；没有安装或启用用户真实配置。

## 未来TODO：第二类及更广范围

- [ ] 合作式发送前正文接口或可选本地模型网关：仅覆盖明确接入的路径。
- [ ] 纯内存、无快照的上传观察；对TLS/应用层加密和旁路诚实标未知。
- [ ] 可选系统文件打开/网络事件关联，单独验证Apple权限、发行及额外资源成本。
- [ ] 更多产品/工具适配，逐项公布能力矩阵；不将Skill安装视为通用宿主防护。

当前不安装TLS代理、不改系统证书/代理、不抓用户流量、不承诺检测或阻断所有外传。
