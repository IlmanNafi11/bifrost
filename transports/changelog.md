## ✨ Added

- **Per-Key Proxy Pools** - Upstream API keys can bind to a pool of HTTP/SOCKS5 proxies with six selection mechanisms. Bound keys egress through the picked proxy; a pool that cannot serve fails closed with a terminal `proxy_pool_exhausted` 503 rather than leaking the provider or global proxy. Admin CRUD, import, probe, bind/unbind and a per-key read projection are available under `/api/proxy-pools`, alongside runtime selection/exhaustion/probe stats on the list response

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- `add_proxy_pool_tables` creates `proxy_pools`, `proxy_pool_proxies` and `proxy_pool_bindings`. The tables are additive and older binaries ignore them; the migration has no down-migration so pool configuration is preserved across a downgrade.
