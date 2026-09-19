# 开发版使用与离线验证

当前开发线：`0.3.0`。普通使用者请直接下载预编译Release，见[二进制安装](BINARY-INSTALL.md)；以下构建命令面向开发者。已有Go命令行、快照与工具事件监测、持久化去重、薄Skill和无窗口通知helper。新增ZCode/Claude Code异步工具适配，详见[接入说明](TOOL-HOOKS.md)。实机回调、完整上传流程、系统通知送达和长期稳定性仍未全部验收，不是正式发布版。

## 构建

Go 1.25或更高；本轮使用经官方SHA-256校验的Go 1.27.1工具链。核心没有第三方Go依赖，最终用户运行编译好的二进制不需要Go或Node。

```bash
go test -race ./...
go vet ./...
go build -trimpath -o bin/laodi ./cmd/laodi
./bin/laodi --help
```

macOS通知helper另行构建，不会请求权限或发送通知：

```bash
sh platform/macos/notifier/build.sh
```

## 当前解析范围

- 已静态核对的ZCode构建：`3.12.3.7463`，详见[契约](spikes/ZCODE-3.12.3-CONTRACT.md)。
- 默认从已安装App的Info.plist发现构建；监测期间文件变化会重新检查。未知构建明确降级。
- 读取已知checkpoint下的manifest/state；不跟随state中的任意绝对路径，不读取manifest指向的源码或Git对象。
- 历史事件针对清单中`.git/objects/…`与`.git/lfs/objects/…`路径及对应状态；`.git`指针、HEAD、config、reflog不会被当成历史对象。另独立识别extra清单global-configs组，即使工作区没有Git也可产生附加配置证据。
- 不含Git历史对象的非空工作区清单也可见，使用`workspace_snapshot_*`三阶段；与Git历史分类互斥。它不代表内容包含秘密或上传未经授权，也不因用户日常执行Git命令就触发。
- `attemptCount`只表明客户端进入尝试流程；`lastAcceptedManifestHash`必须关联其自己的清单，不能套用新pending。
- extra解析只提取global-configs条目计数，并用nextExtraManifestHash/lastAcceptedExtraManifestHash关联自己的阶段；不读取配置本体，不输出文件名、contentHash、source或配置值。不检查网络正文，不验证远端保存、删除或训练用途。
- 上限：128个顶层目录条目、每工作区256个历史目录项、每轮256个证据文件、单文件8MiB、每轮新读取32MiB。当前state引用优先、公平轮转；达到上限明确degraded，不宣称完整取证。

## 查询与前台监测

以下默认查询的是用户明确选择使用的本地ZCode证据位置。[已完成的用户授权只读观察](spikes/ZCODE-LIVE-OBSERVATION.md)与合成测试分开记录；开发回归只用临时目录。

```bash
./bin/laodi check --format agent-summary
./bin/laodi status --format agent-summary
./bin/laodi incidents --format agent-summary
./bin/laodi doctor --format agent-summary
```

前台运行示例：

```bash
./bin/laodi watch --interval 2s
```

`watch`只运行Laodi，不启动、暂停、终止或限制ZCode/Agent。首次已有记录作为基线，后续新观察记录持久化。退出用Ctrl-C；状态目录保持用户私有权限，另一个监测实例不能同时写同一状态。

macOS用户级后台接入接口已实现；本轮只用假launchctl执行器和临时目录验证，未在用户系统安装：

```bash
./bin/laodi setup                 # 只预览，不注册
./bin/laodi setup --apply         # 用户明确接入时才执行
./bin/laodi uninstall             # 只预览
./bin/laodi uninstall --apply     # 只移除本工具有ownership记录且未被改动的服务
```

不使用sudo。不同设置不会静默覆盖正在运行的服务；未知/被修改的plist拒绝覆盖。卸载保留事件数据，不递归清理用户目录。launchd提交成功不等于监测正常，仍需查看status/doctor；真实登录启动、系统后台项目确认及72小时运行未测。

