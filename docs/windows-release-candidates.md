# Windows 候选包构建

`.github/workflows/release-windows.yml` 由维护者手动触发，仅构建候选包，不创建 GitHub Release。选择已审查的分支或提交，并填写候选版本；默认 `v0.4.1-windows.beta.1` 只是构建标签参数，不表示已有同名 Release。

流程执行 Windows 原生 race 测试和 vet、通知 helper 离线协议测试，随后构建并核对：

- ZIP 与 `SHA256SUMS-windows` 一致。
- `windows-manifest.json` 的版本及每个文件哈希一致。
- 两个 Go 二进制版本与 VCS 提交匹配。
- `build-info-windows.json` 的提交和 manifest 摘要匹配。

原生测试使用专用长路径临时目录；不会通过放宽生产路径检查来兼容 CI 的短路径别名。

Actions artifact 只包含 `Laodi-<版本>-windows-amd64.zip`、`SHA256SUMS-windows` 和 `build-info-windows.json`，保留 14 天。它是候选构建产物，不是公开支持的 Release 通道，不包含详细实验数据。

该 workflow 只有 `contents: read`，没有 tag 触发器或发布步骤。待 [Windows 发布验收](WINDOWS.md#发布验收) 完成，再单独审查发布配置；不得覆盖 macOS 资产或让两个工作流争抢创建同一 Release。候选包未做 Authenticode 签名，不宣称通过了所有 SmartScreen 或企业策略场景。
