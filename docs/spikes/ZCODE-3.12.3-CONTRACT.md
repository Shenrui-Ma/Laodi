# ZCode 3.12.3 本地客户端证据契约

状态：**公开分发程序的静态代码已核对；真实 GUI 会话、上传和系统通知尚未验证。**

本次只读检查 `/Applications/ZCode.app` 内的程序文件，没有启动 ZCode，没有读取 `~/.zcode`、账号、会话、用户配置或项目。本文不是对这台机器发生过上传的判断。适配器只能对匹配版本的本地记录作有限解释，不能把静态存在的功能等同于服务端当前已启用它。

## 1. 身份与可复查依据

从 `Contents/Info.plist` 取得：

| 字段 | 值 |
|---|---|
| `CFBundleIdentifier` | `dev.zcode.app` |
| `CFBundleShortVersionString` | `3.12.3` |
| `CFBundleVersion` | `3.12.3.7463` |
| `CFBundleExecutable` | `ZCode` |

`Contents/Resources/app.asar` SHA-256：

```text
6d99a52d5c25bcdc9651d0a4aad57d215580cb11fe06c8a9ce3553387013678e
```

关键 ASAR 成员及 SHA-256：

| 成员 | SHA-256 |
|---|---|
| `out/host/index.js` | `c8f7b2e50f2c8f7eeb030a377cfc4779b2a0e2037af2239e065157dc2e3e422e` |
| `out/host/chunk-ZH56ETHO.js` | `2302032d9e27ff3daea25b0f530d522290da25addaae61e672dc7171a4b4908f` |
| `out/host/chunk-OIOBEZTZ.js` | `0d0daa22c940be674a9e627052f0bf463f58f88d97076059da56dd623e0a93fc` |
| `out/main/index.js` | `5105c8659924d8c262bc763302131d6dc1f50b1249d9fc578e2d84cf76f55ae4` |

仓库只保存本说明和标准库只读检查器，不复制或执行厂商源码。复查示例：

```bash
python3 scripts/dev/inspect_zcode_asar.py \
  --member '^out/host/index.js$' \
  --pattern 'recordUploadAttempt\(|lastAcceptedManifestHash' \
  --context 250 --limit 12
```

下面的 offset 是对 UTF-8 解码后文本的零起始字符位置；它不是源码行号，也不是 ASAR 字节位置。以文件 hash 和命名函数为首要定位依据。未知版本可以发现路径层面的线索，但不能沿用本版本的强语义结论。

## 2. 目录和关联关系

host 的 `getDataBaseDir` 按运行时覆盖值、`ZCODE_DATA_BASE_DIR`、`HOME` / `os.homedir()` 取根；`getAppConfigDir` 是 `<base>/.zcode/v2`。常见安装的目录为：

```text
<base>/.zcode/v2/checkpoints/
└── <workspaceHash>/
    ├── state.json
    ├── manifests/<manifestHash>.json
    ├── extra-manifests/<extraManifestHash>.json
    ├── pending/<groupId>.tar.gz.enc
    ├── pending/<groupId>.envelope.json
    └── tmp/<groupId>.tar.gz
```

- `workspaceKey = workspaceIdentity.trim() || workspacePath`。
- `workspaceHash = sha256(workspaceKey).hexdigest().slice(0, 12)`。
- 当前生成的 `groupId = manifestHash + "." + extraManifestHash + "." + createdAt`；兼容路径可能退回用 manifest hash。
- 旧目录 `repo-snapshots` 存在、`checkpoints` 不存在时，客户端会尝试整体 rename；观察器不应执行迁移。
- `checkpoints` 中也有 **GitCheckpointStore** 的其他 JSON。不能把目录里的每一个 JSON 都判成上传清单。

代码定位：`getRepoSnapshotWorkspaceHash`、`getRepoSnapshotRootDir`、`getRepoSnapshotManifestPath`、`getRepoSnapshotExtraManifestPath`、`getRepoSnapshotStatePath`、`getRepoSnapshotArtifactPaths`；host 约 `2226900–2228130`。

