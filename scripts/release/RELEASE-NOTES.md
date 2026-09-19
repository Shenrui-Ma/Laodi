安装和更新使用同一条命令，无需卸载、手动下载或编译：

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi-skills/main/install.sh | sh
```

脚本自动下载本Release通用包、验证SHA-256并执行接入。也可手动下载下面的ZIP。

本版新增实验性的 `laodi protect enable/status/disable`：限制已核验客户端默认路径中的额外快照归档，保留普通 Git 与检查点。首次启用和撤销前需正常退出客户端，老底不会中断任务；不改代理或证书，无需 sudo。默认安装仍只监测。

已通过真实 macOS ACL、故障恢复及合成归档流程正反测试；真机对话、Git、编辑、测试与撤销正常。本轮没有观察到真实客户端归档被拒绝的证据，不把“没有新包”当成阻断成功。工具读取、普通模型上下文和其他上传机制不在阻断范围。详见仓库 `docs/PROTECTION.md`。

限制异常和客户端变化会由现有监测进程限频提醒。更新保留设置和事件；启用了限制时，卸载前需先 `protect disable`。

下载 `Laodi-skills-<版本>-macos-universal.zip`，Apple Silicon 与 Intel 共用一个包。解压后双击 `install.command`，无需自行构建，也无需 Go、Node.js 或 Python。

- 系统要求：macOS 13 或更新版本。
- 安装器配置已检测到的支持工具 Hook 与用户级后台监测；无需 sudo。未发现支持工具时仅复制程序并提示尚未接入。
- Skill 随包提供；安装器不会修改 PATH 或自动向客户端安装 Skill。在解压目录执行 `./laodi status` 可查询状态。
- 检测与提醒不会自动阻断 Agent 任务，不是网络防火墙，也不保证发现全部外传。
- ZIP 同时提供 SHA-256 校验和与构建信息；实际编译器、源码提交和验证范围见 `build-info.json`。

这是 **ad-hoc 签名的预览版**，尚无 Apple Developer ID 签名与公证。macOS 首次打开可能拦截；核对来源后，按“系统设置 > 隐私与安全性”的系统流程处理。不要关闭 Gatekeeper 或删除下载隔离属性。

构建流程检查两种架构、代码签名，并在构建机原生架构运行版本与帮助命令。这不代表已验证另一种架构的实际运行、下载后的 Gatekeeper 流程、系统通知送达或真实客户端回调。

停止监测并移除自有 Hook 与后台服务，可双击 `uninstall.command`，或在解压目录执行 `./laodi remove`；本地记录与运行文件保留。完整说明见包内 `INSTALL.txt` 与仓库 README。
