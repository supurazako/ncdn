# L7LB / popcache

このdirectoryにはL7LB兼cache serverのLinux amd64 binary `l7lb`が配置される。ソースコード上の実装名は`popcache`である。

L4LBから届くIPv4-in-IPv6とIPv6-in-IPv6を受け取るため、起動前に`ip -6 tunnel`でトンネルを作り、VIPをトンネルinterfaceへ設定する。

起動例：

```sh
./l7lb \
  -nodeId C0 \
  -listenAddr :8889 \
  -originURL 'http://[<Origin IPv6>]:8888/'
```

複数台配置する場合は`-nodeId`と、そのPCに設定するIPv6 addressを変える。L4LBのhealth checkは`/statusz`を使用する。
