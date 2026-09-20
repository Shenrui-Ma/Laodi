# Windows 使用与更新

## 发布状态

当前提供 Windows 11 x64 BETA：`v0.4.1-windows.beta.2`。它使用独立 Windows 安装包，macOS ZIP 不能用于 Windows。BETA 不代表所有交互与长时间运行验收均已完成。

已有实现包括本地工具事件监测、当前用户后台任务、安装更新、脱敏记录与通知管理。Windows 默认只接收工具事件；Git 历史打包限制仍仅支持 macOS，Windows 快照格式未通过适配时会报告覆盖不足。

开发验证以 Windows 11 x64、本地 NTFS 为目标。其他 Windows 版本、ARM64、WSL、不同文件系统及系统策略需分别验证。安装在普通用户 PowerShell 中进行，不需要管理员权限，不修改系统执行策略或安全防护。

## 命令行安装

在普通 PowerShell 中执行：

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/v0.4.1-windows.beta.2/install.ps1').Content)) -Version 'v0.4.1-windows.beta.2'
```

脚本从对应 Release 下载 Windows 包并校验 ZIP 与包内文件。不修改 PATH，默认安装到当前用户目录。更新保留配置与记录。

## 计划任务被拒绝时

如果旧版显示 `0x80070005`、`Access denied` 或“文件已安装但后台未连接”，先不要反复提权、改系统 ACL 或删除任务目录。直接重新运行上面的 beta.2 安装命令：它会使用新安装器修复已有的部分安装，保留配置和记录。旧 beta.1 的 `update` 仍由旧安装器执行，遇到这类失败时不能代替重新运行新安装命令。

新版优先保留已有的计划任务。只有首次创建明确被系统拒绝，且确认没有既有任务或收据时，才改用**当前用户的登录启动项**。已有任务、被修改的启动项或不明确错误不会被自动接管。它不要求提升权限，不修改系统安全策略，也不改变用户禁用启动项的设置。

安装输出会区分“计划任务”和“当前用户登录启动”。必须看到后台运行检查通过，才能确认监控运行；“程序已复制”不代表已生效，“已接入工具 0”也需要结合是否完成安装判断。

登录启动项可能受系统启动设置影响，且不提供计划任务的崩溃重启能力。可用 `status` 检查监测是否仍在运行；如果用户或系统禁用了启动项，由用户在 Windows 启动应用设置中决定是否恢复。

## 本地候选包安装与更新

使用可信构建产生的 Windows 候选包。先将 ZIP 的 SHA-256 与同一构建的 `SHA256SUMS-windows` 核对，再将内层 ZIP 的全部文件解压到同一目录，在该目录运行：

```powershell
.\laodi.exe install --dry-run
.\laodi.exe install
```

候选包包含 `laodi.exe`、`laodi-host.exe`、通知辅助程序、logo、Skill 及 `windows-manifest.json`。不要单独复制一个 EXE，也不要删改清单或其他包内文件。安装器会核对清单与各文件哈希。

更新到另一个本地候选版本时，在新包目录运行同一组安装命令，保留原状态目录。它会保留事件与配置，并按恢复记录切换版本；不需要先卸载。客户端版本不受支持、已有接入被修改或有待恢复操作时，先处理安装器报告的问题，不强行覆盖。

如果从 Actions 下载，外层 artifact ZIP 内含候选 ZIP、校验文件与构建信息；需要核对并解压的是里面的候选 ZIP。构建仍由维护者手动触发，候选产物有有效期，不是公开 Release 下载入口。

## 日常使用

安装器不修改 PATH。默认入口是 `%LOCALAPPDATA%\Laodi-skills\laodi.exe`，保留目录名称用于兼容已有测试安装。

```powershell
$Laodi = Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe'
& $Laodi status
& $Laodi incidents --format agent-summary
& $Laodi notifications status
```

`status` 查看监测状态，`incidents` 查看脱敏事件，`notifications status` 只读查询通知集成与系统 API 状态。Skill 只是查询入口，不是后台监测的运行条件。

```powershell
& $Laodi notifications enable
& $Laodi notifications disable
```

上面两条分别开启、关闭老底自己的通知接入，不替用户修改 Windows 通知设置。若要主动测试显示，可执行 `& $Laodi notifications test`，它会发送一条合成通知。系统接受请求不等于用户一定看到横幅；勿扰、策略和系统通知设置仍可能影响显示。

## 在线更新

Windows 更新必须显式指定**包含 Windows 资产**的 Release 标签。不要使用 macOS 标签，也不要省略 `--version`。

更新到当前 Windows BETA：

```powershell
& $Laodi update --version 'v0.4.1-windows.beta.2'
```

后续更新时替换为目标 Windows Release 标签；本地候选包仍可用前述安装方式更新。

## 卸载

```powershell
& $Laodi remove
```

只处理能确认归属老底的后台任务、Hook 和通知接入，保留本地历史记录。已有配置被外部修改时会拒绝不明确的删除操作。

## 发布验收

正式稳定版前仍需补齐并保留：全新用户安装、真实客户端回调、可见通知、安装/更新/卸载与中断恢复、登录启动和会话切换，以及至少 24 小时实际运行观察。BETA 的原生 CI、离线通知协议与包校验不替代这些交互验收。

详细实验资料留在私有验证记录，公开文档不包含真实账号路径、日志、凭据或逐项实验数据。
