# Slider Connection Profile 第一版配置规范

## 1. 状态与目标

本文记录第一版 Connection Profile 的已确认设计边界。它是讨论文档，不代表
已经实现。该概念由 Cobalt Strike C2 Profile 的 HTTP 通信形态配置衍生而来，
但扩展到 WebSocket、HTTP/2 和原生 QUIC，因此不再限定为 HTTP C2 Profile。

Connection Profile 是一份由 Client 与 Server/Gateway 共同使用的通信契约。它描述
被控端主动连接 Listener 时采用的传输协议和 HTTP 交互形态，使具体 HTTP 字段
不再硬编码在 Slider 核心逻辑中。

第一版目标：

- 保持 Web Console 现有行为。
- 一个 Listener 只绑定一个数据连接 Profile。
- 一个 Profile 只使用一种 transport。
- 所有节点通过显式 Go build tag编译所需的 transport能力；普通 Agent通常只包含
  一种，Beacon可以按拓扑组合多种。
- Profile 在启动时加载，运行期间不热更新。
- SSH、Session 和 Gateway 路由不感知 Profile 的 HTTP 细节。

## 2. 第一版范围

第一版只覆盖被控端主动发起的上游连接：

```text
Agent  -> Server/Gateway Listener
Beacon -> Server/Gateway Listener
Agent  -> Beacon Listener
```

Profile按连接边绑定，而不是按进程绑定。Beacon的入站 Listener和上游连接可以
加载不同 Profile，从而允许每一跳使用不同 transport：

```text
Agent --WebSocket--> Beacon --HTTP/2--> Server/Gateway
```

当前 Client和 Beacon的上游连接仍归一化为 Agent接入语义。Beacon终止下游
transport后，只把内部 SSH字节流通过 `slider-beacon` channel转发；该内部
channel不使用 Connection Profile。

第一版不覆盖：

- Server主动连接 Client Listener。
- Gateway通过 `connect --gateway` 主动连接其他 Gateway。
- Gateway启动时 Callback。
- 一个 HTTP/2连接承载多个 SSH会话。
- Profile热更新。
- 动态脚本、模板语言或可执行转换规则。

这些路径继续使用现有行为，或者留到后续阶段单独设计。

## 3. 配置层次

### 3.1 Node Profile

`--profile node.yaml`读取本地 Node Profile。它负责将节点的连接方向绑定到命名的
Connection Profile，并保存本地部署参数：

- Listener bind address和 TCP/UDP port。
- 上游 endpoint。
- TLS证书、CA和 ServerName。
- SSH fingerprint和本地密钥路径。
- connect、handshake、keepalive和关闭策略。
- inbound和 upstream binding。

不同节点允许使用不同的 `node.yaml`。连接双方只要求对应 Connection Profile的
线上协议字段兼容，不要求物理文件相同。

### 3.2 Transport实现

负责建立可供 SSH使用的可靠双向字节流：

- WebSocket
- HTTP/2双向流
- 原生 QUIC

所有节点通过 build tag裁剪实现。

### 3.3 Connection Profile

负责某条连接边的线上协议契约：

- 本地唯一ID。
- transport类型
- HTTP请求匹配规则
- HTTP响应字段
- operation映射
- HTTP header和握手元数据限制
- H2、QUIC所需的 ALPN值

Profile不负责授权。HTTP字段匹配成功只表示请求可以进入 SSH握手，最终身份仍由
SSH key、certificate和 fingerprint确定。

Connection Profile ID只用于Node Profile本地引用、日志和测试标识，不在线上传输，
也不证明两端配置内容相同。第一版不计算配置hash。

## 4. 默认 Profile

所有 transport地位对等，不使用 `legacy`命名。

建议内置名称：

```text
builtin:websocket-default
builtin:http2-default
builtin:quic-default
```

未配置外部 Profile时：

- 包含 `transport_ws`的节点使用 `builtin:websocket-default`。
- 不包含任何数据transport的节点可以编译，但不能启动数据连接。
- 当前 WebSocket请求头、握手和行为保持不变。

