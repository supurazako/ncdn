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

長時間運用では、配布物に含まれるsystemd unitと設定例を使用する。

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
