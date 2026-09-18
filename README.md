<p align="center">
  <img src="assets/laodi-logo.png" width="112" alt="Laodi-skills 老底标志">
</p>
<h1 align="center">Laodi-skills · 老底</h1>
<p align="center"><strong>AI 安心写，老底替你盯。</strong></p>

<table>
  <tr>
    <td width="29%" align="center" valign="middle">
      <img src="assets/chat-demo.svg" width="260" alt="概念聊天演示：老底提示发现 Git 历史快照线索，开发任务继续运行">
      <br><sub>概念演示，非真实日志</sub>
    </td>
    <td width="39%" align="center" valign="middle">
      <strong>担心自己的 Git 历史<br>被第三方工具悄悄上传？</strong>
      <br><br>让 Laodi-skills 替你留意。
      <br><br>一次接入，后台监控。
      <br>发现线索，及时提醒。
    </td>
    <td width="32%" align="center" valign="middle">
      <img src="assets/closure.png" width="300" alt="可露希尔表情插画">
    </td>
  </tr>
</table>

Laodi-skills 是 AI 编程工具的**本地隐私监测器**。它监测已支持的仓库快照和上传线索，检查工具输出中的疑似凭据，通过系统通知提醒，并提供可供 Agent 查询的脱敏记录。

**当前为 `0.2.1-dev` 开发版，提供 macOS 源码构建。** 需要一次接入和通知授权；尚无正式签名的发行包。它不会拦截上传，也不会把“未发现线索”当作“没有上传”。

## 能发现什么

| 检测范围 | 默认反馈 |
| --- | --- |
| ZCode `3.12.3.7463` 的 Git 对象、LFS、普通工作区及附加配置快照 | 记录新线索并通知；区分清单、上传尝试、客户端接受记录 |
| ZCode / Claude Code 的 Bash、Read 请求中可识别的敏感文件访问 | 仅记录，不弹通知 |
| 已接入工具的输出中出现疑似令牌、私钥或凭据赋值 | 保存脱敏记录并通知；不认定已上传 |
| 持续解析异常、读取失败或事件队列缺口 | 记录覆盖缺口并限频提醒 |

正常 Git 操作和 Agent 长任务继续运行。默认无须管理员权限，无专用 GUI；不改命令、不拒绝工具调用、不修改代理、不发送遥测。工具输入和输出只用于本地检测，不作为原文保存，事件中不保存密钥正文。

系统通知使用横幅，无须确认后才能继续。重复事件会去重、限频，已有记录可随时查询。通知权限、专注模式和系统状态可能影响横幅展示，不能保证用户已经看到。

**覆盖边界：** 异步检测可能晚于后续模型请求；客户端纯内存上传、未接入工具及其他宿主后台路径尚不覆盖。Skill 是查询与接入入口，检测不依赖大模型或提示词。

## 快速开始

需要 macOS、Go 1.25 或更新版本、Xcode Command Line Tools。先选定长期保留的目录：后台服务和 Hook 会引用构建产物的绝对路径，接入后移动目录需重新安装。

```sh
git clone https://github.com/Shenrui-Ma/Laodi-skills.git
cd Laodi-skills
make build
./bin/laodi check
```

`check` 执行一次 ZCode 快照检查。不存在支持的证据目录时会明确显示未覆盖，而非宣称安全。

按使用的客户端选择接入命令；去掉 `--apply` 可先查看变更计划。安装器保留其他已有配置。

```sh
./bin/laodi hooks install --adapter zcode --apply
# 使用 Claude Code 时：
./bin/laodi hooks install --adapter claude-code --apply
```

授权系统通知，然后安装当前用户的后台服务：

```sh
./platform/macos/notifier/build/LaodiNotify.app/Contents/MacOS/LaodiNotify --request-permission
./bin/laodi setup --apply \
  --notifier "$PWD/platform/macos/notifier/build/LaodiNotify.app/Contents/MacOS/LaodiNotify"
```

**只使用 Claude Code 时，在上面的 `setup` 命令中加上 `--hooks-only`**，跳过 ZCode 快照巡检。首次接入后，请在后续操作中保持相同的运行模式和状态目录。`setup` 去掉 `--apply` 同样只预览。

查询状态与记录：

```sh
./bin/laodi status
./bin/laodi incidents
./bin/laodi hooks status
```

状态会显示通知及监测信息。空的 Hook 事件列表只表示尚未收到事件，不能证明客户端回调正常。

把 [`skills/laodi`](skills/laodi) 放入客户端支持的 Skill 目录，并确保 `laodi` 可在该客户端的 `PATH` 中找到，即可让 Agent 查询“老底最近发现了什么”。**只安装 Skill 不会自动启动监测。**

## 架构

```text
ZCode 快照文件 ─────────────────────┐
                                  ↓
ZCode / Claude Code 异步 Hook → 私有事件队列 → Go 守护进程
                                              ├─ 脱敏记录 → CLI / Skill 查询
                                              └─ macOS 通知辅助程序
```

一个 Go 常驻进程，薄 Hook 按事件短暂运行，共用检测、去重和通知逻辑。Go 核心没有第三方运行依赖；不需要数据库服务、云端账号或额外网络代理。队列和状态按用户隔离，并限制读取量与存储容量。

## 验证状态

已完成合成证据测试、Go 竞态测试与静态检查；工具接入验证包含 17 次合成 Hook 调用、四路并发、异常输入、截断和重启去重，正常 Git 操作验证通过。合成测试不等于真实客户端验收。

**仍待验收：真实客户端回调、系统横幅送达、长期稳定性，以及正式签名与分发。** 当前能力与剩余风险见[发布前检查](docs/RELEASE-REVIEW.md)。

## 卸载

仅对已安装的适配器执行对应命令，再移除后台服务；去掉 `--apply` 可预览。卸载保留本地事件记录。

```sh
./bin/laodi hooks uninstall --adapter zcode --apply
./bin/laodi hooks uninstall --adapter claude-code --apply
./bin/laodi uninstall --apply
```

## 文档

- [工具 Hook：接入协议、检测规则与隐私边界](docs/TOOL-HOOKS.md)
- [系统通知：授权、状态与送达边界](docs/NOTIFICATIONS.md)
- [ZCode 公开案例：原始证据与覆盖矩阵](docs/spikes/ZCODE-PUBLIC-CASES.md)
- [检测扩展：后续适配与纯内存上传 TODO](docs/DETECTION-EXTENSIONS.md)

## 许可证

原创代码与文档采用 [MIT License](LICENSE)。宣传 PNG 素材不自动纳入 MIT 授权，使用范围见 [素材说明](assets/README.md)。
