# L7LB / popcache

このdirectoryにはL7LB兼cache serverのLinux amd64 binary `l7lb`が配置される。ソースコード上の実装名は`popcache`である。

L4LBから届くIPv4-in-IPv6とIPv6-in-IPv6を受け取るため、起動前に`ip -6 tunnel`でトンネルを作り、VIPをトンネルinterfaceへ設定する。

起動例：

```sh
./l7lb \
  -nodeId C0 \
  -listenAddr :8889 \
  -originURL 'http://[<Origin IPv6>]:8888/' \
  -runtimeStatsInterval 10s
```

標準では送信元、method、host、path、処理時間を含むaccess logを標準エラーへ全件出力する。query stringは認証情報を含む可能性があるため記録しない。定期logにはrequest数、処理中request、RSS、Go heap、goroutine、GC、CPU使用率、取得できる場合は最高温度、cache使用量が含まれる。systemdで起動する場合はjournaldから確認できる。

手動検証でfileにも残す場合は標準出力と標準エラーをまとめて`tee`へ渡す。

```sh
./l7lb <options> 2>&1 | tee -a l7lb.log
```

高負荷時にlog量を抑える場合は、例えば`-accessLogEvery 100`で100 requestごとに1件へsampleできる。RPS benchmarkではlog I/Oを測定対象から外すため`-accessLogEvery 0`を指定する。

複数台配置する場合は`-nodeId`と、そのPCに設定するIPv6 addressを変える。L4LBのhealth checkは`/statusz`を使用する。
