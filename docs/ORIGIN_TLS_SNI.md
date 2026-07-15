# 回源 TLS SNI

当源站只能通过固定 IP 和端口访问，但证书签发给 DNS 域名时，可以分别配置连接地址、HTTP Host 头和 TLS SNI。

例如，客户端访问 `https://lax.dustvm.de`，边缘节点需要直连 `203.0.113.20:443`，源站证书的 SAN 包含 `lax.dustvm.de`：

```text
主源站 URL:      https://203.0.113.20:443
主源站 Host 头:  lax.dustvm.de
主源站 TLS SNI:  lax.dustvm.de
```

生成的 Nginx 行为为：

```nginx
upstream origin_site {
    server 203.0.113.20:443;
}

proxy_set_header Host lax.dustvm.de;
proxy_ssl_server_name on;
proxy_ssl_name lax.dustvm.de;
proxy_ssl_verify on;
proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
```

TCP 连接始终发往 URL 中的 IP 和端口。TLS 握手使用 `tls_server_name` 选择证书并校验证书名称，HTTP 请求使用 `host_header` 选择源站虚拟主机。这三个值互相独立。

TLS SNI 支持 HTTPS、WSS 和 GRPCS 的主源与备用源。留空时继续使用回源 URL 的主机名。HTTP、WS 或 GRPC 非 TLS 源不能配置 SNI。SNI 必须是实际 DNS 主机名，不能包含协议、端口、通配符或 IP。

源站必须提供包含该 DNS 名称的完整证书链，并由边缘节点系统 CA 信任。当前不支持自定义 CA、Cloudflare Origin CA 或关闭回源证书校验。

从任一边缘节点发布前验证：

```bash
curl --resolve lax.dustvm.de:443:203.0.113.20 https://lax.dustvm.de/

openssl s_client \
  -connect 203.0.113.20:443 \
  -servername lax.dustvm.de \
  -verify_hostname lax.dustvm.de </dev/null
```

保存 SNI 会增加站点配置版本并标记为待发布。完成源站连通性验证后，在站点页面执行发布即可，不需要升级边缘代理或全量运行 `publish-all`。
