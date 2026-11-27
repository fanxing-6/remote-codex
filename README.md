# Remote Codex

把电脑上的 **ChatGPT / Codex 桌面版** 直接搬到手机或任意浏览器里用——不修改官方安装包，不破坏签名，装好扫码就能连。

[English](README.en.md) · [安全设计说明](docs/SECURITY.md)

---

## 为什么要做这个

ChatGPT 桌面版（尤其是 **Work / Codex 模式**，带本地工作区、本地工具和电脑上的完整会话）只能守在 Mac 或 Windows 电脑前用。离开工位、躺床上或者在外面时，想看看任务跑完没有、顺手发一条新指令都很不方便。

**Remote Codex** 会在内存中无侵入挂载到本机运行中的 ChatGPT 桌面端，把它的原生界面完整透传到手机/电脑浏览器里，并打包成 macOS 和 Windows 原生可安装软件。打开应用、手机扫码，几秒钟就能直接开用。

默认情况下，**不需要买域名、不需要注册 Cloudflare 账号、不需要装 Python、也不需要敲命令行**。

## 核心特点

- **下载即用，默认零配置** — 提供 macOS `.dmg` 与 Windows `.exe` 安装包。首次打开自动生成强随机密码、自动拉起 Cloudflare Quick Tunnel（`trycloudflare.com`，本机没装 `cloudflared` 会自动下载），窗口里直接弹出扫码直登二维码。
- **也支持自有域名：更快、固定地址、永久免登** — 如果你有自己的域名，可以一键切到「固定域名模式」：
  - 内置 **Let's Encrypt 原生自动 TLS（TLS-ALPN-01）**，直接配合 SakuraFRP / frp 做 TLS 透传，国内直连速度快且端到端加密（中转节点看不到明文）；
  - 也支持 Cloudflare Named Tunnel / Cloudflare Access；
  - 登录后会话持久化（重启软件不掉登录），并支持生成可收藏书签的**永久直登链接**，随时一键吊销。
- **真正认真调过手机端体验** — 直接把桌面 Electron 界面放到手机浏览器里其实坑非常多，这里把最影响日常使用的几个全修掉了：
  - **软键盘对齐**：结合 `interactive-widget=resizes-content` 与 `visualViewport` 实时同步可视高度，弹出输入法时输入框紧贴键盘上沿，不会被顶出屏幕。
  - **不再莫名其妙弹输入法**：拦截桌面端几百处自动聚焦（`autoFocus` / `focusComposer` / 关菜单回焦），并在手动收起软键盘时同步失焦；只有你真的手动点进输入框才会弹键盘。
  - **Android 本地文件管理器 & Work 附件上传**：在 Android 上放开限制性 `accept`（保留独立拍照入口），不再被锁死在“照片和相册”；同时给「工作」会话接入浏览器附件通道，手机选的任意文件可直接传回电脑宿主。
- **零修改官方 App** — 纯只读读取 `app.asar` 静态资源，通过 Electron 内置的 Node.js Frida 在内存注入轻量 Shim，不动官方应用文件与代码签名。
- **单控制权防冲突** — 同一时间只允许一个浏览器页面控制宿主，换设备打开支持一键「接管控制权」，避免多端同时操作把状态写乱。

## 快速开始

### 1. 安装并打开

前往 [GitHub Releases](../../releases) 下载对应系统的安装包：

- **macOS（Apple Silicon）**：`Remote-Codex-*-mac-arm64.dmg`
- **Windows（x64）**：`Remote-Codex-Setup-*.exe`

先启动电脑上的官方 ChatGPT 桌面版，再打开 **Remote Codex**。

> **macOS 提示**：当前发布的安装包未经过苹果开发者签名。如果系统提示“已损坏”或“无法验证开发者”，在终端执行一次：
> ```bash
> xattr -dr com.apple.quarantine "/Applications/Remote Codex.app"
> ```

### 2. 手机扫码直登

软件默认以 **一键临时公网（Quick Tunnel）** 启动：

1. 等几秒自动拿到临时的 `https://*.trycloudflare.com` 入口地址；
2. 用手机扫描控制面板里的二维码；
3. 自动完成免密登录（直登票据放在 URL 的 `#ticket=` 片段中，不会经过任何代理或服务端日志，打开后自动抹除地址栏并写入安全 Cookie）。

关掉控制面板窗口后，程序会缩到菜单栏 / 系统托盘继续在后台运行。

---

## 进阶：使用自己的固定域名

Quick Tunnel 零门槛、上手最快，但有两个天然限制：每次重启都会换新域名，而且国内不开梯子走 Cloudflare 节点延迟偏高。

如果你需要**固定书签、重启不掉登录、国内直连高速**，可以在控制面板切到「固定域名模式」：

