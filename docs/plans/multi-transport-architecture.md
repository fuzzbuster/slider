# Slider WebSocket、HTTP/2 与 QUIC 传输层架构

## 1. 文档状态

- 用途：方案讨论，不作为当前实现承诺。
- 范围：WebSocket、HTTP/2、QUIC 三种传输方式，以及 Connection Profile配置层。
- 约束：尽量减少 Server/Gateway 核心逻辑修改；保持现有 WebSocket Client 独立和兼容。
- 已确认：第一版只覆盖 Agent/Beacon 主动连接，不覆盖反向 Listener、Callback
  和 Gateway 主动出站。
- 非目标：本阶段不修改代码，不确定最终第三方库。

Connection Profile由Cobalt Strike C2 Profile的HTTP通信形态配置衍生而来，但
扩展到非HTTP transport。详细边界见
[Connection Profile v1](connection-profile-v1.md)。

## 2. 当前模型

当前数据链路为：

```text
TCP/TLS -> HTTP/1.1 Upgrade -> WebSocket -> net.Conn adapter -> SSH -> Slider channels
```

WebSocket 握手通过 `Sec-WebSocket-Protocol` 和
`Sec-WebSocket-Operation` 识别 Slider 连接。握手完成后，
`pkg/sconn/wsconn.go` 将消息式 WebSocket 转换为 `net.Conn`，
再交给 `ssh.NewClientConn` 或 `ssh.NewServerConn`。

目标模型为：

```text
WebSocket adapter ─┐
HTTP/2 adapter ────┼─> net.Conn -> SSH -> Slider Session
QUIC adapter ──────┘
```

SSH、Session、Gateway 路由和应用 channel 不应感知具体传输协议。

## 3. 三种传输方案

| 维度 | WebSocket | HTTP/2 双向流 | 原生 QUIC |
|---|---|---|---|
| 底层 | TCP/TLS | TCP/TLS | UDP/QUIC/TLS |
| 数据承载 | Binary message | Request/Response DATA frame | 双向 QUIC stream |
| Server 入口 | HTTP Upgrade handler | HTTP/2 handler | QUIC listener |
| `net.Conn` 适配 | 需要消息缓冲 | 请求体与响应体组合 | QUIC stream adapter |
| CDN/反向代理兼容 | 最成熟，但仍需验证超时 | 必须验证全双工流式转发 | 通常要求直连或 L4 UDP转发 |
| 弱网和网络迁移 | 一般 | 一般，仍受 TCP 影响 | 最好 |
| Client 依赖增量 | 当前基准 | 较小 | 较大 |
| URL 语义 | 有 | 标准请求模式下仍有 | 无 |
| 主要定位 | HTTP/1.1 Upgrade | 标准 HTTP/2 封装 | UDP直连 |

三种 transport地位对等，不将 WebSocket称为 legacy，也不将任何一种协议
作为其他模式的隐式回退。一个 Profile只选择一种 transport。

Connection Profile按连接边绑定，而不是按进程绑定。因此不同跳可以使用不同协议：

```text
Agent --WebSocket--> Beacon --HTTP/2--> Server
```

Beacon的入站 Listener和上游连接分别加载 Profile。其 Client构建必须包含两个
Profile引用的 transport能力，但每条连接仍只使用一个 transport。

## 4. HTTP/2 双向流的含义

HTTP/2 的一条请求 stream 可以同时承载两个方向：

```text
Request Body   Client  -> Server
Response Body  Client <-  Server
```

服务端收到请求后立即返回并 flush `200` 响应头，然后：

- 服务端 `Read` 从 `Request.Body` 读取。
- 服务端 `Write` 向 `ResponseWriter` 写入并 flush。
- 客户端 `Write` 写入请求体对应的 `io.PipeWriter`。
- 客户端 `Read` 从响应体读取。

组合后的对象向 SSH 提供连续、可靠、有序的字节流。这里的 HTTP Client
仅表示请求发起方，与 Slider 的 Agent、Operator、Gateway 角色无关。
Bind、Listener、Callback 或反向端口转发也不决定 HTTP Body 的方向。

## 5. HTTP/2 能否不依赖 URL Path

### 5.1 协议事实

如果采用普通 `POST`，HTTP/2 请求仍然必须携带 `:scheme`、`:authority`
和 `:path`。HTTP/2 只是把这些字段编码在 HEADERS frame 中，并没有取消
URL 语义。

传统 HTTP `CONNECT` 可以主要通过 `:authority` 指定目标，并省略普通
请求的 `:path`，但它具有隧道和代理语义。Extended CONNECT 又会引入
`:protocol`，并通常重新要求 `:scheme` 和 `:path`。如果目标是不采用
代理隧道语义，不能一边使用普通 POST，一边要求线上完全不存在 path。

### 5.2 可以消除的是“对固定 path 的协议依赖”

