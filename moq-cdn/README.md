# MoQ direct CDN experiment

`moq-dev/moq`を使い、ブラウザがRelayからMoQを直接受信するPoP実験環境。

```text
FFmpeg -> SRT -> moq-cli publisher -> moq-relay -> Browser
                                      [Group cache]  [WebTransport + WebCodecs]
```

HLSへの変換は行わない。ブラウザは`@moq/watch`でMoQをSUBSCRIBEし、WebTransport上のQUICで受信した映像をWebCodecsでデコードしてcanvasへ描画する。

## 何を検証するか

- PublisherからRelayへライブ映像を配信できる
- ブラウザがRelayへ直接WebTransport接続できる
- HLS segmentの完成を待たず、MoQのライブTrackをSUBSCRIBEできる
- WebCodecsでH.264をデコードしてリアルタイム描画できる
- Relayに完了済みGroupを保持し、今後のDVR `FETCH`実装に利用できる

## 固定バージョン

- `moqdev/moq-relay:0.14.9`
- `moqdev/moq-cli:0.9.9`
- `@moq/watch:0.4.5`
- `jrottenberg/ffmpeg:7.1-alpine`

以前のcloudflare/moq-rs draft-14構成は`feature/moq-cdn-spike`ブランチに残している。

## 起動

```sh
cd moq-cdn
docker compose up -d --remove-orphans
```

ブラウザとサーバが同じホストなら次を開く。

```text
http://localhost:3002
```

Tailscale経由ならサーバのTailscale IPを使う。

```text
http://100.94.113.55:3002
```

GUIは表示中のホスト名をRelayにも利用し、`http://<host>:4443/`を`@moq/watch`へ渡す。Publisherと同じRelay rootで`demo.hang`をSUBSCRIBEする。開発環境ではライブラリが`/certificate.sha256`を取得して自己署名証明書をpinし、実際のメディアはWebTransport/QUICで受信する。

この実験ではWebSocket fallbackを無効にしている。QUICが確立しない場合にTCPで動作を継続せず、GUIとConsoleに接続エラーを出す。

別のRelayを指定する場合：

```text
http://localhost:3002/?relay=100.94.113.55
```

SSHの`-L`はTCPしか転送しないため、GUIのHTMLは見えてもWebTransportのQUIC/UDPは運べない。リモートRelayの確認にはTailscaleなどUDPが到達する経路を使う。

## キャッシュ設定

`moqdev/relay.toml`で設定する。

```toml
[cache]
capacity = "128MB"
duration = "30s"
```

直接プレイヤーは現在ライブSUBSCRIBEのみを利用する。キャッシュは次の段階で、過去GroupをFETCHして再生し、ライブ端でSUBSCRIBEへ切り替えるDVR機能に使う。

## 自動検証

```sh
./verify-observability.sh
```

自動検証はコンテナ、Relayのfingerprint endpoint、GUI、直接プレイヤー設定、PublisherからRelayへの配信を確認する。WebTransportとWebCodecsはブラウザAPIなので、最終確認はChromeのGUIで行う。

停止：

```sh
docker compose down
```
