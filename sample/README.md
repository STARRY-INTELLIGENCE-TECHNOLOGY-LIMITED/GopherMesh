# Sample Programs

这个目录提供了 5 个可单独执行的示例程序，用于验证 GopherMesh 作为 `local/edge/server mesh gateway + process orchestrator` 的典型接入方式：

- `l7http/hello`: 最小 HTTP JSON 服务，适合验证 L7 代理透传。
- `l7http/sum`: 带查询参数和请求头回显的 HTTP 服务，适合验证 L7 路由与应用逻辑。
- `l4tcp/echo`: 最小 TCP Echo 服务，适合验证 L4 字节流透传。
- `l4tcp/uppercase`: 基于文本行的 TCP 服务，适合验证 L4 长连接和请求响应语义。
- `stdio/echo`: 单次读取 `stdin` 并写回 `stdout` 的请求驱动样例，可同时用于验证 `stdio_mode: "stream"` 和 `stdio_mode: "http"`。

运行方式：

```bash
go run . -config sample/sample_config.json
```

随后可以分别访问：

- `http://127.0.0.1:18081/`
- `http://127.0.0.1:18082/sum?a=3&b=5`
- `http://127.0.0.1:17084/`
- `tcp://127.0.0.1:17081`
- `tcp://127.0.0.1:17082`
- `tcp://127.0.0.1:17083`

说明：

- `sample_config.json` 假定当前工作目录是仓库根目录，因此其中的 `go run ./sample/...` 路径是相对仓库根目录的。
- `sample_config.json` 采用新的 `routes + backends` 结构，每个对外端口都挂了多个后端，用于演示简单轮询负载均衡。
- HTTP 样例会在 JSON 响应里返回 `service` 字段；TCP/STDIO 样例会在响应内容中带上实例名，便于观察请求被分发到了哪个后端。
- `17083` 对应 `stdio_mode: "stream"`，适合用 `TcpClient` / `nc` 并主动发送 EOF，验证原始 TCP -> STDIO 桥接。
- `17084` 对应 `stdio_mode: "http"`，适合用浏览器或 `curl.exe -N http://127.0.0.1:17084/` 验证 HTTP-over-STDIO 的持续回写。
