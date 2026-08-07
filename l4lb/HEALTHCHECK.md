# cache destination health check

L4LBのGo processは、設定された各cache destinationのIPv6 underlay addressに対して`/statusz`を定期的に確認する。health checkは利用者向けVIPを経由しない。

既定値は次の通り。

- interval: 1秒
- timeout: 300ミリ秒
- 3回連続失敗すると転送先から除外
- 2回連続成功すると転送先へ復帰
- port: 8889

値は`-healthCheckInterval`、`-healthCheckTimeout`、`-healthCheckFailures`、`-healthCheckSuccesses`、`-healthCheckPort`で変更できる。

Go processは正常なcache destinationだけをeBPF mapへ書き込む。XDP programは再読み込みせず、packetごとにmapから現在の転送先を選択する。全destinationが異常な場合、VIP宛の対応packetは`XDP_DROP`となり、`no_healthy_destination_total`を増加させる。

現在のL4LBはconnection stateを保持しない。destinationの除外・復帰によって既存TCP connectionの転送先が変わり、connectionが切断される可能性がある。また、剰余による選択のため、destination数の変化によって多数の割り当てが変わる。このissueでは、異常なdestinationへpacketを送り続けないことを優先する。

## 模擬環境での確認

`run-be.sh`と`run-lb.sh`を起動した状態で、C0を停止・復帰できる。

```console
supervisorctl -c ./supervisord.conf stop C0
supervisorctl -c ./supervisord.conf start C0
```

C0を停止して既定で約3秒待つと、L4LBのlogに`Cache destination became unhealthy`と正常destination数が出力される。この間、VIPへの新しいrequestはC1へ届く。C0を起動すると、2回のhealth check成功後に`Cache destination recovered`が出力され、C0が転送先へ戻る。
