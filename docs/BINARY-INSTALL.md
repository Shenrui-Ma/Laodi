# 预编译包安装与更新

普通使用者无需构建源码。当前发行包为`v0.4.1-beta.1`，适用于macOS13+，包含Apple Silicon和Intel两种架构。

## 命令行接入

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.sh | sh
```

脚本只从本项目的GitHub Release下载，核对该ZIP的唯一SHA-256摘要、检查归档条目后解压到私有临时目录，再运行包内`laodi install`。不依赖Go、Node.js或Python。下载、校验或解压失败时不会继续安装，临时文件会清理。

只想预览可传入`--dry-run`：

```sh
curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/install.sh | sh -s -- --dry-run
```

也可自行从[Release页面](https://github.com/Shenrui-Ma/Laodi/releases/tag/v0.4.1-beta.1)下载ZIP、校验并运行包内`install.command`，但这不是默认必经步骤。

安装只作用于当前用户，无需sudo，不重启现有Agent。主程序、通知helper和Skill复制到`~/Library/Application Support/Laodi-skills/runtime`，所以完成安装后可以移动或移除解压文件夹。安装器不改PATH、不自动把Skill放进客户端目录。

只读预览、无通知接入选项：

```sh
./laodi install --dry-run
./laodi install --no-notifications
```

自动接入仅针对检测到的受支持工具。没有检测到时只安装运行文件并提示未接入；安装成功、后台注册、通知授权和真实回调是不同状态，不会合并成“已全面保护”。旧会话不会被强制重启。

首次出现系统授权时选择是否允许；已拒绝则提示到“系统设置 → 通知 → 老底”开启，不重复请求。通知程序等待60秒，安装器为其保留70秒；请求异常后只重新查询一次状态。运行文件、Hook和服务已安装时，通知失败作为明确警告返回，不将整个安装判为失败。`--format json`单独提供`notification_status`及`warnings`，退出码0不代表通知已经开启。

## 更新

重新执行上面的安装命令即可更新，包括从`v0.3.0-preview.1`升级。装过新版后也可运行：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" update
```

`update --dry-run`下载并校验发行包，只显示计划。`update --version v0.4.1-beta.1`指定官方发行版本。默认使用官方安装脚本推荐的版本，不依赖GitHub的`latest`是否包含预览版。自定义状态目录的安装会沿用当前可执行文件所在的状态目录，也可明确传入`--state-dir`。

更新前验证已安装文件、所有权凭据及实际Hook配置，完整准备新运行目录后使用macOS原子目录交换。已有Hook配置、通知应用身份、事件库及队列保留；只短暂重启老底监测进程，不重启Agent或更改其权限。同一包重复安装不重启监测。监测重启期间，工具事件仍可进入原有有界队列；快照轮询有短暂间隔，不能承诺零观测缺口。

新版监测发布新心跳后才清理旧运行目录。启动或健康检查失败会回滚程序并重启旧监测，不回退事件库；进程意外退出后，下一次安装/更新会根据本地事务记录先恢复再继续。文件被外部修改、所有权不匹配、文件系统不支持原子交换时会拒绝更新并保留诊断材料，不覆盖未知文件。更新本身不再次请求通知权限。

## 查询与移除

解压目录里可运行`./laodi status`、`./laodi incidents`。若解压目录已经删除，使用固定位置：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" status
```

双击`uninstall.command`或运行`./laodi remove`，只移除准确归属老底的Hook和后台服务，保留运行文件与事件历史。`./laodi remove --dry-run`只看计划。

命令行更新支持有完整所有权凭据的发行包安装。已有源码安装、模式或状态身份不一致时会明确拒绝；不要用删除事件库的方式绕过提示。仅安装运行文件、尚未接入服务的情况，更新保留这个状态，需要监测时再执行一次安装命令。

## 下载校验与系统确认

将ZIP与Release中的`SHA256SUMS`放在同一目录后运行：

```sh
shasum -a 256 -c SHA256SUMS
```

校验和用于核对下载字节，不等同Apple认可的开发者身份。当前包使用ad-hoc签名，**没有Developer ID签名或Apple公证**；首次运行可能被Gatekeeper阻止。只有确认来源可信且文件未被改动时，再参考[Apple官方说明](https://support.apple.com/102445)处理该应用的打开确认。安装脚本不禁用Gatekeeper、不调用移除下载隔离属性的命令。

## 发行验证范围

构建流程验证两种Mach-O架构、签名、解压后的可执行权限及构建机原生架构的版本/帮助。安装编排通过临时用户目录、假服务及通知调用测试，不在开发者电脑安装真实后台。构建元数据记录具体Go版本、源码提交、最低系统版本和未验证项。

这些检查不等于Intel实机、浏览器下载后的系统放行、通知可见性和真实客户端回调已经全部验收。发布为预览版。默认只监测；可选额外快照限制的适用范围和验收边界见[保护说明](PROTECTION.md)。

## 名称兼容

产品与仓库现名为 Laodi。安装包使用 Laodi 名称；已安装用户的状态目录、通知身份和后台服务标识保留原值，避免改名导致事件记录丢失或重复安装。命令中的 `Library/Application Support/Laodi-skills` 因此继续有效。