加载外部Node Profile后，每个binding只开放其引用的Connection Profile指定的数据
transport，不隐式同时开放WebSocket。需要切换传输时应修改配置并重启。

## 5. Profile、Binding与运行角色

第一版的基本关系为：

```text
一个连接方向 -> 一个 Profile -> 一种 transport
一个 Listener -> 一个 Profile
```

不同进程角色的配置关系：

```text
Server/Gateway Listener -> inbound Profile
Agent outbound           -> upstream Profile
Beacon Listener          -> inbound Profile
Beacon outbound          -> upstream Profile
```

Beacon的 inbound和 upstream Profile彼此独立。第一版每个方向只配置一个
Profile；一个 Listener不同时匹配多个 Profile。

运行角色继续由现有CLI语义决定：

```text
slider client             -> Agent
slider client --listener  -> Client Listener
slider client --beacon    -> Beacon
slider server             -> Server
slider server --gateway   -> Gateway
```

`--listener`、`--beacon`和 `--gateway`决定业务能力，不属于传输配置，因此可以与
`--profile`同时使用，但第一版只为已纳入范围的角色启用binding：

```text
Agent           -> upstream
Beacon          -> inbound + upstream
Server/Gateway  -> inbound
```

`client --listener --profile`在第一版返回unsupported错误，因为Server主动连接
Client Listener尚未接入Connection Profile。`--gateway`只改变Server业务能力；
其Agent入站使用Profile，Gateway主动出站和Callback继续使用现有行为。

使用 `--profile`后，URL位置参数以及 address、port、retry、keepalive、HTTP外观、
TLS、CA、key和fingerprint等重叠连接参数禁止混用。日志显示和输出格式等正交参数
仍可使用。所有冲突和缺失binding必须在启动任何Listener或网络连接前报错。

控制面与数据面不拆分 hostname或主 HTTP Listener。为了避免 Profile规则吞掉
控制台请求，路由顺序固定为：

1. Web Console、认证、健康检查和版本接口等保留路由。
2. 当前 Listener绑定的 Profile matcher。
3. 普通模板、重定向或兜底响应。

Profile不能覆盖或重新定义保留路由。加载 Profile不应改变 Web Console页面、
认证流程和 `/console/ws` 的浏览器连接行为。

对于 QUIC，Server在同一端口号上额外监听 UDP；Web Console继续使用 TCP。

## 6. Node Profile文件模型

Node Profile是启动时动态解析的单个YAML文件，不嵌入二进制，也不热更新。它在
同一文件中声明命名的Connection Profile和本地binding。

概念结构：

```yaml
version: 1

connection_profiles:
  edge-h2-v1:
    transport: h2
    request:
      method: POST
      path:
        match: exact
        value: /configured-path
      headers:
        Content-Type: application/octet-stream
    response:
      status: 200
      headers:
        Content-Type: application/octet-stream
    session:
      operation: agent

bindings:
  upstream:
    endpoint: https://server.example.com
    profile: edge-h2-v1
    tls:
      verify: true
      server_name: server.example.com
      ca: ca.pem
    ssh:
      fingerprints:
        - SHA256:example
    timeouts:
      connect: 10s
      transport_handshake: 10s
      ssh_handshake: 10s
      close: 5s
    keepalive:
      interval: 60s
      timeout: 10s
```

Beacon可以同时声明 `inbound`和 `upstream`，并分别引用不同Profile。

Timeout和keepalive属于binding本地运行策略，不属于双方必须一致的线上协议：

- `connect`限制TCP或UDP拨号及其外层TLS建立。
- `transport_handshake`限制WebSocket、H2或QUIC传输握手。
- `ssh_handshake`通过临时deadline限制SSH握手，成功后清除deadline。
- `close`限制半关闭后的排空和资源回收。
- `keepalive.interval`沿用SSH层keepalive，默认60秒且最小5秒。
- `keepalive.timeout`限制单次keepalive应答，第一版需要新增实现。

