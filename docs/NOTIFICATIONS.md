# 提醒出现后怎么办

老底自动检测、保存脱敏事件并提交系统通知。已启用的归档限制由文件系统执行，不需要 Agent 在线或点击通知。默认监测不会拒绝工具调用；通知也不会自动触发 Agent、轮换密钥、关闭客户端或删除远端数据。

## 当前通知原文

下表对应源码中的固定标题和正文；两平台共有类型使用相同文案。通知不显示项目名、文件路径或密钥原值。客户端记录不等于独立网络抓包，清单不等于已经打包或上传，上传确认也不证明远端留存。

| 类型 | 标题 | 正文 |
| --- | --- | --- |
| archive-blocked-test（macOS 主动自检） | 测试打包操作已拦截 | Git 历史打包保护正常。 |
| snapshot-history | 发现：Git 历史打包 | 某APP的打包清单包含 Git 历史，上传情况待确认。 |
| upload-attempt | 发现：Git 历史上传尝试 | 某APP记录了 Git 历史上传尝试，结果待确认。 |
| upload-accepted | 发现：Git 历史上传确认 | 某APP记录了 Git 历史上传成功，远端留存情况未知。 |
| snapshot-workspace | 发现：项目文件打包 | 某APP已生成项目文件的打包清单，上传情况待确认。 |
| workspace-upload-attempt | 发现：项目文件上传尝试 | 某APP记录了项目文件上传尝试，结果待确认。 |
| workspace-upload-accepted | 发现：项目文件上传确认 | 某APP记录了项目文件上传成功，远端留存情况未知。 |
| snapshot-config | 发现：用户配置打包 | 某APP的打包清单包含用户配置，上传情况待确认。 |
| config-upload-attempt | 发现：用户配置上传尝试 | 某APP记录了用户配置上传尝试，结果待确认。 |
| config-upload-accepted | 发现：用户配置上传确认 | 某APP记录了用户配置上传成功，远端留存情况未知。 |
| tool-output-sensitive | 发现：疑似密钥输出 | 工具返回的内容包含疑似密钥，是否发送给模型未知。 |
| hook-coverage-degraded | 发现：工具监测不完整 | 部分工具事件可能未被完整检查。 |
| coverage-degraded | 发现：部分监测不可用 | 部分已接入来源暂时无法正常监测。 |
| test（Windows 测试） | 老底：通知测试 | 这是一条测试通知。 |

打包保护异常只保存记录，可通过 `status`、`incidents` 和 `protect status` 查询，不发送弹窗。

敏感文件的**访问请求默认只记录，不弹通知**；它不能证明读取已经完成。同一快照在一轮检查中优先显示证据最强的阶段；同类通知通常在 10 分钟内合并。首次安装已有记录和升级基线静默保留，不逐条弹窗。

`--existing` 可给 helper 正文增加“安装前已有记录”前缀，不能将旧记录描述成刚刚上传。客户端 `attemptCount` 不是 HTTP 请求次数；“上传接受记录”也不证明远端长期留存、训练或公开。

## 老底做了什么，还需要谁处理

| 情况 | 老底自动完成 | 用户或受委托 Agent 的下一步 |
| --- | --- | --- |
| 敏感访问请求 | 仅记下风险类别 | 若属于明确授权的任务可继续；不需要为了路径命中中断任务 |
| 工具输出疑似凭据 | 脱敏记录、通知 | 核对任务是否本就使用合成值或凭据；若真实凭据可能暴露，到服务商撤销或轮换，不要把原值再贴给 Agent |
| Git、工作区或配置快照 | 记录清单类别与状态 | 查 `incidents` 和 `protect status`；不希望产生额外归档时，按下面的限制流程处理 |
| 上传尝试记录 | 保存尝试阶段，提醒 | 不等于实际发出请求；查保护是否启用。已经存在的其他上传路径不能靠这条通知自动阻断 |
| 上传接受记录 | 保存客户端确认线索，提醒 | 本地记录先保留；需要远端删除或凭据处置时由用户操作相应服务。老底没有云端撤回能力 |
| 工具或快照监测缺口 | 保留诊断、限频通知 | 查 `status`、`doctor`、`hooks status`。不要用 sudo、全盘权限或关闭代理作为默认修复 |
| 打包保护异常（仅记录） | 检查到健康状态异常，保存记录 | 查 `protect status`；未知客户端不强行启用。需要恢复时等客户端正常退出再处理 |
| 已启用的固定路径归档被权限拒绝 | 文件系统阻止相关操作 | 不需要 Agent 代为执行阻断。当前不记录逐次阻断计数，也没有“刚拦截一次”的通知；不能据 enabled 推算次数 |

