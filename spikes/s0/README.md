# S0 实验探针

这些是 macOS 合成实验，不是可安装的 Laodi 防护程序，也不是生产 sandbox 策略。

基础探针使用已有 Python、Git、Node、Clang 和 `/usr/bin/sandbox-exec`。可选 runtime 探针使用临时目录中的固定版本 sandbox-runtime，不是全局或产品依赖。测试目录由 `TemporaryDirectory` 创建和清理；Git 的 HOME、配置、hooks 和签名与用户环境隔离。

```bash
python3 spikes/s0/security_probe.py
python3 spikes/s0/compatibility_probe.py
python3 spikes/s0/watcher_probe.py
python3 spikes/s0/real_cli_probe.py --output .omx/experiments/s0-real-cli-results.json
```

- `security_probe.py`：主进程/子进程/语言原生读取、Git 布局、回环接收端正反对照，以及环境、FD、硬链接、外部 helper 的边界实验。
- `compatibility_probe.py`：11 个普通开发场景，受保护/无保护各 5 次；小样本耗时不是性能基准。
- `watcher_probe.py`：kqueue 合成目录与文件变化；不请求系统通知权限。
- `real_cli_probe.py`：已有 Codex CLI＋本地假 Provider，不使用真实账号或模型。
- `runtime_probe.py --runtime-dir <临时安装目录>`：验证固定0.0.76后端。测试不会自动下载依赖，缺少目录则失败；安装来源与integrity见结果及锁快照。

结果写入 `.omx/experiments/`。安全实验的 `boundary` 场景有意验证仍可泄露的路径，**expectation_met 不等于防护成功**。有失败/泄露是 S0 的重要结果，不应删掉这些测试把通过率做高。

接收端只绑定 `127.0.0.1`，只处理合成数据，退出时关闭。本地 TCP 端点限制不能证明域名过滤、TLS 检查或真实厂商云接口兼容性。

可选后端复跑需要用户显式准备临时依赖（测试结束可删除自己创建的临时目录；不要对未知路径执行清理）：

```bash
laodi_runtime_dir=$(mktemp -d /private/tmp/laodi-runtime.XXXXXX)
npm install --prefix "$laodi_runtime_dir" --save-exact --ignore-scripts --no-audit --no-fund --registry=https://registry.npmjs.org @anthropic-ai/sandbox-runtime@0.0.76
python3 spikes/s0/runtime_probe.py --runtime-dir "$laodi_runtime_dir"
python3 spikes/s0/real_cli_probe.py --runtime-dir "$laodi_runtime_dir" --output .omx/experiments/s0-real-cli-runtime-results.json
```

下载阶段如需网络代理，仅为该次 npm 命令指定用户已有的代理；不要为测试改系统配置。运行探针使用隔离环境和本地目标，不向真实代理或模型服务发送测试数据。本轮临时 runtime 已清理，锁文件作为来源记录保留；它不表示项目已依赖或全局安装 runtime。

严禁将实验脚本改成读取真实用户的凭据、Git 历史再发送到公网；真实客户端必须使用单独 HOME、配置与本地 stub，不复用登录会话。

后续非阻断Go开发版及某APP静态契约验证见[开发版使用说明](../../docs/DEVELOPMENT.md)；S0阻断探针保留作研究，不是当前V1默认功能。

具体结果和依赖锁快照仅保留在本地 `.omx/experiments/`，不随源码发布。锁快照用于来源核对，不应直接当安装锁文件使用。
