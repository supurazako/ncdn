# popcache

popcacheは、取得したHTTP 200 responseを期限付きのmemory cacheへ保存する。

cache容量は次のflagで指定する。

- `-cacheMaxBytes`: cache全体の上限。既定値は64 MiB
- `-cacheMaxObjectBytes`: 1 objectの上限。既定値は8 MiB
- `-cacheTTL`: objectの有効期間。既定値は30秒

容量にはcache key、HTTP header、response bodyのbyte数を含む。Goのmap、LRU list、allocationなどの管理用overheadは含まないため、`used_bytes`はprocessのRSSと一致しない。

容量が不足すると、最後に参照されてから最も時間が経ったobjectを削除する。object上限を超えるresponseはcacheへ保存せず、Originから利用者へstreamする。

現在の使用量と設定値は`/statusz`の`cache`で確認できる。
