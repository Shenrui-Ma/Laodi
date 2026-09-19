# 预编译包安装

普通使用者无需构建源码。首个发行包为`v0.3.0-preview.1`，适用于macOS13+，包含Apple Silicon和Intel两种架构。

## 三步接入

1. 在[Release页面](https://github.com/Shenrui-Ma/Laodi-skills/releases/tag/v0.3.0-preview.1)下载ZIP并解压。
2. 双击`install.command`，或在解压目录运行`./laodi install`。
3. 首次由系统询问通知权限；在后续新会话中验证工具事件接入。

安装只作用于当前用户，无需sudo，不重启现有Agent。主程序、通知helper和Skill复制到`~/Library/Application Support/Laodi-skills/runtime`，所以完成安装后可以移动或移除解压文件夹。安装器不改PATH、不自动把Skill放进客户端目录。

只读预览、无通知接入选项：

```sh
./laodi install --dry-run
./laodi install --no-notifications
```

自动接入仅针对检测到的受支持工具。没有检测到时只安装运行文件并提示未接入；安装成功、后台注册、通知授权和真实回调是不同状态，不会合并成“已全面保护”。旧会话不会被强制重启。

## 查询与移除

解压目录里可运行`./laodi status`、`./laodi incidents`。若解压目录已经删除，使用固定位置：

```sh
"$HOME/Library/Application Support/Laodi-skills/runtime/laodi" status
```

双击`uninstall.command`或运行`./laodi remove`，只移除准确归属老底的Hook和后台服务，保留运行文件与事件历史。`./laodi remove --dry-run`只看计划。

预览版不自动替换已有不同版本或被修改的安装，也不覆盖未知配置。已有源码安装、模式或状态身份不一致时会明确拒绝，避免影响正在工作的任务和历史记录；不要用删除事件库的方式绕过提示。平滑升级属于后续工作。

## 下载校验与系统确认

将ZIP与Release中的`SHA256SUMS`放在同一目录后运行：

```sh
shasum -a 256 -c SHA256SUMS
```

校验和用于核对下载字节，不等同Apple认可的开发者身份。当前包使用ad-hoc签名，**没有Developer ID签名或Apple公证**；首次运行可能被Gatekeeper阻止。只有确认来源可信且文件未被改动时，再参考[Apple官方说明](https://support.apple.com/102445)处理该应用的打开确认。安装脚本不禁用Gatekeeper、不删除下载隔离属性。

## 发行验证范围

构建流程验证两种Mach-O架构、签名、解压后的可执行权限及构建机原生架构的版本/帮助。安装编排通过临时用户目录、假服务及通知调用测试，不在开发者电脑安装真实后台。构建元数据记录具体Go版本、源码提交、最低系统版本和未验证项。

这些检查不等于Intel实机、浏览器下载后的系统放行、通知可见性和真实客户端回调已经全部验收。发布为预览版，检测规则和不自动拦截的范围保持不变。
