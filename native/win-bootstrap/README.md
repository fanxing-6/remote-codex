# Windows Bootstrap

已验证 Windows 11 AMD64、`OpenAI.Codex` 应用版本 `26.1117.41037`。Bootstrapper 由 `internal/host/windows_bootstrap.py` 与 `windows_bootstrap.js` 组成，通过 `go:embed` 内嵌于 Windows 可执行文件。

进程发现同时检查当前用户、交互会话、MSIX manifest、`owl-app.ini`、主进程参数和 `app.asar`。实际运行路径由进程获取；进程身份绑定 `pid + startedAt + executablePath`。

目标 `chrome.dll` 的 SHA-256 固定为已验收值。Frida 通过 `win64` ABI 调用公共导出函数；函数签名、隐藏返回指针和对象布局由目标二进制的 PE 导出与反汇编确认。`HandleScope` 为 24 字节，`TryCatch` 为 48 字节；`Local` / `MaybeLocal` 使用明确的返回缓冲区。版本或二进制摘要变化会终止附着。

调度只接受唯一的 `CrBrowserMain`。在原有 `uv_run` 入口核验 Isolate、Node Environment 与所属事件循环，在该线程初始化 `uv_async`。Shim 载入发生在随后正常执行的主线程回调中；回调确认线程与 Isolate，进入主 Context，并按顺序释放 `TryCatch`、Context 和 `HandleScope`。`uv_close` 回调返回后才卸载 Frida agent。

启动过程保持 Inspector 关闭。内部 IPC 使用随机回环端口，凭据仅经标准输入和 `Authorization` 传递。Go 适配器复用双端的握手、探活和卸载流程；卸载确认隐藏窗口销毁、内部端口释放与原窗口保留。

运行环境为交付目录旁的 `.venv`，依赖 `frida`、`psutil`、`websocket-client`。构建在 Mac 上交叉编译，运行期使用官方应用和 Python 环境。控制面板的自启开关注册当前用户的 Interactive Scheduled Task。

验收结果见 [Windows 验收记录](../../docs/VALIDATION-windows.md)。
