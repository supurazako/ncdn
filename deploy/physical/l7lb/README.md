# L7LB / popcache

このdirectoryにはL7LB兼cache serverのLinux amd64 binary `l7lb`が配置される。ソースコード上の実装名は`popcache`である。

L4LBから届くIPv4-in-IPv6とIPv6-in-IPv6を受け取るため、起動前に`ip -6 tunnel`でトンネルを作り、VIPをトンネルinterfaceへ設定する。

起動例：

```sh
./l7lb \
  -nodeId C0 \
  -listenAddr :8889 \
  -originURL 'http://[<Origin IPv6>]:8888/' \
  -runtimeStatsInterval 10s \
  -logFile ./logs/l7lb.log
```

標準では送信元、method、host、path、処理時間を含むaccess logを標準エラーへ全件出力する。query stringは認証情報を含む可能性があるため記録しない。定期logにはrequest数、処理中request、RSS、Go heap、goroutine、GC、CPU使用率、取得できる場合は最高温度、cache使用量が含まれる。

`-logFile`を指定すると、標準エラーへの出力を維持したまま同じ内容をfileにも追記する。directoryは自動作成され、OSやprocessの再起動後も以前のlogを確認できる。既定では1 fileあたり128 MiB、backup 3世代でrotationする。

```sh
ls -lh logs/l7lb.log*
tail -f logs/l7lb.log
```

systemd/journaldで管理する場合は、配布物に含まれるunitと設定例も使用できる。

```sh
sudo install -D -m 0755 l7lb /opt/ncdn/l7lb/l7lb
sudo install -D -m 0644 ncdn-l7lb.default.example /etc/default/ncdn-l7lb
sudo install -D -m 0644 ncdn-l7lb.service /etc/systemd/system/ncdn-l7lb.service
sudo install -D -m 0644 journald-persistent.conf.example /etc/systemd/journald.conf.d/ncdn-persistent.conf
sudo systemd-tmpfiles --create --prefix /var/log/journal
sudo systemctl restart systemd-journald
sudo systemctl daemon-reload
sudo systemctl enable --now ncdn-l7lb
```

`/etc/default/ncdn-l7lb`のnode IDやOrigin URLは起動前に実環境の値へ変更する。journaldは最大512 MiB、最長7日間の範囲で永続保存する。現在の起動と過去の起動は次のように確認できる。

```sh
sudo journalctl -u ncdn-l7lb -f
sudo journalctl --list-boots
sudo journalctl -u ncdn-l7lb -b -1
```

高負荷時にlog量を抑える場合は、例えば`-accessLogEvery 100`で100 requestごとに1件へsampleできる。RPS benchmarkではlog I/Oを測定対象から外すため`-accessLogEvery 0`を指定する。

複数台配置する場合は`-nodeId`と、そのPCに設定するIPv6 addressを変える。L4LBのhealth checkは`/statusz`を使用する。