`setup --apply`接入成功后立即执行只读检查并输出已有证据，避免静默基线掩盖安装前的问题。这个输出不是新上传时间线。通知权限须通过`LaodiNotify --status`单独查询，`doctor`当前不会代查。未知版本或降级仍按实际返回显示。

默认不投递系统通知。启用通知前需要完成[通知接入](NOTIFICATIONS.md)；再为watch显式传入已构建helper的绝对路径。查询命令不请求通知权限。

## 不打断任务与通知恢复

- 同一快照同轮出现多个阶段，只选最高阶段进行一次通知，全部证据仍保留。
- 持续覆盖异常超过三轮检查后产生限频提醒；恢复后同原因再次失效仍可形成新事件。
- `queued`在进程重启后可恢复；发送前先持久化`dispatching`再允许helper执行。
- `dispatching`期间崩溃的送达状态未知，重启标`unknown_after_restart`而不盲目重复通知。OS接受不等于用户已看到。
- 损坏文件不会阻止其他有效证据建立基线或产生新事件；不完整扫描后首次发现的记录会标明发生时间未知。
- 无事件时完整状态最多每30秒刷新一次，避免每2秒重写整个去重记录。正常退出状态标为停止；异常退出靠心跳超时识别，不能当成即时存活证明。

## 合成端到端测试

```bash
python3 scripts/dev/verify_monitor_workflow.py \
  --binary bin/laodi \
  --output docs/spikes/v1-monitor-workflow-results.json
```

测试并行运行实际Go监测器与12次Git操作，检查旧记录静默、新事件出现、重启去重、Agent摘要不含项目路径、监测不改源HEAD/index。数据形状来自已安装ZCode包的静态契约，但测试不启动ZCode、不调用模型、不发送系统通知。

公开案例的持续监测回放（默认2秒间隔）：

```bash
python3 scripts/dev/replay_public_cases.py \
  --binary bin/laodi \
  --output docs/spikes/v1-public-case-replay-results.json
```

它记录从合成清单/状态落盘到Laodi事件保存的延迟；不测系统通知送达，也不证明能先于真实上传。分类回归另覆盖42,411条清单、失败次数、旧新hash、无Git接受记录、附加配置、pending清理及升级基线，详见[案例报告](spikes/ZCODE-PUBLIC-CASES.md)。

资源测量：

```bash
python3 scripts/dev/measure_go_monitor.py
python3 scripts/dev/measure_go_monitor.py --manifest-count 1 --files-per-manifest 42411 \
  --output docs/spikes/v1-public-case-large-resource-results.json
```

执行期间不要覆盖二进制。22秒短测不替代72小时稳定性、真实快照负载或通知helper资源测量。

新增分类的升级基线只静默一次，既有Git/配置事件继续去重、新事件照常出现。若升级首次扫描不完整，后续发现会标记时间未知；不会为了等待完整扫描而永久静默。旧版未记录的parser显示unknown，不借当前CLI版本伪装历史解析结果。

## 为真实ZCode测试准备环境

```bash
python3 scripts/dev/prepare_zcode_lab.py --output .omx/zcode-lab.json
```

它只准备独立应用数据目录、合成Git仓库、手动启动脚本和测试任务，不启动或登录ZCode。目录覆盖不是OS安全隔离，不能隔离Keychain、浏览器和系统账号；需要更强保证时使用独立OS用户/VM。

用户随后已在日常ZCode实例登录并授权只读观察；本轮没有启动上述独立实例。用户无需在真实项目中诱发敏感上传。未来真实客户端对照应使用独立OS用户/VM和合成内容，先验证实例/登录回调落点。没有真实checkpoint不能据此判断已修复，也不能证明适配器端到端有效。

## 数据和失败边界

状态只存私有事件摘要、分类指纹和证据位置，事件最多256条。快照去重键最多4096；达到上限会保留已知游标、报告容量降级并跳过无法跟踪的新快照，Hook继续运行；当前尚无快照自动归档。Hook另有最近2048键窗口，超窗只淘汰旧Hook游标，监测继续；窗口外重复可能再次记录。

`agent-summary`使用独立白名单结构，不输出源码、完整清单、原始客户端自由文本、项目路径或远端地址。它不是可证明防篡改审计日志。
