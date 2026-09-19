<p align="center">
  <img src="assets/laodi-logo.png" width="180" alt="Laodi-skills 老底标志">
</p>
<h1 align="center">Laodi-skills · 老底</h1>

<p>
  <img src="assets/chat-demo.png" width="390" align="left" alt="聊天记录截图">
</p>

<h3>担心自己的 Git 历史被第三方工具悄悄上传？</h3>
<p>Laodi-skills 保护你的 Git 老底。</p>
<p>一次接入，后台监控。</p>
<p>有小动作，及时提醒。</p>

<p align="right">
  <br><br><br><br>
  <img src="assets/closure.png" width="400" alt="可露希尔表情插画">
</p>

<br clear="all">

Laodi-skills 是 AI 编程工具的隐私监控，监测项目快照和数据上传，检查编程工具输出的疑似凭据，系统通知提醒，并提供可供 Agent 查询的脱敏记录。

**提供 macOS 预编译通用包，无需自行构建。** 

## 作用

| 检测范围 | 默认反馈 |
| --- | --- |
| 某APP和其他编程工具的快照记录：Git 对象、LFS、普通工作区等| 记录并通知 |
| 某APP和其他编程工具中，已接入的 Bash、Read 请求里的敏感文件访问 | 仅记录，不弹通知 |
| 已接入工具的输出中出现疑似令牌、私钥或凭据赋值 | 保存脱敏记录并通知 |
| 持续解析异常、读取失败或事件队列缺口 | 记录并限频提醒 |

不影响正常 Git 操作和 Agent 长任务，默认无须管理员权限，不改命令、不拒绝工具调用、不修改代理。工具输入和输出只用于本地检测，不作为原文保存，不保存密钥正文。

## 安装和更新

**macOS**：

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi-skills/main/install.sh | sh
```

已安装时会更新，保留配置和事件记录。

[Release 与校验文件](https://github.com/Shenrui-Ma/Laodi-skills/releases/tag/v0.3.0-preview.2) · [查看安装脚本](install.sh) · [详细说明](docs/BINARY-INSTALL.md)

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

## 卸载

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" remove
```

## TODO

- [ ] Windows 端适配。
- [ ] 纯内存上传观测。

## 文档

- [工具 Hook：接入协议、检测规则与隐私边界](docs/TOOL-HOOKS.md)
- [系统通知：授权、状态与送达边界](docs/NOTIFICATIONS.md)

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
