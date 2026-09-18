# 通知与 Skill：低打扰接口

本文件实现 [V1 产品约束](PRODUCT-V1.md)，记录已经提交的代码与仍待验证的能力。当前提供薄 Skill、macOS 无窗口 helper 源码和构建脚本；**源码构建通过不代表系统通知链路已经可用**。本轮不请求通知权限，不发送测试通知，不安装服务，不签名真实开发者身份。

本轮验证：clang `-Wall -Wextra -Werror` 构建成功；Clang Static Analyzer 无报告；`Info.plist` 通过 `plutil -lint`；构建脚本通过 `sh -n`。后续只读`--status`已返回not_determined/not_requested；尚未请求通知授权或测试送达。开发二进制大小不代表运行内存或发布包体积。

## 产品行为

1. Guardian 先持久化脱敏事件，再按重要性与去重策略决定通知；通知失败不丢事件。
2. 调用短时运行的通知 helper。失败只更新通知通道状态，不影响监测循环与 Agent。
3. 系统按用户许可、横幅设置、专注模式和其他通知偏好决定呈现。
4. 用户随后可执行 `laodi incidents`，或在支持 Skill 的 Agent 中询问“老底刚才发现了什么”。

不抢焦点，不要求用户确认后开发任务才能继续；不取消命令，不改变权限，不处理 Git，不改网络配置，不往正在运行的 Agent 会话注入提示词。无需主窗口、Web 管理界面、常驻菜单栏或模型服务。

## Helper 实现与调用

代码位于 `platform/macos/notifier/`。Objective-C + Foundation + UserNotifications，无第三方依赖，最低编译目标 macOS 12。打包为 `LSUIElement` 应用，不创建主窗口。`local.laodi.notify` 是开发身份；发布前须确定自有稳定的 bundle ID，之后避免因升级改变身份造成通知授权丢失。

### 状态查询：没有授权请求

```text
LaodiNotify.app/Contents/MacOS/LaodiNotify --status
```

这条命令只调用 `getNotificationSettingsWithCompletionHandler:`。常规 `status`/`doctor` 可以使用；不得自动退回 `--request-permission`。当前 CLI 若尚未接入 helper，就显示“通知尚未接入/未经验证”，不能用 `osascript` 可执行或 bundle 存在作为已授权的证据。

### 显式接入通知：可能显示一次系统权限对话框

```text
LaodiNotify.app/Contents/MacOS/LaodiNotify --request-permission
```

这是唯一请求授权的代码路径。只请求普通 alert，不请求声音、角标、关键提醒、时间敏感提醒或任何系统扩展权限。用户同意并不保证横幅立即可见；拒绝则保留查询和本地记录功能，不重复请求、不替用户操作系统设置。

### 事件通知：不会自行请求授权

```text
LaodiNotify.app/Contents/MacOS/LaodiNotify --send --id opaque_incident_id --kind snapshot-history
```

`--id` 为 1–64 位 ASCII 字母、数字、下划线或横线，必须由核心生成不含项目、路径或秘密的随机/不可逆事件标识；不要传可猜测路径的纯明文编码。不要把同一事件的每次轮询当作新的通知。相同 ID 再提交仍可能重新提醒，去重必须由 Guardian 完成。

`--kind` 仅接受下列枚举。没有 `--title`、`--body`、任意 JSON payload 或打开任意命令的动作。

| 枚举 | 使用条件 | 提醒含义 |
|---|---|---|
| `snapshot-history` | 已支持清单中明确有 Git 历史对象路径 | 本地快照线索，上传成功未知 |
| `upload-attempt` | 当前版本已验证的字段确实表示尝试或重试 | 有上传尝试，不等于成功 |
| `upload-accepted` | 已验证字段、清单关联及写入路径支持该含义 | 客户端接受记录，远端保存未经核验 |
| `snapshot-workspace` | 非空普通工作区清单，未匹配Git历史对象 | 其他工作区快照；是否含秘密、是否获用户授权未知 |
| `workspace-upload-attempt` | 对应普通清单的尝试字段为正 | 进入过尝试流程，不保证发出HTTP请求 |
| `workspace-upload-accepted` | 接受hash与自己的普通清单匹配 | 普通快照的客户端HTTP成功记录，不谎称Git历史泄漏 |
| `snapshot-config` | extra manifest的global-configs组有条目 | 附加配置被列入快照，内容/秘密与上传状态未知 |
| `config-upload-attempt` | 对应extra hash的当前槽attemptCount为正 | 进入过尝试流程，不保证发出HTTP请求 |
| `config-upload-accepted` | lastAcceptedExtraManifestHash与自己的extra清单匹配 | 客户端HTTP成功记录，不能推断具体配置内容/留存 |
| `coverage-degraded` | 原先有效的监测停止、解析器失效等重要变化 | 监测存在缺口，不能据此宣称泄露 |
| `tool-output-sensitive` | 支持的工具输出包含疑似凭据特征 | 输出有风险；不确认进入模型请求或上传成功 |
| `hook-coverage-degraded` | 工具输出不完整、输入/队列异常等 | 部分工具事件可能未完整检查，任务继续 |