### 方案 A：SakuraFRP / frp + 原生自动 TLS（国内直连最推荐）
1. 在 DNS 把域名（例如 `chat.example.com`）CNAME 到你的 frp 节点（如用 Cloudflare DNS 请设为**仅 DNS / 灰云**）。
2. 在 frp 创建 HTTPS 隧道，**关闭服务端自动 HTTPS**，做纯 TLS 透传指向本机 `127.0.0.1:18443`。
3. 在 Remote Codex 配置中启用 `tlsPort: 18443`、`tlsAutocert: true`。程序会自动向 Let's Encrypt 申请并续签正式证书，私钥只留在本机。

### 方案 B：Cloudflare Named Tunnel / Access
- 将隧道规则指向本机数据面 `http://127.0.0.1:18765`（**切勿暴露本地管理端口 `18766`**）。控制面板内置 `config.yml` Diff 预览与自动备份，确认后才写入。

切到固定域名后，你可以在控制面板点击「**生成并复制永久直登链接**」（或执行 `remote-codex ticket --permanent`）把链接存进手机书签。万一需要作废，点「**吊销全部会话与直登链接**」或修改主密码，所有设备会话和永久链接都会立刻失效并断开 WebSocket。

---

## 架构简图

```text
手机 / 电脑浏览器 (HTTPS + WSS)
        │
        ▼
Cloudflare Quick Tunnel (默认)   或   自有域名 (frp + 内置自动 TLS / CF Tunnel)
        │
        ▼
Remote Codex 桌面端 (Electron)
  ├── 原生控制面板窗口 & 系统托盘
  ├── 内置 Node.js Frida 挂载器（桌面版零 Python 依赖）
  └── Go 核心 Sidecar（仅绑定 127.0.0.1）
        ├── 公开数据面 (:18765 / TLS :18443)  ── 鉴权、纯 Go ASAR 读取、CBOR WS 网桥
        └── 本地管理面 (:18766)               ── 严禁暴露公网
        │
        ▼  本机回环受控 IPC（单次会话 Bearer Token）
官方 ChatGPT 桌面版（仅内存轻量 Shim，原包零改动）
```

代码按职责清晰分层：

- **`desktop/`**：Electron 桌面壳，拆分为 `SidecarManager`、`WindowManager`、`TrayManager`、`CloudflaredManager` 与零 Python 的 `Node-Frida` 挂载器。
- **`cmd/remote-codex/` 与 `internal/`**：Go 核心引擎，抽象统一的 `tunnel.Provider`（`QuickProvider` / `CustomProvider`）与 `host.Bootstrapper` 接口，内置纯 Go ASAR 读取器、`autocert` 自动证书、仅存哈希的持久化认证存储（`auth-state.json`，`0600`）与单控制权仲裁器。
- **`web/`**：浏览器端兼容层（`browser-transport.ts`、`mobile-runtime.ts`、`mobile-layout.css`）、宿主内存 Shim 与本地控制面板前端。

---

## 安全说明

- **管理面与数据面彻底隔离**：管理端口仅监听 `127.0.0.1:18766`，任何公网入口均无法访问 `/admin` 或管理接口。
- **默认绝不无密码裸奔**：首次以 Quick Tunnel 启动时自动生成 256-bit 随机主密码并以 `scrypt` 哈希保存。
- **磁盘零明文凭据**：持久化文件 `auth-state.json`（`0600`）仅保存 Session 与永久 Ticket 的 SHA-256 摘要。
- **官方应用签名完好**：全程不修改 `/Applications/ChatGPT.app` 或 Windows MSIX 安装目录。

更多细节见 [docs/SECURITY.md](docs/SECURITY.md)。

---

## 从源码构建

需要 **Go 1.24+** 与 **Node.js 22+**。

```bash
# 编译 Go 核心与内嵌前端资源
make build

# 运行 Go 竞态测试与静态检查
make test
go vet ./...

# 打包 macOS 安装包（产物在 desktop/release/）
make package-mac

# 打包 Windows 安装包（需在 Windows 上执行，产物在 desktop/release/）
make package-windows
```

### 纯命令行无头模式（可选）

如果不想用 Electron，只想单独跑 Go 二进制：

```bash
./scripts/setup-python.sh      # 仅独立命令行模式需要一个小型 Python venv 跑 Frida
./bin/remote-codex             # 默认启动 Quick Tunnel 并在浏览器打开控制面板
./bin/remote-codex status
./bin/remote-codex doctor
./bin/remote-codex ticket --permanent
./bin/remote-codex revoke-all
```

---

## 兼容性与声明

- 已在 **macOS（Apple Silicon）** 与 **Windows 11（x64）** 的 ChatGPT / Codex 桌面版 `26.1117.41037` 上实测通过。若官方未来大版本调整底层 Electron/V8 结构，可能需要同步更新宿主适配。
- **非官方开源工具**：本项目为独立开源工具，与 OpenAI 无任何隶属或合作关系，仅供个人安全访问自己电脑上运行中的桌面客户端。

## 许可证

[MIT](LICENSE)
