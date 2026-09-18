# S0：macOS 正式分发与授权核验

核验日期：2026-09-18。范围：Apple 官方公开资料、当前主机的只读开发工具检查。本文不代表 Apple 已批准 Laodi，不代表已安装或运行 Endpoint Security / Network Extension。

## 结论

**正式路线存在，但当前只完成了路径核验，尚未完成账号、签名、受限能力和安装实测。** 建议采用站外分发的 Developer ID 签名并公证的原生宿主 App，内嵌文件和网络系统扩展。Apple 官方支持 macOS 内容过滤 System Extension 的 Developer ID 分发；Endpoint Security（ES）另需 Apple 批准受限 entitlement。[S1][S2][S3]

用户目前没有／不确定是否有付费 Apple Developer 账号，按“未确认可用”处理。**现在不用为了 S0 先付费，也不用关闭 SIP。** 先完成无账号的合成探针、兼容性测试和申请材料；是否投入正式系统扩展，应结合这些结果及 Apple 对申请资格的答复决定。

以下条件不能互相替代：

| 条件 | 解决什么 | 不代表什么 | 当前证据 |
| --- | --- | --- | --- |
| Developer Program 身份 | 取得正式签名和相应开发者资源的前提 | 不等于 ES 已获批 | 未确认账号可用 |
| Developer ID 签名 | 站外发行的发布者身份和代码完整性 | 不等于公证、FDA 或用户批准 | 尚未签名 |
| Apple 公证 | 发布包通过 Apple 自动安全检查 | 不等于功能正确、授权已生效或安全审计 | 尚未提交 |
| ES 受限 entitlement／profile | 程序可以申请连接 ES | 不等于已获用户 Full Disk Access | 未申请、未批准 |
| Network Extension capability／profile | 使用指定网络扩展类型 | 不等于用户允许网络过滤 | 未生成 |
| 系统扩展激活批准 | OS 允许扩展安装／激活 | 不等于 ES 或网络规则已正常运行 | 未触发 |
| Full Disk Access（FDA） | 用户允许 ES 客户端接入相应系统能力 | 不等于普通源码 Agent 也需要或应获得 FDA | 未请求 |
| 网络过滤确认 | 用户允许配置过滤网络流量 | 不等于能识别 TLS 中上传的文件 | 未请求 |
| 通知许可 | 告警可使用系统提示方式 | 不保证用户立即看到，仍受通知设置影响 | 未请求 |

## 1. 当前主机的实测环境

本次只读执行以下命令，并读取返回值：

```text
sw_vers
  ProductName: macOS
  ProductVersion: 26.3
  BuildVersion: 25D2125

xcode-select -p
  /Library/Developer/CommandLineTools

xcrun --sdk macosx --show-sdk-path
  /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk

xcrun --sdk macosx --show-sdk-version
  26.2

xcrun --find notarytool
  /Library/Developer/CommandLineTools/usr/bin/notarytool

xcrun --find stapler
  /Library/Developer/CommandLineTools/usr/bin/stapler

csrutil status
  System Integrity Protection status: enabled.
```

这证明 SDK 和公证命令工具可被定位，**不证明存在发布证书、可用 provisioning profile 或公证凭据**。当前选用的是 CLT；没有检查其他位置的完整 Xcode，也没有登录 Apple 账号、读取钥匙串、查询用户证书或更改开发目录。

## 2. 账号类型：可以确认与不能确认的部分

Apple Developer Program 明确接受个人和组织注册。个人无需为注册而先成立公司；组织需具备法律实体身份、相应签约权限、组织邮箱和网站，通常还需 D‑U‑N‑S 信息。两类注册都涉及身份核验与本人接受协议。官方当前列出的通常年费为 99 美元或当地币种价格。[S4]

但“个人可以加入 Program”**不能推导出**“个人的 ES 产品必然获批”。公开 ES 页面要求向 Apple 提交 entitlement 请求，没有公开保证每类申请人均获准，也没有可依赖的审批时限。申请页面转向 Apple 登录，本次未进入账号表单。因此不声称“必须公司账号”，也不声称“个人一定可以”。[S1][S3]