**观察器只能在已获授权的检查根内打开自己构造的路径。** 不跟随 `state.json` 提供的绝对路径，不打开工作区、Git 对象、归档或 envelope 内容；把这些字段当不可信引用。解析器须对 symlink、路径逃逸、超大文件、文件轮换和半写 JSON 留出处理。

## 3. 普通清单

以下是按真实结构写的合成示例，数值和路径均非用户数据：

```json
{
  "schema": "repo_snapshot_manifest/v2",
  "workspaceKey": "/synthetic/repo",
  "createdAt": 1789700000000,
  "files": [
    {"path": ".git/objects/pack/pack-synthetic.pack", "sizeBytes": 123},
    {"path": "src/main.go", "sizeBytes": 42}
  ],
  "stats": {"includedFileCount": 2, "includedBytes": 165}
}
```

`files` 是数组，路径在每个对象的 `path` 字段，不是对象 key。文件项由扫描内部对象去掉 `absolutePath`、`modifiedTimeMs`、`changeTimeMs` 后写出，当前留下 `path` 和 `sizeBytes`。时间戳是 `Date.now()` 风格的毫秒值。

代码定位：`scanRepoSnapshot`，host offset `2234482`；schema 常量在 `out/host/chunk-ZH56ETHO.js` 约 `571450`。

扫描会先使用 `git ls-files --cached --others --exclude-standard -z`，失败时回退遍历；随后 `appendRootGitMetadataPaths` 另外加入根 `.git` 下的条目。若 `.git` 是普通 gitfile，仅加入该文件；这里没有证明它跟随 gitfile 读取外部整个管理目录。筛选对 Git 元数据有提前放行分支，但拒绝 symlink。

所以清单中存在 `.git` / `.git/config`，只能说明 **Git 元数据线索**；`.git/objects/...` 才能指向 **Git 对象数据线索**。即使出现对象文件名，也不证明某个特定秘密在其中，更不证明这些数据已经离开设备。

### manifestHash 不是文件内容摘要

`computeRepoSnapshotManifestHash`（host 约 `2226300`）对以下逻辑对象进行 canonical JSON 后算 SHA-256：

```text
{
  schema: "repo_snapshot_manifest_hash/v1",
  workspaceKey,
  files: files.sort(path.localeCompare).map(({path, sizeBytes}) => ({path, sizeBytes}))
}
```

canonical JSON 会递归排序对象 key、忽略 `undefined`、保留数组顺序、用 JSON 字面量编码基本类型，不含缩进。原清单的 `createdAt`、`stats`、原始 schema 及内容字节不参与。**同路径同大小但内容不同的文件可产生相同 manifest hash。**

因此不能拿原始 JSON 文件 bytes 的 SHA-256 与文件名比较，也不能把文件名 hash 当作历史内容完整性证明。跨语言重算还需复现 JS `localeCompare` 的排序语义；首版可按受约束的同工作区 hash 文件名关联并明确它是客户端记录。

## 4. extra manifest

```json
{
  "schema": "repo_snapshot_extra_manifest/v1",
  "createdAt": 1789700000000,
  "groups": [
    {
      "groupId": "global-configs",
      "changePolicy": "rare",
      "files": [
        {
          "path": "settings.behavior.json",
          "sizeBytes": 123,
          "contentHash": "<sha256>",
          "source": "app-memory:global-settings"
        }
      ]
    }
  ],
  "stats": {"includedFileCount": 1, "includedBytes": 123}
}
```

字段 `changePolicy`、`source` 可以省略；组名还存在 `references`。全局配置可能出现 `mcp.json`、`skills.json`、`commands.json`、`hooks.json`、`plugins.json`、`memory.json`、`subagents.json`、`instructions.json` 等。客户端对部分键做脱敏，但 Laodi 不能因此宣称所有附加配置已脱敏，也不应读取实际内容进行验证。

extra manifest 的内容 hash 与普通 manifest 不同：它按组/路径规范化，并包含每项的 `contentHash`。观察器不必读取这些文件本体。需要提示的只是“附加配置进入快照清单”，而非“密钥已经泄露”。

代码定位：`buildRepoSnapshotExtra`，host 约 `884200–885650`；`buildRepoSnapshotGlobalConfigsExtraInputs`，约 `2228090–2230050`。

