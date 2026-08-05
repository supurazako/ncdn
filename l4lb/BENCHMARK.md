# L4LB benchmark

`benchmark.sh`はnetns模擬環境を作り直し、現在のL4LBのリソース使用量とIPv4/IPv6の基準性能をCSVで出力する。

devcontainer内で実行する。

```console
$ cd l4lb
$ DURATION=10 PARALLEL=4 ./benchmark.sh | tee /tmp/l4lb-baseline.csv
```

各IP familyで次の2種類を測る。

- `throughput`: iperfによる転送量重視のTCP stream
- `packet-rate`: npingによるpacket rate重視のTCP SYN。標準の送信目標は合計1,000,000 pps

主な列は、LBの`net0`が受信したpacket数から求めた`ingress_pps`、受信byte数から求めた`ingress_gbps`、iperfが報告する`tcp_throughput_gbps`である。`rx_dropped`は同interfaceのdrop差分である。`packet-rate`では`tcp_throughput_gbps`を出力しない。

`PACKET_RATE`でnpingの合計送信目標ppsを変更できる。生成側がボトルネックにならないよう、想定するL4LB性能より十分大きな値を指定する。

リソース使用量として、Go control plane processの現在・最大RSS、binary size、XDP programと関連BPF mapのmemlock、JIT後のprogram sizeを記録する。RSSにはkernel内のBPF program/mapは含まれないため、それぞれ別の列として扱う。

`host_cpu_busy_percent`は測定中のホスト全体のCPU使用率であり、iperf client、cache node、ほかのホスト上の処理も含む。L4LBだけのCPU使用率ではない。

この結果はvethとnetwork namespaceを使った同一ホスト内の比較用baselineであり、実NIC上の最大性能を表さない。実装変更の前後は、同じホスト、`DURATION`、`PARALLEL`、`PACKET_RATE`で比較する。実機性能はキャンプのハードウェア上で別途測定する。
