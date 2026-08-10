# MoQ CDN experiment

HTTP PoPとは別に、Media over QUIC Transport（MOQT）のRelayをPoPとして動かす実験環境。Publisherを受けるOrigin Relayと、利用者へ配信するEdge Relayを分離する。

```text
                           +-> C0 Edge Relay -> Subscriber / Browser
Publisher -> Origin Relay -|
                           +-> C1 Edge Relay -> Subscriber / Browser

Browser -> Request Router --(Edge URL)--> C0 or C1
```

PublisherはOriginへ`/demo`や`/clock`をannounceする。C0/C1に購読が来ると、共有CoordinatorでNamespaceの所在を解決し、Originへの上流購読をオンデマンドで作成する。ブラウザ向けにC0はUDP 4443、C1はUDP 4444を公開し、OriginはDocker network内に閉じる。

Cloudflareの`moq-rs`をcommit `69302d3dc2422e93b8a1d62f853a6759aa9e5468`に固定して利用する。このcommitとブラウザPlayerはIETF MOQT draft-14で揃えている。MOQTは策定中で実装間のwire compatibilityがdraft versionに依存するため、Relay、Publisher、Subscriber、Playerを同じdraftへ固定する。

## 実行

DockerとDocker Compose、OpenSSLが必要。

```sh
cd moq-cdn
./generate-cert.sh
docker compose up --build
```

初回はRust workspace全体をbuildするため時間がかかる。成功すると、`subscriber`のlogにRelay経由で受信した時刻が継続的に表示される。

別terminalから確認する場合:

```sh
docker compose logs -f subscriber
```

## 観測

各RelayはPrometheus形式のmetricsを公開する。C0は`http://localhost:9091/metrics`、Originは`http://localhost:9092/metrics`、C1は`http://localhost:9093/metrics`で確認できる。

```sh
./verify-observability.sh
```

```text
c0.moq_relay_active_connections=1
c0.moq_relay_upstream_connections=1
c0.moq_relay_active_subscriptions=1
c0.moq_relay_active_tracks=1
c1.moq_relay_active_connections=1
c1.moq_relay_upstream_connections=1
c1.moq_relay_active_subscriptions=1
c1.moq_relay_active_tracks=1
origin.moq_relay_active_connections=3
origin.moq_relay_active_publishers=2
origin.moq_relay_active_subscriptions=1
origin.moq_relay_active_tracks=1
```

GUI dashboardは`http://localhost:3001/d/moq-relay-overview`で確認できる。loginは不要で、Relayの現在値、時間変化、購読解決時間を5秒間隔で表示する。

映像と配信経路を同時に見る専用Visualizerは`http://localhost:3002`で確認できる。VisualizerのHTMLはTCPで配信するが、埋め込まれたPlayerはTailscale経由で選択したEdgeへ直接WebTransport接続する。

Visualizer上部のselectorでは、自動振り分けと手動選択を切り替えられる。

```text
http://localhost:3002/?strategy=round-robin
http://localhost:3002/?strategy=rendezvous
http://localhost:3002/?edge=c0
http://localhost:3002/?edge=c1
```

`round-robin`はリクエストごとにC0/C1を交互に返す比較用baselineである。`rendezvous`はNamespaceとEdge IDから決定的に接続先を選ぶため、同じNamespaceは同じEdgeへ集まりやすい。これにより、Edge間で同じ上流購読を重複して作ることを抑えられる。デフォルトは`rendezvous`。

Request Router APIを直接確認する場合:

```sh
curl 'http://localhost:8080/route?namespace=/demo&strategy=round-robin'
curl 'http://localhost:8080/route?namespace=/demo&strategy=rendezvous'
curl 'http://localhost:8080/metrics'
```

Routerは映像データを中継しない。ブラウザへ接続先を返すcontrol planeであり、返答後のWebTransport通信はブラウザから選択されたEdgeへ直接流れる。

回線を使わず、アルゴリズムの性質だけを再現可能な条件で比較できる。

```sh
curl -s 'http://localhost:8080/compare?namespaces=1000&requests_per_namespace=10' | jq
```

`imbalance_percent`はEdge間の選択数の偏り、`average_edges_per_namespace`は同じNamespaceが平均何台へ散ったか、`remapped_after_add_percent`はEdgeを1台追加した際に割当が移動したNamespaceの割合を表す。

同じAPIの`load_experiment`は、`/experiment/0`だけをhot Namespaceとして、通常のRendezvousと負荷上限付きRendezvousを比較する。既定の`load_bound_percent=125`は、各Edgeの平均負荷の125%までprimary Edgeへの集約を許し、それを超えたrequestだけを次点のEdgeへ送るという意味である。

これはアルゴリズムのトレードオフを確認するsimulationであり、実際のRelay負荷を参照して本番のrouteを変更する機能ではない。

手元PCからSSH port forwardingで開く場合:

```sh
ssh -L 3002:localhost:3002 sprzk@debian
```

その後、`http://localhost:3002`を開く。WebTransportがsecure contextを要求するため、Tailscale IPの`http://100.94.113.55:3002`ではなく、開発時は`localhost`から開く。

Chrome/Chromium系ブラウザを使用する。映像が自動で始まらない場合は、プレイヤー中央の再生ボタンを押す。

TailscaleのMTUは1280のため、Chromeの既定QUIC packet sizeでは`ERR_MSG_TOO_BIG`になることがある。macOSでは検証専用profileを使い、QUIC packetを1200 bytesに制限して起動する。

```sh
open -na "Google Chrome" --args \
  --user-data-dir=/tmp/chrome-moq \
  --quic-max-packet-length=1200
```

通常のMTU 1500のLANでは、この起動optionは不要。証明書はWebTransportの`serverCertificateHashes`で検証できるよう、有効期間10日のP-256 ECDSA自己署名証明書を`generate-cert.sh`で生成する。

映像が表示されない場合は、ブラウザconsoleとRelay logを確認する。

```sh
docker compose logs --since=5m relay video-publisher
```

`ERR_MSG_TOO_BIG (-142)`は経路MTU、`InvalidMessage`や`unexpected end of stream`はMOQT draft不一致の可能性が高い。

生のmetricsは`http://localhost:9091/metrics`、Prometheusのquery UIは`http://localhost:9090`で確認できる。Prometheusでは、例えば次をqueryする。

```promql
moq_relay_active_connections
rate(moq_relay_subscribers_total[1m])
histogram_quantile(0.95, rate(moq_relay_subscribe_latency_seconds_bucket[5m]))
```

QUIC connection単位のqlogは`artifacts/qlog/`、MoQのmessage logは`artifacts/mlog/`へ保存する。通常のRelay logは「何が起きたか」、Prometheusは「現在値と時間変化」、qlog/mlogは「なぜその通信になったか」の調査に使う。

停止する場合:

```sh
docker compose down
```

RelayはhostのUDP 4443を公開する。自己署名証明書と`--tls-disable-verify`、Relayの`--dev`はlocal experiment専用であり、そのまま外部公開しない。

## 次の段階

1. Round RobinとRendezvousで、Edge負荷と上流購読の重複を比較する
2. Edge追加・削除時にNamespaceの割り当てがどれだけ移動するか計測する
3. Edge負荷に上限を設けたbounded-load Rendezvousを追加する
4. startup latency、受信Object数、dropを含めてアルゴリズムを比較する