### 先查脱敏记录

macOS 安装器不修改 PATH。以下命令可直接使用：

```sh
laodi_bin="$HOME/Library/Application Support/Laodi-skills/runtime/laodi"
"$laodi_bin" incidents --format agent-summary
"$laodi_bin" status --format agent-summary
"$laodi_bin" protect status --format agent-summary
```

想交给已接入老底 Skill 的 Agent，可以说：

> 查看老底最近的脱敏提醒和保护状态，解释发生到了哪一步，并告诉我对应处理办法。不要读取密钥、源码或完整客户端日志，也不要中断当前任务。

Skill 查询同一个本地 CLI，不会从系统通知自动接收任务。没有 Skill 时，上面的终端命令也能查；无需购买额外模型服务。摘要有意不包含文件名和密钥，Agent 不能凭空指出具体哪个 key 泄露。

### 想限制后续额外归档

只适用于已核验客户端的固定路径。先让当前任务完成并正常退出客户端，再执行：

```sh
"$laodi_bin" protect enable
```

然后照常打开客户端；以后由文件系统自动执行这组限制。它不能阻止此前的读取、普通模型请求、其他工具的自有缓存或内存上传。其他产品应先核实它的相关功能开关与路径，不能直接把当前保护适配器套过去。

状态为 `recovery_needed` 时，保持客户端退出后执行 `"$laodi_bin" protect disable`，恢复老底自己添加的权限。不要清空整个 ACL 或删除恢复记录。其他异常先按 [保护说明](PROTECTION.md) 判断；没有一条适用于所有异常的“强制修复”命令。

### 为什么没有横幅

```sh
"$laodi_bin" doctor --format agent-summary
"$laodi_bin" hooks status
"$HOME/Library/Application Support/Laodi-skills/runtime/LaodiNotify.app/Contents/MacOS/LaodiNotify" --status
```

`doctor` 检查支持范围和目录，不修复配置，也不查询通知许可；后台是否存活看 `status`，通知设置看 helper。若 helper 返回 `denied`，到系统设置 → 通知 → 老底，开启允许通知及横幅。只有状态为 `not_determined` 且用户希望开启时，才使用 helper 的 `--request-permission` 请求一次授权。

`accepted_by_os` 表示系统接受通知请求，不等于用户看到了。专注模式、横幅设置和系统策略可能隐藏通知。缺失、拒绝、失败或超时都不能算成功显示；事件仍可通过 CLI 回查。守护进程完全停止后也不能依靠它自己发出停止提醒。

当前已有一次真实客户端 Bash 风险事件及用户确认可见通知的验收；该用户在安装后手动开启了通知。它不证明首次安装一定能自动弹出权限申请，也不证明各系统配置下必然可见。

## 授权内与授权外

- 指定文件并要求读取、修改，允许完成该任务所需的访问；只提及文件路径不自动授权全文上传。
- 指定 Git 提交分析，允许必要的历史读取；普通 Git 内部对象访问不等于整库备份。
- 给 key 用于服务 A 的认证，不自动允许回显或发送给服务 B。
- 明确开启指定云备份，范围内的后台发送可以是授权内行为。
- 缺少任务与设置证据时标为“待核实”；敏感路径、系统权限或后台发生本身都不足以认定擅自访问。

这是解释事件的规则。当前程序没有自动用户意图或授权分类器；授权内的敏感操作仍可能产生风险记录。

## 实现接口

通知由 `platform/macos/notifier/main.m` 提交给 UserNotifications。helper 支持 `--status`、`--request-permission`、`--send --id ... --kind ...`；没有自定义文案、任意命令或“一键中断”的动作按钮。查询与发送不会自行请求授权。检测器先持久化记录，再异步调用 helper；通知失败不阻塞检测循环。

[Apple 通知许可说明](https://developer.apple.com/documentation/usernotifications/asking-permission-to-use-notifications) · [保护与恢复](PROTECTION.md) · [工具接入范围](TOOL-HOOKS.md)