可以让 Server 不再通过核心代码硬编码的 URL path 判断是否为 Slider：

```text
共享 HTTP Listener
  -> Web Console等保留路由
  -> HTTP/2 handler接收 Profile允许的请求形态
  -> 读取并验证传输元数据
  -> 建立 SSH
```

此时 path 仍存在于 HTTP/2 请求中，但只是 Profile 控制的 HTTP 字段，
不再承担以下职责：

- 不作为 Slider 协议版本标识。
- 不决定 Agent、Operator 或 Callback 角色。
- 不作为身份认证依据。
- 不与 Session 路由绑定。

如果控制面和数据面共用同一个 hostname、端口和 HTTP router，则不能只凭
“这是 HTTP/2 连接”接管整条连接，因为浏览器和普通 API 也可能使用 HTTP/2。
这种部署必须依赖 path、method、header 或其他明确的请求级匹配条件。

ALPN `h2` 只能表示 HTTP/2，不能单独表示 Slider。自定义 ALPN 后直接传输
Slider 数据虽然可以完全取消 URL，但那已经不是标准 HTTP/2 Web 服务模式，
更接近自定义 TLS 协议。

### 5.3 推荐选择

HTTP/2 模式采用普通双向流式请求，但让 Profile 决定 method、path 和允许的
header集合。一个 Listener绑定一个数据 Profile，并按以下顺序处理请求：

```text
Web Console、认证、健康检查等保留路由
-> 当前 Profile matcher
-> 普通页面、模板或兜底响应
```

Profile不能覆盖 Web Console保留路由。这样可以避免将 `/slider/h2` 一类固定
路径永久写入核心协议，但标准 HTTP/2请求仍然具有 path。

一条 H2 TCP连接只建立一条 HTTP/2 stream，并承载一个 SSH会话。第一版不使用
H2多 stream复用多个 SSH会话。

第一版同时允许TLS + ALPN `h2`和直连h2c prior knowledge，但不实现
HTTP/1.1 `Upgrade: h2c`。h2c不承诺经过CDN或代理，并强制要求SSH fingerprint。
H2适配器必须实现 `CloseWrite`：Client侧结束request body，Server侧结束
response body；完整关闭、Context取消和超时必须解除双向阻塞。

HTTP匹配规则固定为：

- method未配置时默认 `POST`。
- path必填，v1只支持规范化的 `exact`匹配；query不参与匹配。
- `/a`与 `/a/`不同，拒绝编码斜杠、`.`和 `..`等歧义形式。
- header名称大小写不敏感，值去除首尾OWS后精确匹配。
- 重复header不拼接，任一独立值匹配即通过。
- 匹配失败默认返回404，第一版不配置失败响应。
- 不限制Tunnel Body总大小，只限制header和握手元数据。

Path结构预留未来 `glob`模式，其中 `*`只匹配一个路径段，`**`允许跨路径段，
但第一版解析器拒绝非 `exact`模式。

## 6. Profile 的职责边界

Connection Profile应描述线上传输契约，而不是承载本地运行策略或安全身份：

```text
profile
├── transport: websocket | h2 | quic
├── HTTP method 与 path 规则（仅 HTTP 模式）
├── 请求和响应 header 规则
├── 固定的 agent operation 映射
├── header和握手元数据限制
└── H2/QUIC ALPN选择
```

不应放入 Profile：

- SSH 私钥、证书私钥或长期认证秘密。
- 绕过 SSH fingerprint 验证的开关。
- 认证失败后的协议降级策略。
- 直接决定授权结果的未认证 HTTP 字段。
- connect、handshake、keepalive和关闭超时。
- Tunnel Body总大小限制。

Profile 可以改变线上 HTTP 形态，但不能改变内部语义。所有 Profile 最终必须
归一化为：

```text
net.Conn + TransportKind + OperationAgent + RemoteAddr
```

第一版 Profile仅用于被控 Agent/Beacon主动连接 Listener。Operator、
Callback、Gateway主动出站和 Client Listener暂不接入 Profile。

本地Node Profile通过binding为Connection Profile提供endpoint、bind address、
TLS证书路径、SSH fingerprint和timeout。节点运行角色仍由现有 `--listener`、
`--beacon`和 `--gateway`参数决定；这些角色参数可以与 `--profile node.yaml`
同时使用。其他重叠连接参数禁止与 `--profile`混用。

任何没有验证外层对端身份的upstream binding都必须配置SSH fingerprint，包括
明文WebSocket、h2c以及任意transport的 `tls.verify: false`。该条件必须在建立
网络连接前校验。

## 7. QUIC 与 URL

原生 QUIC 不属于 HTTP，因此不需要 URL、method 或 path：

```text
UDP endpoint
  -> TLS 1.3
  -> ALPN: slider/1
  -> QUIC connection
  -> bidirectional stream
  -> transport preface
  -> SSH
```

