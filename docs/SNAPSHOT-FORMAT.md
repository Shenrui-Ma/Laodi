# 快照元数据格式

本文描述当前解析器接受的数据格式，不包含实验记录。支持构建以 `internal/laodi/scanner.go` 的 `KnownBuild` 为准；未知构建必须报告覆盖不足，不能套用已有字段语义。

## 目录与读取边界

```text
<checkpoint-root>/<workspace-id>/
├── state.json
├── manifests/<manifest-id>.json
└── extra-manifests/<manifest-id>.json
```

- 工作区目录名是 `sha256(workspaceKey)` 的前 12 位小写十六进制；清单标识是 64 位小写十六进制。
- 解析器只在选定证据根内打开自己构造的元数据路径，不跟随状态里的任意绝对路径，也不读取清单指向的源码、Git 对象、配置或归档内容。
- 同目录中的其他 checkpoint JSON 不能自动视为上传清单。损坏、超限、符号链接和工作区不匹配必须报告覆盖不足。

## 工作区清单

结构示例仅展示字段，不是实验数据：

```text
schema = "repo_snapshot_manifest/v2"
workspaceKey = 非空工作区标识
files = [{path: 相对路径, sizeBytes: 字节数}, ...]
```

`files` 是数组；路径取自每项的 `path`。`.git/objects/` 和 `.git/lfs/objects/` 表示历史对象数据线索；单独出现 `.git`、HEAD、config 或 reflog 不等于包含历史对象。非空普通工作区清单单独分类。

清单文件名的标识用于关联客户端记录，不是源码内容完整性证明。相同路径和大小但不同内容可能具有相同标识，不能用原始 JSON 字节摘要替代其含义。

## 附加配置清单

```text
schema = "repo_snapshot_extra_manifest/v1"
groups = [{groupId: 分组标识, files: [{path, sizeBytes, ...}, ...]}, ...]
```

解析器只提取 `global-configs` 组条目计数。不读取配置本体，不输出文件名、内容摘要、来源字段或配置值；清单存在不代表其中一定包含真实密钥。

## 状态关联

`state.json` 须同时包含非空 `workspaceKey` 和 `workspacePath`。主要关联字段为：

| 字段 | 用途 |
| --- | --- |
| `activeUpload` / `pendingUpload` | 同一活跃槽的不同版本字段，不能重复计数 |
| `latestPendingUpload` | 另一待处理组，可与活跃槽不同 |
| `nextManifestHash` / `nextExtraManifestHash` | 对应上传槽的清单标识 |
| `attemptCount` | 客户端进入尝试流程的计数线索 |
| `lastAcceptedManifestHash` / `lastAcceptedExtraManifestHash` | 客户端接受记录对应的清单标识 |

接受记录必须关联自己的清单，不能套用较新的待处理组。当前格式没有可靠的 `acceptedAt`，不能把清单创建时间或状态文件修改时间当成精确上传时间。

## 事件语义

| 线索 | 可报告 | 不能推断 |
| --- | --- | --- |
| 有效清单 | 发现快照清单 | 包已完整形成、含有密钥、已发送 |
| 尝试计数 | 客户端进入尝试流程 | 实际网络请求数或发送成功 |
| 接受字段与有效清单关联 | 客户端记录上传返回成功 | 独立服务端确认、长期留存或训练用途 |
| 无对应记录 | 未发现支持的线索 | 从未上传或已阻断 |

解析有文件数、大小和总读取量上限，具体值以实现为准。达到上限或输入不完整时须保留降级信息，不应输出完整覆盖结论。
