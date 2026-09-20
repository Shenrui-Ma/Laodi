# Laodi Windows BETA

面向 Windows 11 x64 的预编译包。提供工具事件监测、用户级后台运行、系统通知和指定版本更新。Windows 暂不提供 Git 历史打包阻断。

## 安装

在 PowerShell 中运行：

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/v0.4.1-windows.beta.1/install.ps1').Content)) -Version 'v0.4.1-windows.beta.1'
```

默认安装到当前用户的 `%LOCALAPPDATA%\Laodi-skills`，沿用已有安装路径，无需管理员权限。

```powershell
$Laodi = Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe'
& $Laodi status
& $Laodi notifications test
& $Laodi update --version 'v0.4.1-windows.beta.1'
```

后续更新时，将版本号换成对应 Windows Release 的标签。

## 验证范围

发布前执行 Windows 原生测试、竞态检查、`go vet` 和离线通知协议测试；核对 ZIP、包内文件、版本、源码提交与构建清单。

以上检查不代表真实客户端交互、桌面通知可见性、干净用户隔离及 24 小时长测全部通过。这些验收仍在进行，当前为 BETA。

本 Release 只包含 Windows 资产，不更新或替换 macOS 安装包。