QUIC 仍需要主机/IP、UDP 端口、TLS ServerName 和 ALPN。它避免的是 HTTP
资源定位语义，不是连接目标本身。

如果选择 HTTP/3 或 WebTransport，URL、authority 和 HTTP request
语义会重新出现。因此对于原生 Go Client 到 Slider Server 的连接，
原生 QUIC 比 WebTransport 更符合“无 URL/path”的目标。

QUIC同样存在 CDN和代理适配问题，但形式不同：

- CDN支持浏览器 HTTP/3不等于支持任意 QUIC应用协议。
- 原生 QUIC需要直连 Origin、L4 UDP转发，或者能够理解自定义 ALPN的终止器。
- 企业 HTTP代理通常不能直接转发原生 QUIC。
- 若引入 WebTransport或 CONNECT-UDP，应作为新的 transport设计，而不是视为
  原生 QUIC的透明代理。

QUIC始终使用TLS 1.3。如果本地binding关闭外层证书身份验证，必须同时配置SSH
fingerprint。QUIC的正式目标包括在NAT rebinding或本机IP变化时保持QUIC连接，
并尽量维持其上的SSH会话。

## 8. 最小化 Server/Gateway 修改

服务端核心只增加统一接入函数：

```go
serveTransport(conn net.Conn, meta TransportMetadata)
```

职责包括：

1. 根据已校验的 metadata 设置 Session role。
2. 创建并登记 Session。
3. 调用现有 SSH server/client 建连逻辑。
4. 在连接结束时执行统一清理。

各协议入口只负责生成 `net.Conn` 和 metadata：

```text
WebSocket handler -> wsConn adapter ----┐
HTTP/2 handler    -> h2StreamConn ------├-> serveTransport
QUIC listener     -> quicStreamConn ----┘
```

现有 WebSocket 路径、header 和握手保持不变，以保证旧客户端继续工作。
HTTP/2 可以复用当前 TCP/TLS listener。QUIC 必须增加 UDP listener，
但 TCP 443 与 UDP 443 可以使用相同端口号。

Server和 Gateway也使用显式transport build tag。运行时只启动Node Profile当前
binding需要的数据transport；Web Console继续使用现有TCP HTTP(S)入口，并且其
`/console/ws`依赖不受数据transport tag控制。

## 9. 构建隔离

所有节点使用显式Go build tag控制数据transport能力：

```text
-tags transport_ws                 -> WebSocket
-tags transport_h2                 -> HTTP/2
-tags transport_quic               -> QUIC
-tags "transport_ws transport_h2"  -> WebSocket + HTTP/2
```

所有已编译 transport提供相同的拨号边界：

```go
dialTransport(ctx, config) (net.Conn, error)
```

普通Agent优先使用单transport构建以控制体积；Beacon可以根据上下游拓扑组合
多个tag。例如WebSocket入站、H2上游要求Beacon同时编译WS与H2。

Node Profile通过 `--profile`在启动时动态解析YAML。build tag只决定二进制能力；
每条连接由对应 Profile选择一种 transport。Profile要求的 transport未被编译时
必须启动失败，不进行自动回退。

没有transport tag时源码仍可编译，但任何需要数据连接的模式在启动网络资源前
必须失败。CI和Release构建必须显式指定tag。

Timeout和keepalive位于本地binding，不属于Connection Profile。第一版区分
connect、transport handshake、SSH handshake、keepalive response和close
timeout，不提供会关闭正常闲置Shell的数据idle timeout。

## 10. 建议推进顺序

1. 提取统一 `net.Conn` 接入点，保持WebSocket和Web Console行为不变。
2. 使用现有WebSocket完成回归验证。
3. 定义Node Profile、Connection Profile schema和YAML严格校验。
4. 完成H2双向流及H2到H1.1降级gap的设计。
5. 设计Cloudflare、Nginx、组合链路和企业代理的验证矩阵。
6. 根据验证结果决定H2实现和发布范围。
7. 单独设计QUIC的直连、L4 UDP转发和终止边界。
8. 实现前为每种候选方案定义Shell、Vim、1 GiB、Race和公网Gateway验收。

## 11. 验收指标

三种模式使用相同 SSH 与应用负载：

- 首次连接及重连耗时的 p50/p95。
- Shell 按键往返延迟。
- 1 GiB SFTP 上传和下载吞吐、CPU、内存。
- 丢包和高 RTT 下的吞吐与会话稳定性。
- Wi-Fi/蜂窝网络切换后的会话表现。
- Client 裸二进制及压缩后体积。
- Gateway 多跳场景下的断线和资源清理。
- Cloudflare、Nginx和企业代理链路的协议转换、buffering和超时行为。

最终选择应依据这些数据，而不是仅依据协议特性。