## 5. state 与 pending upload

state 没有版本 schema。主要字段：

```text
workspacePath, workspaceKey
failureCount?                       // 不是上传次数
activeUpload?                       // 活跃上传槽
pendingUpload?                      // activeUpload 的旧兼容别名
latestPendingUpload?                // 新的待处理组，可能和 active 不同
lastAcceptedManifestHash?
lastAcceptedManifestPath?
lastAcceptedExtraManifestHash?
lastAcceptedExtraManifestPath?
lastCompressedSize?                 // 大小观测，不代表上传
```

上传槽对象经 `sanitizePendingUpload` 保存这些字段：

```text
groupId, uploadCredentialHandle?, kind("baseline"|"increment")
encryptedArtifactPath, encryptionEnvelopePath
manifestPath, extraManifestPath?
baseManifestHash?, nextManifestHash
baseExtraManifestHash?, nextExtraManifestHash?
createdAt, attemptCount?, lastAttemptAt?, failureCountedAt?
attribution { sessionId, queryId?, requestId, failureCount,
              captureStage?, historyRoundCount? }
```

读取时优先 `activeUpload ?? pendingUpload`，不要计两次；`latestPendingUpload` 需作为另一组处理。`sanitizePendingUpload` / `normalizeStateSlots` 代码定位在 host 约 `2262100–2263800`。

`lastCompressedSize` 中有 `encryptedSizeBytes`、`workspaceSizeBytes`、`manifestHash`、`recordedAt`。体积超限也可能记录它并删除产生的文件，因此它不能单独证明“完整包已形成”，更不能证明“上传失败多少次”。

## 6. 上传阶段的准确语义

| 证据 | 支持的表述 | 不支持的推断 |
|---|---|---|
| 合法 manifest 出现 | 客户端生成了该快照清单 | 全部文件内容相符、完整包仍存在、已上传 |
| pending 槽出现 | 生成结果被登记为待上传组 | 上传请求已发出 |
| `attemptCount > 0` / `lastAttemptAt` | 客户端进入过上传尝试流程 | 网络请求一定发出、次数等于上传次数 |
| `failureCount` | 客户端的历史失败组计数线索 | HTTP 重试次数、完整上传次数 |
| `lastAcceptedManifestHash` 等 | 客户端记录此前上传返回 HTTP 成功 | 独立服务端取证、远端留存/删除/训练用途 |
| 不存在 state / pending / accepted | 当前未观察到相应记录 | 从未上传、没有风险 |

具体顺序已经由静态调用链核对：

1. `flushActiveUpload` 读取 `activeUpload ?? pendingUpload`，取得 token。
2. `recordUploadAttempt` **先**写入 `attemptCount + 1` 和 `lastAttemptAt`。
3. 随后仍可能因 `failureCountedAt`、凭据 handle 缺失/过期、大小上限等原因返回或丢弃；`requestUploadTarget` 在本版本是验证内存凭据并组装目标，不是另一次服务器请求。
4. `uploadObject` 通过 PUT / POST 发出归档上传；返回值只在 HTTP `response.ok` 时为 `{ok:true}`。成功分支没有进一步验证 response body。
5. `markAcceptedManifest` 将当前组的 `nextManifestHash` / 清单路径写入 accepted 字段，并可能提升 latest pending；随后清理已处理的加密归档，保留被 accepted 引用的清单。

最短关键代码特征：

```text
attemptCount: (n.attemptCount ?? 0) + 1, lastAttemptAt: this.now()
return i.ok ? {ok: true, etag: ...} : {ok: false, ...}
lastAcceptedManifestHash: o.manifestHash
```

定位：`recordUploadAttempt` offset `2247486`；`flushActiveUpload` 约 `2274800`；`RepoSnapshotUploadClient.uploadObject` 约 `2272400`；accepted 写入约 `2249167` / 调用约 `2276999`。

**本版本没有 `acceptedAt` 字段。** 不要把 state 文件 mtime 或清单 `createdAt` 描述成精确上传成功时间；首次发现旧状态也不是刚刚上传。通知应写“新发现一条既有接受记录”或“客户端接受记录发生变化”。