不提供通用数据idle timeout，避免关闭正常闲置的Shell。当前代码已有WebSocket
握手10秒、SSH Client配置10秒和SSH keepalive interval；其余能力需要随新
transport实现。

### 6.1 第一版允许的字段

- schema版本和本地唯一ID。
- 单一 transport。
- 固定 HTTP method；未配置时默认为 `POST`。
- 必填的 URL path；第一版只允许 `exact`。
- 固定请求 header和预期值。
- 固定响应 status和 header。
- 固定 `agent` operation映射。
- header和握手元数据尺寸上限。
- H2、QUIC所需的 ALPN值。

Path必须以 `/`开头，`/a`与 `/a/`不同，query不参与匹配。第一版拒绝编码后的
斜杠、`.`和 `..`等歧义形式。字段结构预留未来 `glob`模式，其中 `*`只匹配单个
路径段，`**`可以跨路径段；第一版解析器必须拒绝非 `exact`模式。

HTTP header名称按ASCII大小写不敏感匹配，值去除首尾OWS后精确匹配。配置一个值时，
只要实际请求的任一独立值精确匹配即可，不拼接重复header。第一版禁止配置
`Authorization`、`Cookie`、`Host`、`Content-Length`和hop-by-hop header。
HTTP请求不匹配时统一返回404；第一版不能配置失败状态码或响应body。

只要外层连接没有提供经过验证的对端身份，upstream binding就必须配置至少一个
SSH fingerprint。该规则统一覆盖明文WebSocket、h2c以及任意transport的
`tls.verify: false`，并在网络连接开始前校验。

### 6.2 第一版不允许的字段

- 任意脚本或表达式执行。
- 动态代码插件。
- SSH、TLS私钥或长期凭据。
- 根据未认证输入提升为 Operator或 Callback。
- 认证失败后的 transport回退。
- 运行时 Listener创建、删除或重新绑定。
- 多 transport自动协商。
- 请求体的任意可编程转换。
- Tunnel Body总大小上限；SSH和SFTP是未预先限定长度的流。

解析器必须拒绝未知字段、重复字段、无效枚举和相互冲突的配置，避免拼写错误被
静默忽略。

## 7. Operation边界

第一版 Profile只允许：

```text
session.operation = agent
```

请求中的 path或 header不直接决定 Session权限。匹配某个 Profile后，Listener
将该连接映射为固定 Agent语义，再进入现有 SSH认证。

这样避免允许未认证请求自行声明：

```text
operator
callback
gateway
```

未来若扩展其他 operation，应为每种方向单独设计 Profile和认证前置条件。

## 8. WebSocket Profile

默认 WebSocket Profile封装当前行为：

```text
HTTP/1.1 Upgrade
-> WebSocket binary messages
-> wsConn adapter
-> SSH
```

现有 Client不要求外部配置文件。其默认行为在概念上等价于加载
`builtin:websocket-default`。

外部 WebSocket Profile可以配置第一版允许的固定请求和响应字段，但不得改变
SSH握手、身份验证和 Session角色规则。

## 9. HTTP/2 Profile

H2使用一条连接、一条 HTTP/2 stream和一个 SSH会话：

```text
TCP/TLS connection
└── HTTP/2 request stream
    ├── Request Body:  Client -> Server
    └── Response Body: Server -> Client
        └── SSH
```

第一版不复用同一 H2连接建立多个 SSH会话。

由于控制面和数据面共用 Listener，H2请求必须具有请求级匹配条件。Profile可以
配置 method、path和 headers，因此 Slider核心不需要硬编码固定 path；但标准
HTTP/2请求在线上仍然具有 `:path`。

处理顺序：

1. Client根据 Profile建立 TLS并协商 ALPN `h2`。
2. Client发起 Profile定义的流式请求。
3. Listener先排除 Web Console等保留路由。
4. Profile matcher验证 method、path和 headers。
5. Server立即发送并 flush成功响应头。
6. 请求体和响应体被组合为 `net.Conn`。
7. 进入 SSH握手和现有 Session流程。

第一版支持两种H2接入：

- TLS + ALPN `h2`。
- 直连h2c prior knowledge。

