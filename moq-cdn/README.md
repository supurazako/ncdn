# MoQ multi-channel CDN

`moq-dev/moq`を使い、複数のライブchannelをOrigin RelayからEdge Relayへ配送し、ブラウザが選択したEdgeからMoQを直接受信する実験環境。

```text
FFmpeg -> moq-cli -> Origin Relay
                         │ Relay cluster
                 ┌───────┴───────┐
              Edge C0         Edge C1
                 └───────┬───────┘
                      L4LB VIP
                         ↑ WebTransport + QUIC
                      Browser
```

HTTP CDNではL7LB/Cacheとして動くC0/C1を、MoQではEdge Relayとして兼用できる。HTTP response cacheとMoQ Group cacheはデータ形式が異なるため別々に持つ。

## 実装内容

- `motion.hang`と`bars.hang`の2つのbroadcast
- channel catalog API (`GET /channels`)
- 共通VIPへ接続するclient設定API (`GET /config`)
- 振り分け比較用のRendezvous Router API (`GET /route`)
- C0/C1のRelay clusterと、track単位の上流購読集約
- GUIからchannelを切り替え、共通VIPへWebTransport接続
- 30秒・128MBのMoQ Group cache
- Edge Cacheの過去Groupを使う「10秒戻る」と「LIVEへ戻る」
- Raspberry Piと既存C0/C1へ分けて配置できるdeploy bundle

## 固定バージョン

- `moqdev/moq-relay:0.14.9`
- `moqdev/moq-cli:0.9.9`
- `@moq/watch:0.4.5`
- Raspberry Pi実機ではDebian bookwormのFFmpeg

## ローカル起動

```sh
cd moq-cdn
docker compose up -d --build --remove-orphans
```

ブラウザで次を開く。

```text
http://localhost:3002
```

別ホストから開く場合、Routerが返すEdge URLもそのホストから到達可能にする。

```sh
EDGE_C0_PUBLIC_URL=http://192.168.20.11:4443 \
EDGE_C1_PUBLIC_URL=http://192.168.20.9:4444 \
docker compose up -d --build --remove-orphans
```

GUIの`CHANNEL` selectorで映像を切り替える。ブラウザは常に共通VIPへ接続し、L4LBがQUIC flowをC0/C1へ固定する。

URLの`relay` queryを指定するとRouterを迂回して手動のRelayへ接続できる。

```text
http://localhost:3002/?relay=192.168.20.11
```

WebSocket fallbackは無効化している。QUICが確立しない場合はGUIとConsoleに接続エラーを出す。

## 配布物

```sh
make GOARCH=arm64 deploy-moq
```

次のdirectoryが生成される。

- `dist/linux-arm64/moq-publisher`: Raspberry Pi向けPublisher + Origin Relay + GUI
- `dist/linux-arm64/moq-edge`: C0/C1へ追加するEdge Relay用（Composeと共通証明書生成手順）

実機手順は各directoryの`README.md`を参照する。

## 検証

```sh
./verify-observability.sh
```

自動検証では、2つのbroadcast、Relay cluster、channel catalog、Router、GUIを確認する。WebTransportとWebCodecsの最終確認はChromeで行う。

停止：

```sh
docker compose down
```
