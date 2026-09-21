# Laodi v0.4.1-windows.beta.4

适用于 Windows 11 x64。

## 本版更新

- 新增可选的 Git 历史打包限制，支持启用、状态查询、自检和撤销；仅适配经核验的 3.11.2 旧构建。
- 校验程序身份和缓存位置；记录原始权限，保护对象被移动或修改时保留恢复记录。
- 自检通知与 macOS 一致：**测试打包操作已拦截** / Git 历史打包保护正常。
- 保留 beta.3 的后台恢复、监测状态和推荐版本更新。

保护默认不启用，首次启用和撤销前需正常退出客户端。具体命令和恢复方法见[保护说明](https://github.com/Shenrui-Ma/Laodi/blob/main/docs/PROTECTION.md#windows-使用)。不建议为开启此功能降级日常使用的客户端。

## 安装与更新

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.ps1').Content)) -Version 'v0.4.1-windows.beta.4'
```

已安装 beta.3 或更新版本：

```powershell
& (Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe') update
```

beta.2 或更早版本可直接重跑安装命令，或用 `update --version 'v0.4.1-windows.beta.4'`。安装不需要管理员权限，保留配置与事件。

## 验证范围

已通过 [Windows 发布检查](https://github.com/Shenrui-Ma/Laodi/actions/runs/35572935577)：原生测试、竞态检查、通知协议、包内校验和，以及已固定哈希的原版归档组件测试。本地接收器独立解密验证历史数据、待传包恢复、新工作区和增量归档；附加配置与发送器是测试实现。

这些结果不等于完整客户端对官方服务的上传验收。保护限固定构建及归档路径，Windows 真实快照上传检测仍未启用；工具读取、模型请求和其他上传路径不在阻断范围。

启用保护后，回退到 beta.3 或更早版本前请先撤销。若已回退，恢复记录和新版版本目录仍保留，可使用支持保护的新版撤销。

本版仍为 BETA，未做 Authenticode 签名；不替换 macOS Release。
