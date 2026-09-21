# Laodi v0.4.1-beta.2

适用于 macOS 13+，Apple Silicon 与 Intel 共用一个安装包。

## 本版更新

- 通知统一为短标题和一句事实，提醒标题以“发现：”开头。
- 新增 `protect test --notify`：实际检查打包操作是否被拒绝、正常读写是否可用，并清理测试文件，通过后提示“测试打包操作已拦截”。
- 通知使用左侧标准应用图标，不附加右侧图片。
- 打包保护异常保留在状态和事件中，不再弹出这类提醒。
- 更新随包 Skill 的平台说明，保留已有配置、事件和保护规则。

## 安装与更新

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.sh | sh
```

已安装用户可运行：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" update
```

也可使用 `update --version v0.4.1-beta.2` 指定本版。安装不需要 sudo，不重启正在工作的编程工具。

已启用支持范围内的打包保护时，可主动检查：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" protect test --notify
```

该通知表示主动自检通过，不是某 APP 的实际上传拦截记录；未启用、检查失败或清理未完成时，不发送成功通知。

## 验证与范围

发布流程执行 Go 竞态测试、静态检查、命令行安装回归、原生通知与图标检查，并构建两种架构、核验签名和构建机上的版本输出。ZIP 附带 SHA-256 和 `build-info.json`。

仍为 ad-hoc 签名的 BETA，未做 Apple Developer ID 签名与公证。两种架构均构建，Intel 实机、不同系统通知设置和所有客户端组合并未全部验收。

保护仍限已支持客户端的特定归档路径，不阻止所有文件读取、模型请求或其他上传方式。Windows 使用独立 Release，本次不替换 Windows 安装包。
