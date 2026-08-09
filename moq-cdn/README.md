# MoQ CDN experiment

HTTP PoPとは別に、Media over QUIC Transport（MOQT）のRelayをPoPとして動かす実験環境。
最初の到達点は、時計Objectを次の経路で配信することである。

```text
moq-clock publisher -> moq-relay-ietf -> moq-clock subscriber
```

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

RelayはPrometheus形式のmetricsを公開する。PublisherとSubscriberが接続した状態で、主要なgaugeを確認できる。

```sh
./verify-observability.sh
```

```text
moq_relay_active_connections=2
moq_relay_active_publishers=1
moq_relay_active_subscriptions=1
moq_relay_active_tracks=1
```

GUI dashboardは`http://localhost:3001/d/moq-relay-overview`で確認できる。loginは不要で、Relayの現在値、時間変化、購読解決時間を5秒間隔で表示する。

映像と配信経路を同時に見る専用Visualizerは`http://localhost:3002`で確認できる。VisualizerのHTMLはTCPで配信するが、埋め込まれたPlayerはTailscale経由で`100.94.113.55:4443/udp`のRelayへ直接WebTransport接続する。

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

1. 単一RelayでObject、Track、Groupとpublish/subscribeの流れを観察する
2. Relayを2台に増やし、PublisherからSubscriberまでの経路をPoP間接続にする
3. 接続数、受信Object数、遅延、dropを計測する
4. 混雑またはRelay停止時に、どのObjectを届けて何を捨てるか検討する
