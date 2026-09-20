# Laodi Windows BETA

面向 Windows 11 x64 的预编译包。提供工具事件监测、用户级后台运行、系统通知和指定版本更新。Windows 暂不提供 Git 历史打包阻断。

本版修复计划任务被拒绝时的安装中断：在确认无既有任务的首次安装中，明确的访问拒绝会切换到当前用户登录启动。已有任务与用户修改的启动项保持归属检查，安装仍需通过真实心跳验证。

安装输出明确区分“文件已复制”“后台运行”“工具已接入”，并移除原始 PowerShell XML 错误。在线更新改为调用经过校验的新安装器。

**beta.1 安装失败的用户请重新运行下方安装命令，不能仅依赖旧版 update。** 当前仍是 BETA，原受影响设备需要复测。

## 安装

在 PowerShell 中运行：

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/v0.4.1-windows.beta.2/install.ps1').Content)) -Version 'v0.4.1-windows.beta.2'
```

默认安装到当前用户的 `%LOCALAPPDATA%\Laodi-skills`，沿用已有安装路径，无需管理员权限。

```powershell
$Laodi = Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe'
& $Laodi status
& $Laodi notifications test
& $Laodi update --version 'v0.4.1-windows.beta.2'
```

后续更新时，将版本号换成对应 Windows Release 的标签。

## 验证范围

发布前执行 Windows 原生测试、竞态检查、`go vet` 和离线通知协议测试；核对 ZIP、包内文件、版本、源码提交与构建清单。

以上检查不代表真实客户端交互、桌面通知可见性、干净用户隔离及 24 小时长测全部通过。这些验收仍在进行，当前为 BETA。

本 Release 只包含 Windows 资产，不更新或替换 macOS 安装包。
