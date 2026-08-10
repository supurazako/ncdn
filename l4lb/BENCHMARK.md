# L4LB benchmark

`benchmark.sh`はnetns模擬環境を作り直し、現在のL4LBのリソース使用量とIPv4/IPv6の基準性能をCSVで出力する。

devcontainer内で実行する。

```console
$ cd l4lb
$ DURATION=10 PARALLEL=4 ./benchmark.sh | tee /tmp/l4lb-baseline.csv
```

XDPのattach方式は`XDP_MODE`で指定できる。既定値は`auto`で、環境に合わせてカーネルが選択する。
利用可能な方式を固定したい場合は、例えば`XDP_MODE=generic DURATION=10 ./benchmark.sh`のように実行する。
`driver`は対応ドライバがない環境では起動に失敗する。

各IP familyで次の2種類を測る。

- `throughput`: iperfによる転送量重視のTCP stream
- `packet-rate`: 1個のTCP SYN packetを生成し、tcpreplayで指定ppsに従って反復送信する測定。標準では100,000、250,000、500,000、1,000,000 ppsを順に測る

各条件は標準で3回測定し、`packet-rate`の本計測前には1秒間のwarm-upを行う。測定回数は`REPETITIONS`、warm-up時間は`WARMUP_DURATION`、空白区切りの送信目標は`PACKET_RATES`で変更できる。従来の単一値`PACKET_RATE`も利用できる。

`throughput`でも本計測前にTCP warm-upを行う。時間は`THROUGHPUT_WARMUP_DURATION`で変更できる。

throughputだけを測る場合は`PACKET_RATES=`を指定してpacket-rate測定をスキップできる。

主な列は次のとおり。

- `xdp_mode`、`interface_type`、`rx_queues`、`tx_queues`: 測定時のXDP attach modeとinterface情報。実機とveth模擬環境の結果を区別するために記録する
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

## L4LB variant比較

同じsource treeから次の6種類をbuildできる。

| variant | hot pathの違い | 失うもの・制約 |
|---|---|---|
| `full` | 通常実装 | なし。比較のbaseline |
| `no-stats` | 統計map lookupとpacketごとのcounter更新を省略 | packet数やdrop理由の観測性 |
| `inline-dest` | 設定と転送先2台を1つのmap valueにまとめ、destination map lookupを省略 | cache nodeは最大2台。bounds checkと配列計算が増える |
| `pow2-dests` | backend数が2の冪なら剰余をbit maskへ置換 | 分岐が増える。非2冪では通常の剰余へfallback |
| `keep-padding` | 小packetの末尾paddingをtrimするhelper呼び出しを省略 | wire byteが増え、末尾paddingを許容するNIC・受信側を前提にする |
| `minimal` | TCP VIP判定、backend選択、IPv6 encapだけを残す | 統計、ICMPによるPMTUD応答、DSR ICMP error配送。性能上限の比較専用 |

全versionは`make variants`で同時にbuildされ、1台のserverへ配置できる。起動時に`-variant`で使用するXDP objectを選ぶため、source treeやbinaryを交換する必要はない。`run-lb.sh`では環境変数から指定する。

```console
$ cd l4lb
$ L4LB_VARIANT=pow2-dests ./run-lb.sh
```

切替にはL4LB processの再起動が必要だが、network topologyとcache nodeはそのまま利用できる。

開発コンテナ内で各variantのbuildと対応する正常性testをまとめて実行する。

```console
$ cd l4lb
$ ./verify-variants.sh
```

実機で全variantを同じ条件で順番に測定する場合:

```console
$ cd l4lb
$ DURATION=30 REPETITIONS=5 PACKET_RATES="500000 1000000 2000000" \
    OUTPUT_DIR=/tmp/l4lb-variants ./benchmark-variants.sh
```

結果は`OUTPUT_DIR`内のvariant別CSVへ分けて保存する。各CSVの`variant`列にもbuild種別を記録する。全variantで送信機、NIC、queue数、XDP attach mode、packet size、試行回数を揃える。

`minimal`が最速でも、そのまま採用候補にはしない。`full`との差は、安全性と観測性を含む追加機能が性能へ与える総コストとして扱う。それ以外は個別のコストを切り分ける候補であり、実機で差が再現しない変更は採用しない。
