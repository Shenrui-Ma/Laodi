<p align="center">
  <img src="assets/laodi-logo.png" width="180" alt="Laodi-skills 老底标志">
</p>
<h1 align="center">Laodi-skills · 老底</h1>

<p>
  <img src="assets/chat-demo.png" width="390" align="left" alt="聊天记录截图">
</p>

<h3>担心自己的 Git 历史被第三方工具悄悄上传？</h3>
<p>Laodi-skills 保护你老底。</p>
<p>一次接入，后台监控。</p>
<p>有小动作，及时提醒。</p>

<p align="right">
  <br><br><br><br>
  <img src="assets/closure.png" width="400" alt="可露希尔表情插画">
</p>

<br clear="all">

Laodi-skills 是 AI 编程工具的隐私监测器,监测已支持的仓库快照和上传线索，检查工具输出中的疑似凭据，通过系统通知提醒，并提供可供 Agent 查询的脱敏记录。

**当前为 Dev 版，需要一次接入和通知授权。**

## 能发现什么

| 检测范围 | 默认反馈 |
| --- | --- |
| 某APP的已支持快照记录：Git 对象、LFS、普通工作区及附加配置 | 记录新线索并通知；区分清单、上传尝试、客户端接受记录 |
| 某APP和其他编程工具中，已接入的 Bash、Read 请求里的敏感文件访问 | 仅记录，不弹通知 |
| 已接入工具的输出中出现疑似令牌、私钥或凭据赋值 | 保存脱敏记录并通知；不认定信息已被上传 |
| 持续解析异常、读取失败或事件队列缺口 | 记录覆盖缺口并限频提醒 |

正常 Git 操作和 Agent 长任务继续运行。默认无须管理员权限，不改命令、不拒绝工具调用、不修改代理。工具输入和输出只用于本地检测，不作为原文保存，事件中不保存密钥正文。

## 快速开始

需要 macOS、Go 1.25 或更新版本、Xcode Command Line Tools。先选定长期保留的目录：后台服务和 Hook 会引用构建产物的绝对路径，接入后移动目录需重新安装。

```sh
git clone https://github.com/Shenrui-Ma/Laodi-skills.git
cd Laodi-skills
make build
./bin/laodi check
```

按使用的编程工具选择适配器，具体标识和命令以本机帮助及[接入说明](docs/TOOL-HOOKS.md)为准。安装器保留其他已有配置；实际接入时加 `--apply`，否则只预览。

```sh
./bin/laodi hooks --help
```

授权系统通知，然后安装当前用户的后台服务：

```sh
./platform/macos/notifier/build/LaodiNotify.app/Contents/MacOS/LaodiNotify --request-permission
./bin/laodi setup --apply \
  --notifier "$PWD/platform/macos/notifier/build/LaodiNotify.app/Contents/MacOS/LaodiNotify"
```

**只需要工具输出检测时，在上面的 `setup` 命令中加上 `--hooks-only`**，跳过快照巡检。首次接入后，请在后续操作中保持相同的运行模式和状态目录。`setup` 去掉 `--apply` 同样只预览。

查询状态与记录：

```sh
./bin/laodi status
./bin/laodi incidents
./bin/laodi hooks status
```

把 [`skills/laodi`](skills/laodi) 放入客户端支持的 Skill 目录，并确保 `laodi` 可在该客户端的 `PATH` 中找到。**

## 架构

```text
已支持的快照文件 ───────────────────┐
                                  ↓
已接入编程工具的异步 Hook → 私有事件队列 → Go 守护进程
                                              ├─ 脱敏记录 → CLI / Skill 查询
                                              └─ macOS 通知辅助程序
```

## 验证状态

已完成合成证据测试、Go 竞态测试与静态检查；工具接入验证包含 17 次合成 Hook 调用、四路并发、异常输入、截断和重启去重，正常 Git 操作验证通过。

**仍待验收：真实客户端回调、系统横幅送达、长期稳定性，以及正式签名与分发。** 当前能力与剩余风险见[发布前检查](docs/RELEASE-REVIEW.md)。

## 卸载

先按照 `hooks --help` 移除已安装的适配器，再移除后台服务；去掉 `--apply` 可预览。卸载保留本地事件记录。

```sh
./bin/laodi hooks --help
./bin/laodi uninstall --apply
```

## TODO

- [ ] Windows 端适配。
- [ ] 纯内存上传观测。

## 文档

- [工具 Hook：接入协议、检测规则与隐私边界](docs/TOOL-HOOKS.md)
- [系统通知：授权、状态与送达边界](docs/NOTIFICATIONS.md)

## 许可证

原创代码与文档采用 [MIT License](LICENSE)。宣传 PNG 素材不自动纳入 MIT 授权，使用范围见 [素材说明](assets/README.md)。