`--existing` 在正文前加“安装前已有记录”。首次扫描不逐条刷屏，旧记录是否发一次汇总由 Guardian 负责。当前 helper 只负责提交单个通知，不完成去重、限速、扫描或事件存储。

普通无声音通知采用 active interruption level，可以按系统偏好呈现非模态横幅，不绕过 Focus。helper 不使用 critical/time-sensitive 提醒，不提供“点击后中止任务”或执行 shell 的按钮。点击行为仍须在正式包上验证；目前无需点击才能查看详情，通知正文说明了只读查询入口。

### JSON 响应

每次正常 API 完成返回一行 JSON；异常返回固定错误码，不输出原始系统错误文本、目录或用户内容。示例：

```json
{
  "schema_version": 1,
  "action": "send",
  "ok": true,
  "authorization": "authorized",
  "alert_setting": "enabled",
  "notification_center_setting": "enabled",
  "lock_screen_setting": "enabled",
  "delivery": "accepted_by_os",
  "task_interrupted": false
}
```

- `action`：`status`、`request-permission`、`send`；无效输入为 `invalid`。
- `authorization`：`not_determined`、`denied`、`authorized`、`provisional`、`unknown`；API 未返回时字段缺省。
- `*_setting`：`enabled`、`disabled`、`not_supported`、`unknown`。
- `delivery`：`not_requested`、`not_available`、`accepted_by_os` 或 `unknown`。
- `granted`：仅权限请求结果提供；拒绝是一个有效请求结果，需要结合该字段与 authorization 判断，不只看 `ok`。
- `error_code`：固定错误类别，仅出错时出现。
- 退出码：0 表示查询/请求正常完成；2 表示参数无效；3 表示不可用或服务失败。拒绝授权的请求可以正常完成并返回 0，不得将它视作已同意。

`accepted_by_os` 只表示 UserNotifications 接受请求，**不是 delivered、shown 或 user_seen**。Focus 和其他系统策略仍可能影响呈现。提交超时显示 `unknown`，不能自动反复重发制造通知风暴。

Guardian 应通过参数数组直接启动 helper，设置独立进程超时和有限输出大小，不经 shell 拼接内容。查询/发送 helper 内部 10 秒超时；显式权限请求 60 秒超时，超时只表示未知。子进程等待不能阻塞文件检测主循环；队列有界，失败使用退避。

## Skill 的职责

`skills/laodi/SKILL.md` 采用通用 Agent Skills 结构，不绑定单一模型厂商，不创建 Codex 插件清单。它只通过 `status/check/incidents/doctor --format agent-summary` 查询受约束摘要。

它不通过模型推断一次上传是否成功，也不主动读取秘密或原始 Git 历史。仅当用户要求安装、开启提醒或处置时进入对应流程；查询失败不会自动提权。安装 Skill 不代表 Guardian 已运行，安装 Guardian 也不代表通知已授权。

摘要的当前实现契约以 CLI 为准，后续版本应包含可判断覆盖状态的信息，至少区分：未安装、未运行、初始扫描未完成、支持范围有效、格式未知、目录不可读、通知通道不可用。它们都不是“没有风险”的同义词。

## 必须执行但本轮不执行的验收

| 场景 | 通过标准 |
|---|---|
| 全新包首次 `--status` | 无窗口、无权限弹窗、无通知；正确返回 not_determined |
| 用户主动接入 | 仅一个系统通知授权请求；不要求 root/FDA/ES |
| 用户拒绝 | 不重试授权；事件仍可回查；CLI 显示通知不可用 |
| 已授权发送合成事件 | 横幅或通知中心按偏好显示固定脱敏文案；Agent 长任务照常完成 |
| Focus / 横幅关闭 | 不能错误显示 user_seen；事件保留，不刷屏补发 |
| 锁屏 | 不显示路径、项目名、秘密和任意源日志 |
| 重复重试和旧记录 | Guardian 去重、限速和旧记录标记正确；状态升级才产生新提醒 |
| helper 缺失、异常、卡住 | 检测持续运行，有界退避；状态准确降级 |
| 长任务和 Git 对照 | 安装前后原命令退出码和成果一致，不引入阻断/重启 |
| 包位置变化和升级 | 稳定身份和授权可验证；若授权失效则准确报告 |
| 分发包 Gatekeeper 检查 | Developer ID、公证与实际安装流程通过；不把开发构建当分发成品 |

普通用户通知能力不需要 Endpoint Security entitlement 或系统扩展。Developer ID 签名和公证属于后续正式分发的身份与可信安装问题；不能据此让用户现在为测试安装 root 服务或关闭 SIP。

## 依据

- [Apple：请求通知许可](https://developer.apple.com/documentation/usernotifications/asking-permission-to-use-notifications)
- [Apple：LSUIElement 无 Dock 界面的后台应用](https://developer.apple.com/documentation/bundleresources/information-property-list/lsuielement)
- 本机 macOS SDK 的 `UNUserNotificationCenter.h`、`UNNotificationSettings.h`、`UNNotificationContent.h`：查询、授权与请求为独立 API；通知级别受系统设置控制。
- [Agent Skills 规范](https://agentskills.io/specification)：通用 `SKILL.md` 和按需参考资料结构。
