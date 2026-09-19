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

Laodi-skills 是 AI 编程工具的隐私监控，监测已支持的仓库快照和上传线索，检查编程工具输出的疑似凭据，通过系统通知提醒，并提供可供 Agent 查询的脱敏记录。

**当前提供 macOS 预编译通用包，无需自行构建。** 预览版本 `v0.3.0-preview.1`。

## 能发现什么

| 检测范围 | 默认反馈 |
| --- | --- |
| 某APP和其他编程工具的快照记录：Git 对象、LFS、普通工作区等| 记录并通知 |
| 某APP和其他编程工具中，已接入的 Bash、Read 请求里的敏感文件访问 | 仅记录，不弹通知 |
| 已接入工具的输出中出现疑似令牌、私钥或凭据赋值 | 保存脱敏记录并通知 |
| 持续解析异常、读取失败或事件队列缺口 | 记录并限频提醒 |

不影响正常 Git 操作和 Agent 长任务，默认无须管理员权限，不改命令、不拒绝工具调用、不修改代理。工具输入和输出只用于本地检测，不作为原文保存，事件中不保存密钥正文。

## 一条命令安装

适用于 **macOS 13+，Apple Silicon / Intel 通用**：

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi-skills/main/install.sh | sh
```

脚本自动下载预编译 Release、校验 SHA-256、解压并完成接入。无需手动下载 ZIP，也不需要 Go、Node.js、Python 或 sudo。安装器会配置已检测到的支持工具与后台监测，不重启当前 Agent；首次按系统提示选择通知权限。

[Release 与校验文件](https://github.com/Shenrui-Ma/Laodi-skills/releases/tag/v0.3.0-preview.1) · [查看安装脚本](install.sh) · [详细说明](docs/BINARY-INSTALL.md)

预览版尚无 Developer ID 签名与 Apple 公证，首次运行可能需要系统确认；脚本不会关闭系统安全保护。未检测到支持工具时会明确提示尚未接入。

查看状态：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" status
```

Skill 随包提供，可按需接入；后台检测独立运行。

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

**仍待验收：真实客户端回调、系统横幅送达、长期稳定性，以及正式签名与公证。** 当前能力与剩余风险见[发布前检查](docs/RELEASE-REVIEW.md)。

## 卸载

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" remove
```

只移除老底自己的 Hook 和后台服务，保留本地记录与运行文件。预览版暂不自动覆盖已有不同版本的安装。

## TODO

- [ ] Windows 端适配。
- [ ] 纯内存上传观测。

## 文档

- [工具 Hook：接入协议、检测规则与隐私边界](docs/TOOL-HOOKS.md)
- [系统通知：授权、状态与送达边界](docs/NOTIFICATIONS.md)

<details>
<summary>开发者：从源码构建与测试</summary>

需要 Go 1.25+ 和 Xcode Command Line Tools；普通使用者直接下载 Release 即可。

```sh
git clone https://github.com/Shenrui-Ma/Laodi-skills.git
cd Laodi-skills
make build
make test
make check
```

[开发说明](docs/DEVELOPMENT.md) · [发布构建](scripts/release)

</details>

## 许可证

原创代码与文档采用 [MIT License](LICENSE)。宣传 PNG 素材不自动纳入 MIT 授权，使用范围见 [素材说明](assets/README.md)。
