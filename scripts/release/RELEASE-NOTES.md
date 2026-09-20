Laodi 更名后的 macOS BETA 版本。已有安装继续沿用原状态目录、通知身份和接入配置。

安装或更新：

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.sh | sh
```

已安装用户也可运行 `laodi update`；未加入 PATH 时使用：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" update
```

- 发布包改为 `Laodi-v0.4.1-beta.1-macos-universal.zip`，包内目录为 `Laodi`。Apple Silicon 与 Intel 共用一个包。
- 更新器切换到新仓库地址；安装保留已有配置与事件，不迁移用户状态目录。
- README 精简实验摘要和提醒后自查；Skill 改进已安装程序的路径查找。
- `protect enable/status/disable` 提供可选的 Git 历史打包限制（BETA），默认安装仅监测。限制仅适用于支持的客户端版本和归档路径，不阻断普通模型请求、文件读取或其他上传路径。详见仓库 `docs/PROTECTION.md`。

系统要求为 macOS 13 或更新版本。安装器配置已检测到的支持工具 Hook 与用户级后台监测，无需 sudo；未发现支持工具时仅复制程序并提示尚未接入。Skill 随包提供，不会自动安装到客户端，也不修改 PATH。

发布 ZIP 附带 SHA-256 和 `build-info.json`。本包为 **ad-hoc 签名的 BETA**，尚无 Apple Developer ID 签名与公证。macOS 首次打开若拦截，请核对来源并按“系统设置 > 隐私与安全性”的系统流程处理。

构建验证包括两种架构、代码签名和构建机原生架构的版本与帮助命令；不代表已完成 Intel 实机、下载后 Gatekeeper、系统通知及所有客户端回调验收。

卸载可运行 `laodi remove` 或包内 `uninstall.command`；启用打包限制时需先正常退出受保护客户端并运行 `laodi protect disable`。本地记录保留。完整用法见 README 与包内 `INSTALL.txt`。
