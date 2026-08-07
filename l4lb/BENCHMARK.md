# L4LB benchmark

`benchmark.sh`はnetns模擬環境を作り直し、現在のL4LBのリソース使用量とIPv4/IPv6の基準性能をCSVで出力する。

devcontainer内で実行する。

```console
$ cd l4lb
$ DURATION=10 PARALLEL=4 ./benchmark.sh | tee /tmp/l4lb-baseline.csv
```

各IP familyで次の2種類を測る。

- `throughput`: iperfによる転送量重視のTCP stream
- `packet-rate`: 1個のTCP SYN packetを生成し、tcpreplayで指定ppsに従って反復送信する測定。標準では100,000、250,000、500,000、1,000,000 ppsを順に測る

各条件は標準で3回測定し、`packet-rate`の本計測前には1秒間のwarm-upを行う。測定回数は`REPETITIONS`、warm-up時間は`WARMUP_DURATION`、空白区切りの送信目標は`PACKET_RATES`で変更できる。従来の単一値`PACKET_RATE`も利用できる。

`throughput`でも本計測前にTCP warm-upを行う。時間は`THROUGHPUT_WARMUP_DURATION`で変更できる。

主な列は次のとおり。

- `ingress_pps`: LBの`net0`が受信したpacket rate
- `forwarded_packets`: C0/C1の`tunnel` interfaceへ到達したpacket数の合計
- `forwarding_drop_percent`: `(ingress_packets - forwarded_packets) / ingress_packets`。負値は計測noiseとして0に丸める
- `target_achievement_percent`: 実際の`ingress_pps`が送信目標ppsの何%か
- `sustainable`: forwarding dropが0.1%未満、かつ実測ppsが送信目標の95%から105%に収まる場合に`yes`
- `tcp_throughput_gbps`: iperfが報告したTCP throughput

`sustainable`の閾値は`MAX_FORWARDING_DROP_PERCENT`、`MIN_TARGET_ACHIEVEMENT_PERCENT`、`MAX_TARGET_ACHIEVEMENT_PERCENT`で変更できる。目標を大幅に超える結果も送信generatorを正しく制御できていない測定として除外する。1回の結果だけで飽和点とせず、同じ条件の中央値を比較に使う。

`packet-rate`のppsはtcpreplayへ指定した送信時間で計算する。process起動・終了時間は含めないため、短い測定でも起動overheadをpacket rate低下と誤認しない。

benchmark中はcache serverの`/statusz`を起動しないため、L4LBのhealth checkを明示的に無効化する。通常の`run-lb.sh`ではhealth checkは有効なままである。

リソース使用量として、Go control plane processの現在・最大RSS、binary size、XDP programと関連BPF mapのmemlock、JIT後のprogram sizeを記録する。RSSにはkernel内のBPF program/mapは含まれないため、それぞれ別の列として扱う。

`host_cpu_busy_percent`は測定中のホスト全体のCPU使用率であり、iperf client、cache node、ほかのホスト上の処理も含む。L4LBだけのCPU使用率ではない。

この結果はvethとnetwork namespaceを使った同一ホスト内の比較用baselineであり、実NIC上の最大性能を表さない。実装変更の前後は、同じホスト、`DURATION`、`PARALLEL`、`PACKET_RATES`、`REPETITIONS`で比較する。実機性能はキャンプのハードウェア上で別途測定する。
