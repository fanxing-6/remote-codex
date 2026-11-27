# remote-codex 安全说明

数据服务与控制面板分别监听 `127.0.0.1`，控制面板使用独立端口。公网通道仅连接数据端口。请在拥有使用权限的电脑上运行，并保护本机账户。

首次启动会生成 256-bit 随机 Owner Password，配置中仅保存 scrypt 哈希。通过本机控制面板可设置新的密码。配置和认证状态使用原子写入，macOS 文件权限为 `0600`，Windows 使用当前用户专属 ACL。

Quick Tunnel 默认使用 HTTP/2，运行所需的配置文件由程序独立管理。临时 HTTPS 地址仅保存在内存中，重新启动通道后地址可能改变。服务对请求执行 Host、Origin、CSRF 和会话校验，公网 Cookie 保持 Secure、HttpOnly 和 SameSite=Strict。

二维码包含五分钟有效的一次性登录票据。永久直登链接可重复使用，应按密码保管。修改密码或吊销全部会话会同时作废临时票据、永久票据及设备会话。

固定域名模式支持 Cloudflare Named Tunnel，也支持使用 TCP 转发及 TLS-ALPN-01 自动证书。原生 TLS 使用独立监听端口，需将公网 443 端口通过 TCP 转发到该端口，并保持证书申请所需的 TLS 协议。

宿主连接只支持经过验证的应用版本。macOS 使用内嵌的 Inspector agent，并在引导结束时关闭临时 Inspector。诊断和进程发现操作仅查询进程信息。`--no-host` 可关闭自动宿主连接。

运行验证脚本时使用项目 `.work/` 内的独立配置和空闲端口。涉及宿主连接的端到端测试需要独占测试用 ChatGPT 应用；公网网络测试通过 `REMOTE_CODEX_NETWORK_TESTS=1` 显式启用。
