# Windows 使用与更新

## 发布状态

当前提供 Windows 11 x64 BETA：`v0.4.1-windows.beta.4`。它使用独立 Windows 安装包，macOS ZIP 不能用于 Windows。BETA 不代表所有交互与长时间运行验收均已完成。

已有实现包括本地工具事件监测、当前用户后台任务、安装更新、脱敏记录与通知管理。Windows 默认接收工具事件；beta.4 提供限已核验旧构建的可选 Git 历史打包限制，需用户主动启用。真实快照上传检测仍未接通，不能将保护状态当作上传事件记录。[支持范围与操作](PROTECTION.md#windows-使用)

开发验证以 Windows 11 x64、本地 NTFS 为目标。其他 Windows 版本、ARM64、WSL、不同文件系统及系统策略需分别验证。安装在普通用户 PowerShell 中进行，不需要管理员权限，不修改系统执行策略或安全防护。

## 命令行安装

在普通 PowerShell 中执行：

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.ps1').Content)) -Version 'v0.4.1-windows.beta.4'
```

脚本从对应 Release 下载 Windows 包并校验 ZIP 与包内文件。不修改 PATH，默认安装到当前用户目录。更新保留配置与记录。

安装脚本使用主分支入口，安装包仍由 `-Version` 固定选择。这使安装入口的兼容性修复可以独立于发行包交付；旧标签中的安装脚本不会随主分支更新。

在已登录 Windows 桌面的 Agent 中执行时，脚本会检查临时包的真实路径。如果宿主重定向了用户目录，它会通过同一用户、同一会话的桌面启动经过校验的安装器，等待完成并返回输出和退出码。它不请求管理员权限，也不依赖临时计划任务。生成的本机脚本仅在该子进程内设置执行策略，不修改用户或系统策略；组织策略仍优先。无可用桌面、策略禁止启动或结果超时会明确报错；结果不确定时保留私有暂存文件，不自动重复安装。

该兼容路径适用于 `install.ps1`。它不修改宿主的目录视图，也不更新协议 1 的固定启动器；已经存在重定向副本的宿主，直接运行旧固定入口查询状态仍可能失败。此时在普通桌面终端查询，后台运行不依赖 Agent 会话。

## 计划任务被拒绝时

如果旧版显示 `0x80070005`、`Access denied` 或“文件已安装但后台未连接”，先不要反复提权、改系统 ACL 或删除任务目录。直接重新运行上面的 beta.3 安装命令：它会使用新安装器修复已有的部分安装，保留配置和记录。旧 beta.1 的 `update` 仍由旧安装器执行，遇到这类失败时不能代替重新运行新安装命令。

新版优先保留已有的计划任务。只有首次创建明确被系统拒绝，且确认没有既有任务或收据时，才改用**当前用户的登录启动项**。已有任务、被修改的启动项或不明确错误不会被自动接管。它不要求提升权限，不修改系统安全策略，也不改变用户禁用启动项的设置。

安装输出会区分“计划任务”和“当前用户登录启动”。必须看到后台运行检查通过，才能确认监控运行；“程序已复制”不代表已生效，“已接入工具 0”也需要结合是否完成安装判断。

登录启动项可能受系统启动设置影响。beta.3 为该后端增加有限崩溃恢复：监测进程异常退出后等待一分钟，最多重试三次；正常退出、主动停止和卸载不会触发重启。每次重试都会重新核对启动项、归属记录、当前版本与系统启动偏好；它们被删除、修改或无法确认时停止恢复。beta.2 及更早版本不具备该功能。

恢复进程使用独占锁，保留协议 1 的固定启动器和快捷方式。它不解释或修改系统未公开的启动偏好字段，只将偏好变化视为停止恢复的条件。如果用户或系统禁用了启动项，由用户在 Windows 启动应用设置中决定是否恢复；`status` 仍以实际监测进程和心跳判断运行状态。

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

上面两条分别开启、关闭老底自己的通知接入，不替用户修改 Windows 通知设置。若要主动测试显示，可执行 `& $Laodi notifications test`，它会发送一条测试通知。系统接受请求不等于用户一定看到横幅；勿扰、策略和系统通知设置仍可能影响显示。

## 在线更新

beta.3 起，可直接更新到 Windows 推荐版本：

```powershell
& $Laodi update
```

推荐通道独立于 macOS Release。可先运行 `& $Laodi update --dry-run` 查看更新计划。

从 beta.2 升级到最新版，首次仍需指定版本：

```powershell
& $Laodi update --version 'v0.4.1-windows.beta.4'
```

如果旧版安装未完成，重新运行上面的命令行安装入口。指定 `--version` 仍可选择其他 Windows 版本；不要使用 macOS 标签。本地候选包仍可用前述安装方式更新。[更新通道说明](windows-update-channel.md)

## 卸载

```powershell
& $Laodi remove
```

只处理能确认归属老底的后台任务、Hook 和通知接入，保留本地历史记录。已有配置被外部修改时会拒绝不明确的删除操作。

## 发布验收

正式稳定版前仍需补齐并保留：全新用户安装、真实客户端回调、可见通知、安装/更新/卸载与中断恢复、登录启动和会话切换，以及至少 24 小时实际运行观察。BETA 的原生 CI、离线通知协议与包校验不替代这些交互验收。

详细实验资料留在私有验证记录，公开文档不包含真实账号路径、日志、凭据或逐项实验数据。
