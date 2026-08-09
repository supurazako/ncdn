# MoQ CDN experiment

HTTP PoPとは別に、Media over QUIC Transport（MOQT）のRelayをPoPとして動かす実験環境。
最初の到達点は、時計Objectを次の経路で配信することである。

```text
moq-clock publisher -> moq-relay-ietf -> moq-clock subscriber
```

Cloudflareの`moq-rs`をcommit `933a443d98b65bc536f0b6753e51a17b5eeaba15`に固定して利用する。このcommitはIETF MOQT draft-16を対象としている。MOQTは策定中で実装間の互換性がdraft versionに依存するため、まず同じ実装で全componentを揃える。

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