不支持通过 HTTP/1.1 `Upgrade: h2c`切换协议。h2c只承诺直连，不承诺经过CDN、
反向代理或企业代理，并且对应upstream binding必须配置至少一个SSH fingerprint。
内层SSH继续提供会话机密性和对端身份验证，但h2c外层的path和header仍为明文，
可能被观察或修改。

H2适配器必须支持半关闭。Client侧 `CloseWrite`结束request body；Server侧
结束response body。Context取消、完整关闭和超时必须解除两端阻塞并最终回收
HTTP handler。

## 10. QUIC Profile

原生 QUIC不使用 HTTP URL、method或 path：

```text
UDP endpoint
-> QUIC/TLS
-> ALPN
-> one bidirectional stream
-> fixed transport metadata
-> SSH
```

第一版约束：

- 一条 QUIC连接只承载一个 SSH会话。
- 只使用可靠双向 stream。
- 不使用 datagram。
- 不使用 0-RTT传输认证或 SSH握手数据。
- Profile匹配失败和认证失败均不得降级到其他 transport。
- 支持NAT rebinding和本机IP变化时保持同一QUIC连接，并尽量维持其上的SSH会话。

QUIC在统一 `net.Conn` 接入边界完成后单独评估和推进，不依赖 H2先实现。

QUIC始终使用TLS 1.3。`tls.verify: false`只表示跳过外层证书身份验证，不表示
明文传输；此时对应upstream binding必须配置至少一个SSH fingerprint。该约束与
h2c相同，并在任何网络连接开始前校验。

## 11. 构建矩阵

Client、Server和 Gateway均通过显式build tag选择数据transport：

```text
-tags transport_ws                 -> WebSocket
-tags transport_h2                 -> HTTP/2
-tags transport_quic               -> QUIC
-tags "transport_ws transport_h2"  -> WebSocket + HTTP/2
```

build tag表示二进制具备的能力，不决定某条连接实际使用哪种 transport。普通
Agent应尽量使用单 transport构建；需要桥接不同协议的 Beacon按其 inbound和
upstream Profile组合能力。运行时 Profile引用未编译的 transport时立即失败。

一个节点可以包含多种 transport，但一条连接仍由一个 Profile选择一种，
并且不进行隐式自动回退。

没有transport tag时源码仍可编译，但任何需要数据连接的模式必须在启动网络资源前
返回 `no data transport compiled`。CI和正式发布必须显式传入tag。Web Console的
`/console/ws`是独立控制面能力，不由数据transport tag控制，因此启用Web Console的
Server即使不包含 `transport_ws`数据接入，也仍可能保留WebSocket库。

体积暂不设置硬性上限，但必须记录：

- 裸二进制体积。
- `-s -w -trimpath` 后体积。
- 压缩包体积。
- 相比当前 WebSocket Client的绝对值和百分比变化。

## 12. 启动与校验

运行时只启动当前 Listener配置和 Profile需要的数据 transport：

- WebSocket/H2使用 TCP Listener。
- QUIC使用 UDP Listener。
- Web Console始终保留当前 TCP HTTP(S)行为。

Profile在启动时完成：

1. 读取。
2. 严格解析。
3. 语义校验。
4. 检查CLI冲突和当前角色要求的binding。
5. 检查binding引用的transport已编译。
6. 检查所有未验证外层对端身份的upstream binding均存在SSH fingerprint。
7. 编译matcher。
8. 绑定到Listener。

运行期间不重新读取文件。Profile变更需要重启进程。

## 13. 中间设施兼容

目标环境可能包括：

```text
Client -> Cloudflare -> Slider
Client -> Nginx -> Slider
Client -> Cloudflare -> Nginx -> Slider
Client -> 企业 HTTP代理 -> Slider
```

兼容性不是 H2独有问题：

| Transport | 中间设施必须提供的能力 | 主要风险 |
|---|---|---|
| WebSocket | 明确支持 Upgrade与长连接转发 | Header处理、空闲超时、连接重启 |
| HTTP/2 | 保留请求和响应的并发流式语义 | H2降级、body buffering、长请求超时 |
| QUIC | UDP/L4透传，或理解自定义 ALPN并重新发起连接 | 普通 HTTP/3 CDN不转发原生 QUIC |

