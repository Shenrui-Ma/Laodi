# S0：真实 CLI 与本地假 Provider 的补充验证

日期：2026-09-18。环境：macOS 26.3 arm64，`codex-cli 0.137.0`。分别验证手写 Seatbelt 与正式选型依赖 `@anthropic-ai/sandbox-runtime 0.0.76`；二者均为实验集成，尚未形成 Laodi 产品启动器。

## 结论

**真实 Codex CLI＋本地假 Provider 的工具调用闭环已通过正反对照。** 对照组成功通过 `git show` 和直接读取 Git loose object 取出合成历史秘密，并将它放进下一次 Provider 请求。实验组的整个客户端从启动入口就受外层 Seatbelt 限制：`git show` 返回 128，直接读取历史对象得到 `PermissionError`，下一次 Provider 请求中没有合成秘密。两组都完成了当前源码读取、生成文件和简单函数测试。

| 实测项 | 对照：允许合成 `.git` | 实验：拒绝合成 `.git` |
|---|---|---|
| 真实 CLI 启动、执行命令、工具输出回送、正常结束 | 通过，退出 0 | 通过，退出 0 |
| 当前源码读取 | 通过 | 通过 |
| 生成新文件 | 通过 | 通过 |
| 简单函数测试 | 通过 | 通过 |
| `git show HEAD~1:old.txt` | 退出 0，读取秘密 | 退出 128，无法识别可读 Git 仓库 |
| 直接解压历史 loose object | 读取秘密 | `PermissionError` |
| 秘密到达本地假 Provider | 是，第 2 次请求 | 否，两次请求均无秘密 |

这证明指定任务中外层文件访问规则覆盖了真实客户端派生的 shell、Python 和 Git 子进程。不是由 stub 拒绝执行历史读取命令：两组使用同一任务脚本，stub 没有判断哪组该拒绝；实际拒绝来自操作系统文件访问规则。

同时发现并处理了代理兼容性问题：首次实验中，客户端在新建 HOME/CODEX_HOME 下仍自动选择本机系统 HTTP 代理 `127.0.0.1:7897`；仅设置 `NO_PROXY=*` 无效。新的独立实验将六个大小写代理环境变量全部显式指向假 Provider 自己，stub 接受且仅接受目标等于自身 URL 的 absolute-form 请求，不实现任何转发或 CONNECT。这个方案一次通过，始终没有放行真实 7897 端口或修改系统代理设置。

结果文件：[手写 Seatbelt](./s0-real-cli-results.json)、[sandbox-runtime](./s0-real-cli-runtime-results.json)。脚本：[real_cli_probe.py](../../spikes/s0/real_cli_probe.py)。

## 计划验证的闭环

1. 创建两份完全合成的 Git 仓库，历史提交包含随机测试标记，当前提交已删除该文件。
2. 为每份仓库创建空 HOME/CODEX_HOME、无用户凭据和无用户 hooks 的客户端环境。
3. 在整个 `/opt/homebrew/bin/codex exec` 入口外施加 Seatbelt。两组都禁止访问真实用户家目录并禁止所有网络，只允许本地假 Provider 的一个随机端口。
   `HTTP_PROXY/http_proxy/HTTPS_PROXY/https_proxy/ALL_PROXY/all_proxy` 全部指向这个相同端口，避免回落到系统代理；stub 不转发请求。
4. 对照组允许读取合成 `.git`；实验组额外禁止读取与修改合成 `.git`。
5. 本地 deterministic Responses stub 请求客户端执行一个固定的合成脚本：读取当前源码、创建文件、执行简单函数测试、尝试 `git show` 和直接读取 Git loose object。
6. 根据客户端回送给 stub 的工具输出，验证正常功能及秘密是否到达接收端。

假 Provider 没有调用任何真实模型或厂商 API，也没有账号或真实 API key。Codex 自身的命令沙箱在探针中显式关闭，目的是测试**入口外层的 Seatbelt 规则**；两组的客户端与子进程都处于该外层规则之下。实验并未因关闭内部沙箱而直接运行无保护客户端。

## 实际执行与有限重试

| 轮次 | 调整 | 结果 |
|---|---|---|
| 1 | 全新环境、仅放行 stub 端口 | 两组客户端启动，均在请求 Provider 时失败；0 请求到达 stub |
| 2 | 开启最少 HTTP 诊断，标准输入设为 DEVNULL | 日志定位到系统代理介入；0 请求到达 stub |
| 3 | 增加 `NO_PROXY=*` 与 `no_proxy=*` | 仍选择系统代理；0 请求到达 stub，停止重试 |
| 新方案 1 次 | 基于系统代理诊断，显式把全部代理变量指向同一受控 stub，并支持仅自身 URL 的 absolute-form | 两组均收到 2 次请求、完整工具闭环，正反预期全部满足 |

