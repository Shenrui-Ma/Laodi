# Laodi v0.4.1-windows.beta.3

面向 Windows 11 x64 的预编译包，提供工具事件监测、用户级后台运行和系统通知。Windows 暂不提供 Git 历史打包阻断。

## 本版更新

- 支持 `update` 自动选择 Windows 推荐版本，保留指定版本更新。
- 登录启动后端增加有限崩溃恢复；主动停止、卸载或启动设置改变时停止恢复。
- 改进桌面 Agent 内的安装兼容性，修复默认文件所有者导致的私有文件创建失败。
- 状态查询区分工具接入、历史回调和监测覆盖；通知统一为短标题和一句事实。

## 安装

在普通 PowerShell 中运行：

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.ps1').Content)) -Version 'v0.4.1-windows.beta.3'
```

无需管理员权限，默认安装到当前用户的 `%LOCALAPPDATA%\Laodi-skills`。**旧版安装未完成时，直接重新运行这条命令**，保留已有配置和记录。

```powershell
$Laodi = Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe'
& $Laodi status
& $Laodi notifications test
```

## 更新

beta.2 首次升级需要指定版本：

```powershell
& $Laodi update --version 'v0.4.1-windows.beta.3'
```

升级到 beta.3 后，后续更新直接运行：

```powershell
& $Laodi update
```

推荐通道在公开安装包校验完成后推广，独立于 macOS Release。

## 验证范围

已通过 [Windows 发布构建检查](https://github.com/Shenrui-Ma/Laodi/actions/runs/35562750242)：原生测试、竞态检查、`go vet`、离线通知协议测试，以及 ZIP、包内文件、版本和源码提交校验。

这些检查不代表全部设备上的真实客户端交互、可见通知、干净用户隔离和 24 小时运行均已通过。旧版客户端的后台打包上传也不在 Windows 当前覆盖承诺内。本版仍为 BETA，程序未做 Authenticode 签名。

本 Release 只包含 Windows 资产，不替换 macOS 安装包。