不能假设边缘使用 HTTP/2就代表到 Origin仍为 HTTP/2，也不能假设请求体和响应体
会被同时流式转发。H2中间设施可能：

- 将 H2转换为 H1。
- 缓冲请求体或响应体。
- 限制长时间请求。
- 不允许普通双向 POST。
- 修改或删除 header。
- 在空闲时主动关闭连接。

因此每种明确支持的链路都必须通过真实环境测试。无法满足双向流式传输时，应选用
WebSocket Profile，而不是在认证或会话建立阶段自动降级。

H2第一版只保留设计，不承诺立即实现或发布。正式兼容基线要求端到端H2，并且
中间设施必须在请求结束前持续转发request body、允许Origin提前返回response
body、不缓冲双向数据且允许长连接。边缘和Origin两侧都协商H2仍不足以证明这些
条件成立。

若CDN或企业代理将Origin连接降为H1.1，应作为明确的已知gap，不纳入第一版兼容
承诺。H1.1 chunked请求和响应理论上可以并发传输，但Go运行时与中间设施可能需要
显式启用全双工、关闭buffering，并且不能保证所有代理保留该语义。第一版以直连H2
为基线；Nginx、Cloudflare和企业代理均在真实E2E验证通过后再声明对应配置受支持，
不能仅依据两侧协商出的HTTP版本判断。

原生 QUIC不会发生 H2到 H1.1这种转换，但存在对应的终止问题：CDN支持面向浏览器
的 HTTP/3，不等于支持任意 QUIC ALPN或把 UDP连接透传到 Origin。原生 QUIC通常
要求直连或 L4 UDP转发；如果改用 HTTP/3、WebTransport或 CONNECT-UDP，则会形成
另一种需要单独设计和验证的 transport。

## 14. 统一接入边界

各 transport最终归一化为：

```text
net.Conn
TransportKind
OperationAgent
RemoteAddr
ProfileID（仅本地诊断）
```

Server/Gateway公共入口负责：

1. 创建 Session。
2. 设置固定 Agent角色。
3. 登记 Session。
4. 启动 SSH server。
5. 等待连接结束。
6. 清理 Session和下游资源。

Profile ID不在线上传输，也不计算Profile hash。它只用于本地配置、日志和测试
标识；同名Profile内容不一致仍会表现为404或握手失败。

## 15. 验收要求

每种 Client构建必须使用真实 transport完成：

- 编译和单元测试。
- `go vet`。
- Race检测。
- 交互式 Shell。
- Vim终端操作。
- 1 GiB文件上传。
- 1 GiB文件下载。
- 真实公网连接。
- Gateway链路。
- 连接中断和资源清理。
- Client体积对比。

H2还必须分别验证计划支持的 Cloudflare、Nginx和企业代理链路。测试结果应明确
记录客户端协议、边缘协议、Origin协议、是否发生 buffering以及连接超时行为。

## 16. 第一版实施顺序

1. 提取统一 `net.Conn` Session接入函数，以现有WebSocket完成回归验证。
2. 定义Node Profile、Connection Profile schema和YAML严格解析。
3. 将现有WebSocket行为表达为 `builtin:websocket-default`。
4. 保持Web Console固定路由不变，并增加单Profile matcher。
5. 完成 H2双向流、协议降级和代理链路的设计与验证计划。
6. 根据验证结果决定 H2是否进入实现。
7. 单独设计并验证 QUIC的直连、UDP转发和代理边界。
8. 根据节点拓扑生成单 transport或组合 transport Client。

## 17. 后续议题

以下内容不属于第一版：

- 一个 Listener同时加载多个数据 Profile。
- Profile热更新与会话版本固定。
- Gateway主动出站 Profile。
- Listener和 Callback Profile。
- H2连接多 stream复用。
- 通用多 transport Client。
- 动态模板和其他复杂转换能力。
