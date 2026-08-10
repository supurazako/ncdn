# MoQ CDN DVR experiment

`moq-dev/moq`のRelay履歴キャッシュとFETCHを使い、ライブ映像を巻き戻せるPoPを試す環境。

```text
FFmpeg -> SRT -> moq-cli publisher -> moq-relay -> moq-cli HLS exporter -> Browser
                                      [Group cache]       [Timeline + FETCH]
```

## 何を検証するか

- ライブ映像を`SUBSCRIBE`相当の経路でRelayへ届ける
- Relayが完了済みGroupを一定期間メモリに保持する
- Timeline Trackから時刻とGroup IDの対応を得る
- 過去segmentの要求をMoQのGroup `FETCH`へ変換する
- ブラウザで一時停止、巻き戻し、ライブ端への復帰を行う

Cloudflare DVR for Liveの外部仕様を参考に、タイムライン、pause/resume、ライブ端表示、`LIVE`への復帰を最初のUX目標にする。

## 固定バージョン

- `moqdev/moq-relay:0.14.9`
- `moqdev/moq-cli:0.9.9`
- `jrottenberg/ffmpeg:7.1-alpine`
- `hls.js:1.6.13`

RelayとCLIは現在`moq-lite-05`をネゴシエーションする。以前のcloudflare/moq-rs draft-14構成は`feature/moq-cdn-spike`ブランチに残している。

## 起動

```sh
cd moq-cdn
docker compose up -d
```

ブラウザで次を開く。

```text
http://localhost:3002
```

シークバーを左へ動かすと過去GroupがFETCHされる。`LIVE位置へ戻る`を押すとplaylistの最新位置へ移動する。

## キャッシュ設定

`moqdev/relay.toml`で設定する。

```toml
[cache]
capacity = "128MB"
duration = "30s"
```

- `capacity`: 全Trackで共有するGroup payloadの目標容量
- `duration`: 最新以外のGroupを最後の書き込みまたはFETCHから保持する上限
- 最新Group: ライブ端なので常に保持される

HLS exporterのplaylist windowは20秒にしている。Relayの30秒保持より短いため、playlistに掲載したsegmentが取得前にexpireしにくい。

## 自動検証

```sh
./verify-observability.sh
```

検証は次を確認する。

1. Relay、Publisher、HLS exporterが起動している
2. master playlistに映像renditionがある
3. media playlistに複数の過去segmentがある
4. 最古の掲載segmentを取得できる
5. RelayログにそのGroupのFETCHが記録される

手動で確認する場合：

```sh
curl http://localhost:8089/demo.hang/master.m3u8
curl http://localhost:8089/demo.hang/video/0.avc3/media.m3u8
docker compose logs relay | grep 'fetch started'
```

## 現在の範囲

現在は1台のRelayをEdgeとして動かす最小構成。HLSはブラウザでDVR挙動を確認するためのegress adapterであり、履歴の正本はHLS serverではなくMoQ RelayのGroup cacheにある。

次の段階ではOrigin RelayとEdge Relayを分離し、Edge cache miss時の上流FETCH、FETCH集約、Edgeごとのヒット率を扱う。

停止：

```sh
docker compose down
```