最终 JSON 保留新方案的两组完整脱敏日志，并在 `previous_experiments` 中保留首次实验的两组失败摘要。新方案基于新增诊断另行授权执行，未继续重复 `NO_PROXY` 猜测。所有设置仅作用于临时子进程环境，没有更改系统权限、安装服务、改动系统代理或安装依赖。

实际请求路径、工具名 `exec_command`、客户端 JSONL、拒绝错误与秘密是否到达接收端的布尔值均留在结果文件。合成秘密本身已替换为占位符。脚本语法与 JSON 解析检查也通过。

## sandbox-runtime 与真实 CLI 的联合验证

在原生规则成功后，增加 `--runtime-dir` 模式，将整个 Codex 入口交给已经单独测试的 `@anthropic-ai/sandbox-runtime 0.0.76`。依赖来自临时安装目录，未修改项目或全局依赖；其完整性摘要与 [runtime 单独验证记录](./s0-runtime-results.json) 相同。两种后端结果分别保存，原始成功记录没有被 runtime 结果覆盖。

实际配置要点：

- `denyRead` 包含真实用户家目录，受限组再加入合成仓库 `.git`；只允许合成临时目录写入。
- `network.allowedDomains` 只包含 stub 的 `127.0.0.1:随机端口`，`strictAllowlist: true`；不开放其他目的地、local binding 或任意 Unix socket。
- 使用 `/private/tmp` 下短临时目录，避免 runtime 的 Unix socket 路径超出 macOS 长度限制。
- runtime 为受限客户端注入其自身的策略代理；宿主环境中的上游代理仍只指向不转发的 stub。

第一轮失败原因是 runtime 自动将 `127.0.0.1` 加入 `NO_PROXY`，导致 Codex 尝试直接连接 stub，直连被限制拒绝。核对实际安装的 `sandbox-utils.js` 后，第二轮在受限入口使用 `/usr/bin/env NO_PROXY= no_proxy=` 清除这个豁免，保留 runtime 代理和原有允许列表，使请求真正经过 runtime 网络策略。没有放松端点规则。

第二轮两组都收到 2 次 Provider 请求并正常退出。结果与上表一致：对照组两种历史读取成功且秘密到达；受限组 Git 返回 128、直接对象读取 `PermissionError`，秘密未到达；源码读取、生成文件与简单函数断言均通过。第一轮失败摘要保存在 runtime 结果文件的 `previous_experiments` 中。

验收同时要求 `stub_error == null`、正常功能通过、客户端工具闭环完成、对照组 **Git 返回 0 与直接读取成功同时成立**，受限组 **Git 非零与直接读取 PermissionError 同时成立**，并核对接收端的秘密有无。不能仅凭聚合的“未收到秘密”判为阻断成功。

可复跑入口（运行前须按同版本锁文件在显式临时目录安装依赖；本轮临时安装目录会清理）：

```bash
python3 spikes/s0/real_cli_probe.py --runtime-dir /absolute/path/to/temporary-runtime-install --output docs/spikes/s0-real-cli-runtime-results.json
```

省略 `--runtime-dir` 则使用手写 Seatbelt 模式。脚本不会自行联网安装依赖，缺少既有 runtime 或版本不符时退出。

## 下一步及边界

下一步应使用真实厂商模型和目标客户端版本，在另行明确的测试范围内验证代表性工作流，以及实际产品后端、既有实例交接和桌面 helper。对使用代理的客户端，必须明确处理代理身份与可达目的地；不能把配置中的模型 URL 当成实际连接目标的充分证据。

本探针只证明**该版本真实 CLI＋假 Provider＋指定合成任务＋新启动受控进程树**的行为，以及所固定 runtime 版本在该条件下可集成。不能扩展为 ZCode GUI、所有桌面 helper、已运行的客户端、真实模型兼容性或 Laodi 产品后端已具备发布质量。所用 `gpt-5.4` 仅是客户端请求中的模型标识，没有调用该模型。简单函数断言不能代表完整项目构建、测试套件或智能任务质量；单次短运行也不能作为性能基准。对当前源码的正常读取仍然意味着客户端能够把该源码发送到获准目标，文件历史隔离不等于全面防上传。

配置依据：[OpenAI 官方高级配置文档](https://learn.chatgpt.com/docs/config-file/config-advanced)说明自定义 provider 的 `base_url`、`wire_api` 及项目配置边界；实际参数以本机 `codex exec --help` 为准。本次使用 OpenAI Docs 技能核对该文档。