若用户没有会员，先向 Apple Developer Support 确认：个人开发者、开源本地 AI 编程数据访问控制产品，是否有申请 ES 发行权限的资格、需要哪些产品资料。付费不是拿到受限能力的保证。[Apple 联系入口](https://developer.apple.com/contact/)

普通 Developer ID 证书创建页面列明 Account Holder 角色；Apple 另有特定 cloud-managed 证书授权。因此后续由账号持有人控制身份、协议、证书与签名凭据，不能把这一步当成工具可以匿名完成的构建操作。[S5]

## 3. 正式发布链路

```text
确认发布者和账号资格
  → 申请 ES 受限能力
  → 登记宿主／扩展 App IDs 并启用相应 capabilities
  → 生成覆盖目标权限的 Developer ID provisioning profiles
  → 构建、逐层签名并检查 entitlements
  → 提交公证、读取结果与警告、附加公证票据
  → 在正常安全设置的测试机进行首次安装与用户授权
  → 实测阻断、兼容性、升级、重启、撤销权限、卸载
```

宿主 App 和两个扩展沿用同一个开发团队。跨团队再分发需要另一项 entitlement，本项目没有引入这个依赖。[S6]

### 签名与公证

- 宿主、CLI 和所有分发的可执行组件都要有有效签名；站外正式包使用 Developer ID。`.pkg` 如果存在，另需 Developer ID Installer；只用 `.app`／磁盘映像分发时，不因含系统扩展就必须自制安装器。[S5][S7]
- 正式构建开启 Hardened Runtime、带安全时间戳，不能残留启用的 `get-task-allow`。使用 `notarytool` 或 Xcode 的发行流程提交；成功后检查日志并使用 `stapler` 附票据。公证验证本身不能代替功能测试。[S7]
- `ad-hoc` 签名、开发签名以及绕过 Gatekeeper 的本机成功都不能用来通过正式发布验收。
- 未来流水线可自动构建、检查签名/profile、提交并读取公证结果；前提是账号持有人已经配置安全的签名和公证访问。仓库不存密码、私钥或账号 token。

### 目标与能力分配

| Target | 计划的关键能力 | 配置要点 |
| --- | --- | --- |
| HostApp | `com.apple.developer.system-extension.install` | 用 System Extension capability；向 OS 发激活／停用请求。[S8] |
| FileGuardExtension | `com.apple.developer.endpoint-security.client` | Apple 受限批准与正确 profile；手写 plist 不会获得权限。[S3] |
| NetworkGuardExtension | `com.apple.developer.networking.networkextension` | Developer ID 内容过滤采用 `content-filter-provider-systemextension`；按官方说明启用 capability 并生成 profile。[S9] |
| 宿主网络配置 target | 对应 Network Extension capability | 用 `NEFilterManager` 安装并启用过滤配置，遵守所选项目配置和 profile；最终以 Archive 的实际 entitlement 检查为准。[S9][S10] |

这是待实现的目标分配，不是宣称只靠这几行 plist 就能发布。构建时还要核查 Bundle ID、Team ID、签名 entitlement、profile 授权是否一致。Apple 明确把这些作为系统扩展激活检查的一部分。[S11]

本次采用的 Network Extension 路线是 **macOS 内容过滤 system extension**，不是要求 iOS supervised device 的路线，也不必为此把普通用户设备纳入 MDM。TN3134 给出了 macOS 10.15 起的正式部署方式；这是 API 部署起点，不是 Laodi 已承诺支持的最低系统版本。[S2]

## 4. 用户安装时需要的真实授权

1. **把正式宿主 App 放入适当的 Applications 目录并启动。** 系统扩展嵌入 `Contents/Library/SystemExtensions`；由宿主调用 `OSSystemExtensionManager` 激活，不是 shell 复制到受保护系统位置就算安装。[S11]
2. **在系统界面批准扩展。** `requestNeedsUserApproval` 代表等待批准，必须继续显示“未启用”；不能提前显示“保护中”。macOS 26 的扩展管理在“系统设置 → 通用 → 登录项与扩展”，其中区分 Endpoint Security 与 Network Extensions。应按运行系统显示正确引导，不能照搬旧文档的旧版设置路径。[S12][S13]
3. **给 ES 组件 Full Disk Access。** `es_new_client` 需要相应 entitlement 和 TCC 同意；缺少身份、FDA、权限时分别呈现失败原因。ES 进程有 root 运行要求，但不应要求用户每天用 `sudo laodi` 启动正常产品。[S14][S15]
4. **允许网络过滤。** Apple 的网络过滤示例有扩展批准和网络过滤确认两个层次。配置已保存也不能直接当成流量已受控，须做本地端到端连接探针。[S10]
5. **允许需要的通知类型，并验证实际通知。** 宿主通过 `UNUserNotificationCenter` 请求权限并持续读取当前设置；拒绝后保留本地事件与状态入口，不谎称一定及时弹窗。[S16]
6. **按宿主后台方案确认后台运行。** 要分别显示登录项／后台项与系统扩展的状态；系统扩展能跨用户登录运行，不代表用户界面的通知组件也总在。[S2][S13]

非受管的个人 Mac 不能由 Skill 自动授予这些权限。MDM 有企业预授权机制，但不作为本项目面向普通个人用户的安装前提。[S17]

**SIP 必须保持开启。** Apple 提供的开发期宽松设置不能通过本项目的正式验收门槛；当前没有执行任何关闭 SIP、系统扩展 developer mode 或 TCC 数据库变更。

## 5. 现在可完成与需要配合的边界

| 工作 | 现在可自动完成 | 需要用户／外部条件 |
| --- | --- | --- |
| 合成仓库和无账号运行后端探针 | 是；只使用本地合成内容 | 无需 Apple 账号 |
| ES / NE 接口原型、单元测试、bundle 结构和离线验证 | 是 | 不把离线通过称为内核授权生效 |
| 申请说明、entitlement 矩阵和发布脚本 | 是 | 最终发布者信息由用户确定 |
| ES 受限能力申请 | 可备妥材料 | 账号持有人登录提交、Apple 审核 |
| Developer ID 签名／公证 | 可以准备和校验流程 | 正式账号、证书、profile、公证访问；用户保管凭据 |
| ES / NE 正常设备端到端验证 | 可以写好测试和采集证据 | 签名组件可用后，用户在系统界面批准扩展、FDA、网络过滤 |
| 通知可见性验证 | 可做消息与状态逻辑 | 有具体宿主构建后，由用户选择通知权限并确认展示 |

**最小配合不发生在这一轮只读核验里。** 之后只有两类必要配合：账号持有人完成账号／Apple 申请相关动作；签名测试包准备好后，在测试 Mac 的官方界面执行权限选择。不要把 Apple 账号密码、二步验证码、证书私钥发给 Agent。当前无须安装不明扩展、授予终端全盘访问或购买会员来解锁合成探针。

## 6. 可复用的 ES 申请材料草稿

以下是待用户确认的产品说明，不是已提交的申请；发布者身份、Bundle IDs、下载地址仍待填入真实信息。

> Laodi-skills is a local-first macOS security utility for users of AI coding applications. It aims to prevent designated applications and their descendants from reading user-selected sensitive paths, including Git object stores and credential files, while keeping ordinary source editing available. We request the Endpoint Security client entitlement for a Developer ID-distributed system extension using synchronous authorization events and process lifecycle events. Enforcement will use bounded local policies; source code and file contents will not be sent to our servers. Users will explicitly approve installation and Full Disk Access, can inspect the active policy, and can uninstall the product. Network control is handled separately through the Network Extension framework. We do not require disabling SIP. Please confirm whether an individual Apple Developer Program membership is eligible to request this entitlement for distribution, what materials are required, and whether any additional conditions apply to this use case.

还需随申请／技术说明准备：产品功能截图、数据流图、收集字段、留存期限、隐私说明、AUTH handler 的有界响应设计、权限撤销与卸载流程，以及真实项目主页。**没有来源支持审批一定需要多少天，工期里不得编造 SLA。**

## 7. 不能跨过的验证门槛

正式系统版当前状态：**NOT VERIFIED / 外部资格与执行验证待完成**。

至少取得以下证据才可改为通过：

- Apple 批准 ES 能力，实际 profile 授予期望 entitlement；用相符证书签名的正式包公证成功。
- 正常安全设置测试机激活，`es_new_client` 成功；取得 FDA 后合成历史读取实际被拒绝，撤销权限能检测并降级。
- 网络过滤配置及扩展确实处理本地允许／拒绝连接；单独记录已有连接结果，不用“安装成功”替代结果。
- 首装、升级、重启、卸载、权限撤回、异常退出全部有可重复结果。
- AUTH 热路径延迟、掉事件、策略刷新和健康告警均实测。Apple 已明确 AUTH deadline 过期会杀死客户端并隐式放行，因此“系统级”不能被解释为无条件 fail-closed。[S17]
- 签名完整性、profile 与 app 身份、最终安装包哈希作为 release evidence 保存；不把凭据或用户私密路径写入公开报告。

无账号受限启动探针即使成功，也只证明该后端在该次进程树和策略下的效果。**它不替代 ES 正式分发验证，更不自动覆盖 Finder 直接打开、已运行桌面客户端、外部服务代读、既有 FD 或已批准网络中的上传。**

## 官方来源

全部链接于 2026-09-18 核查。Apple 文档 HTML 若只显示需要 JavaScript，读取其官方 `.md` 链接正文；没有使用第三方转载代替。

- [S1 System Extensions and DriverKit](https://developer.apple.com/system-extensions/)：ES entitlement 申请入口、系统扩展概念。
- [S2 TN3134: Network Extension provider deployment](https://developer.apple.com/documentation/technotes/tn3134-network-extension-provider-deployment)：macOS 内容过滤、Developer ID 分发、全局运行环境。
- [S3 Endpoint Security entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.endpoint-security.client)：必须向 Apple 申请、缺失 entitlement 的失败。
- [S4 Program enrollment](https://developer.apple.com/help/account/membership/program-enrollment/)：个人／组织、身份前提与会员费用。
- [S5 Developer ID certificates](https://developer.apple.com/help/account/certificates/create-developer-id-certificates)：证书种类、Account Holder、profile 检查。
- [S6 System Extension Redistributable Entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.system-extension.redistributable)：默认团队一致要求。
- [S7 Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)：签名、Hardened Runtime、公证要求与工具。
- [S8 System Extension Entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.system-extension.install)：宿主安装能力。
- [S9 Network Extensions Entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.networking.networkextension)：Developer ID profile 的配置方法和扩展类型。
- [S10 Network Extensions for the Modern Mac](https://developer.apple.com/videos/play/wwdc2019/714/)：NEFilterManager、扩展批准与过滤确认。
- [S11 Installing System Extensions and Drivers](https://developer.apple.com/documentation/systemextensions/installing-system-extensions-and-drivers)：bundle、激活、更新、卸载与系统验证。
- [S12 requestNeedsUserApproval](https://developer.apple.com/documentation/systemextensions/ossystemextensionrequestdelegate/requestneedsuserapproval(_:))：等待用户批准的实际状态。
- [S13 macOS 26 Login Items & Extensions](https://support.apple.com/en-euro/guide/mac-help/mtusr003/26/mac/26)：当前 OS 对应的系统设置位置。
- [S14 es_new_client](https://developer.apple.com/documentation/endpointsecurity/es_new_client(_:_:))：ES 接入与 FDA。
- [S15 es_new_client_result_t](https://developer.apple.com/documentation/endpointsecurity/es_new_client_result_t)：包括缺 entitlement、权限、root 的错误类别。
- [S16 Asking permission to use notifications](https://developer.apple.com/documentation/usernotifications/asking-permission-to-use-notifications)：通知请求与状态检查。
- [S17 Build an Endpoint Security app](https://developer.apple.com/videos/play/wwdc2020/10159/)：FDA／受管部署、AUTH 时限与失败行为。

检索记录：网页搜索 2 个查询，主题为 Apple ES entitlement 与 Developer ID、NE system extension 分发；其后直接核对上列 Apple 原始页面和官方 Markdown。未使用 AI 搜索答案作为证据，未访问登录后的账号资料。
