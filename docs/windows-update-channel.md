# Windows 推荐更新通道

从 `v0.4.1-windows.beta.3` 起，省略 `--version` 时会从固定地址读取 [Windows amd64 推荐元数据](https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/channels/windows-amd64.json)。通道独立于 GitHub `latest`，不会选择 macOS Release。

```powershell
$Laodi = Join-Path $env:LOCALAPPDATA 'Laodi-skills\laodi.exe'
& $Laodi update --dry-run
& $Laodi update
& $Laodi update --version v0.4.1-windows.beta.3
```

`--version` 绕过通道，可固定版本或主动回退。默认通道拒绝比正在运行的正式构建更旧的推荐版本；同版本仍可核验并执行安装计划。没有正式发行标签的开发构建不参与版本顺序比较。`--dry-run` 仍下载、校验并调用新包安装器的预览模式，不切换版本。

## 元数据合同

仓库的 `channels/windows-amd64.json` 仅包含四个字段：

```json
{
  "schema": 1,
  "channel": "recommended",
  "platform": "windows/amd64",
  "version": "v0.4.1-windows.beta.3"
}
```

读取上限为 4096 字节，只接受有效 UTF-8、单一 JSON 对象、精确字段名和类型；未知、重复、缺失字段及尾随内容均拒绝。版本必须符合既有严格发行标签规则，且预发行标识必须为 `windows` 或以 `windows.` 开头。元数据不能指定下载地址或命令。当前协议仅支持 Windows amd64。

固定 HTTPS 请求最多等待 30 秒，拒绝任何重定向。元数据缺失、网络失败、格式错误或较旧推荐均在下载和安装前报错；不会静默选择缓存、内置旧版本或 GitHub `latest`。可重试或明确指定 `--version`。

解析出的标签继续进入既有 Windows 下载器：官方 HTTPS Release 主机限制、`SHA256SUMS-windows`、ZIP 路径与尺寸限制、包内 manifest 和逐文件哈希、版本一致性检查全部保留。执行已校验新包中的安装器，并沿用事务更新、中断恢复与失败回退。元数据和校验和依赖同一仓库的 HTTPS 信任，不等于独立签名或 Authenticode。

## 发布与推广

1. 按 Windows 候选构建流程检查新版本，并独立发布带 Windows 专属标签的 GitHub Release。确认公开 ZIP 和 `SHA256SUMS-windows` 可下载且一致。
2. 在单独可审查的改动中，将 `channels/windows-amd64.json` 的 `version` 更新为已发布版本并合并到 `main`。候选构建工作流不自动推广通道。
3. 使用包含此功能的构建在隔离安装上执行默认 `update --dry-run`，核对选择版本与安装计划，再执行已授权的更新验收。

beta.2 及更早二进制不会自动获得新代码。beta.2 首次升级需显式指定 `--version v0.4.1-windows.beta.3`；旧版安装未完成时，重新运行[命令行安装入口](WINDOWS.md#命令行安装)。只有公开资产校验完成后才推广推荐通道。新发行版若比通道推荐更高，默认更新会拒绝降级，直到维护者推广通道。