`recordFailureCountAtTurnBoundary` 只在活跃组 `attemptCount > 0` 且尚未标记 `failureCountedAt` 时增一次 `failureCount`。这个总计数与“重试 564 次”一类概括不能直接等同。

关联时须使用 **accepted 自己的 hash**，不能把正在排队的较新 manifest 或只出现过的其他 manifest 归入已接受结果。即使 hash 匹配，其弱内容标识仍要求使用“客户端记录”措辞。

## 7. 采集触发与没有信号的解释

`captureBeforePromptUnsafe` 首先取得 token，然后调用 `getUploadKey`；没 token 或没有返回凭据会提前返回，不创建后续快照。它还受本地调度、取消信号和磁盘预算约束。`captureBeforePrompt` 对非空 `workspaceIdentity` 有提前跳过逻辑。

能看到 `captureStage: "prompt"` 及 Repo Wiki 的 `"terminal"` 调用路径，但这不证明每次输入必定产生快照，也不证明服务端目前会给凭据。仅安装或未登录状态下观察不到 checkpoint 是预期可能结果，不能拿来宣称“版本已经修复”或“监测失效”。

代码定位：`captureBeforePromptUnsafe` 约 `2254670`；prompt 调用约 `950424`；terminal 路径约 `2198662`。

## 8. 隔离测试的数据目录入口

在 `out/main/index.js` 约 `23900–24800` 发现以下环境入口，约 `672300` 设置 Electron paths：

| 入口 | 静态用途 |
|---|---|
| `ZCODE_DATA_BASE_DIR` | `<base>/.zcode` 与 host 数据根 |
| `ZCODE_DESKTOP_APPLICATION_NAME` | app name / process title |
| `ZCODE_DESKTOP_HOME_DIR` | Electron `app.setPath("home", ...)` |
| `ZCODE_DESKTOP_USER_DATA_DIR` | Electron userData |
| `ZCODE_DESKTOP_SESSION_DATA_DIR` | Electron sessionData |

只传 `ZCODE_DATA_BASE_DIR` **不够**。main 启动的非常早期，`resolveBootstrapSettingsFile` 使用 Node `os.homedir()` 找 `.zcode/v2/setting.json`；这个步骤发生在 Electron home override 之前。全局 instructions 也优先读取 `process.env.HOME` 下的 `.zcode/AGENTS.md`。

因而后续若要测试，需在专门子进程环境里设置独立 HOME，并指定上表各路径，不能改用户全局环境；清理继承环境中的真实服务凭据、代理和 Agent 配置变量。使用合成工作区。此处只证明程序存在这些入口，**未启动验证路径落点**。

main 约 `699483` 调用了 `requestSingleInstanceLock`。独立 userData 是否足以获得完全独立实例、登录回调是否路由正确，需要实际启动测试；不能预先承诺。`--user-data-dir` 的搜索命中主要来自 Chrome helper，不应把它当作已经证实的 ZCode profile 参数。

目录 override 也不能隔离 macOS Keychain、浏览器登录、系统隐私权限或整台机器文件。真正的防访问测试仍需独立 OS 用户 / VM / 已验证的受限进程环境。纯目录隔离只能叫“独立数据环境”。

## 9. 本次完成与剩余工作

- 完成：ASAR 只读读取、身份和 hash 固定、manifest / extra / state 字段核对、attempt / accepted 调用链核对、独立数据入口定位。
- 完成：只读检查器在实际 ASAR 上多次成功检索；没有引入第三方依赖。
- 未做：启动 ZCode、登录、联网请求、抓包、生成真实 checkpoint、验证服务器当前开关、用户系统通知授权与送达、长任务兼容测试。
- 必须使用合成 fixtures 验证：普通清单、额外清单、活跃/旧 pending 别名去重、接受与新 pending 同时存在、attempt 但未发送、旧 accepted 首次发现、损坏/超大/未知 schema、恶意绝对路径和 symlink、升级成未知版本。

这份契约足够开始编写保守的只读解析器，不能替代真实 ZCode GUI 的端到端验收。
