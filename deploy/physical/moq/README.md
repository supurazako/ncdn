# MoQ CDNの実機デプロイ

Raspberry PiをPublisher・Origin Relay・GUIとして使い、既存HTTP CDNのC0/C1をMoQ Edge Relayとして兼用する。

```text
https://moq.supurazako.com -> 219.100.95.114 -> Raspberry Pi GUI

Raspberry Pi                                C0 / C1
FFmpeg -> moq-cli -> Origin Relay ==QUIC==> Edge Relay + 30秒Group Cache
                                                     ^
Browser -> 219.100.95.113:4443 -> L4LB -------------+
```

Piは64bit Raspberry Pi OS、各ノードはDocker EngineとCompose pluginを使用する。例ではPiのLAN IPを`192.168.20.12`とする。

## 1. 共通Edge証明書

管理PCで一度だけ生成する。WebTransportのfingerprint認証用なので有効期間は10日である。

```sh
cd moq-edge
./generate-edge-cert.sh
cat certs/relay.sha256
```

生成された`certs` directoryを秘密鍵ごとC0/C1へ安全にコピーする。Gitには追加しない。両ノードのfingerprintが一致することを確認する。

```sh
openssl x509 -in certs/relay.crt -outform DER \
  | openssl dgst -sha256 -hex
```

期限切れ前に同じ手順で再生成し、C0/C1の証明書とPiの`MOQ_EDGE_CERTIFICATE_SHA256`を同時に更新して各Composeを再起動する。WebTransportの要件に合わせ、生成scriptは有効期間を最大14日に制限している。

## 2. Raspberry Pi

arm64配布物を生成して`moq-publisher` directoryをPiへコピーする。

```sh
make GOARCH=arm64 deploy-moq
```

Piで`.env`を作る。fingerprintは手順1の`relay.sha256`を使用する。

```sh
cd moq-publisher
cat >.env <<'EOF'
MOQ_LAN_IP=192.168.20.12
MOQ_EDGE_CERTIFICATE_SHA256=<64桁のfingerprint>
GUI_DOMAIN=moq.supurazako.com
EOF
docker compose -f compose.publisher.yaml up -d --build
docker compose -f compose.publisher.yaml logs -f
```

Piでは次のサービスが起動する。

- Origin Relay: LANのTCP/UDP `4443`
- 2つのarm64 FFmpegダミー映像: `motion.hang`と`bars.hang`
- arm64 Go Router: チャンネルと接続設定API
- Caddy: TCP `80/443`でGUIを公開し、Let's Encrypt証明書を自動取得

FFmpegは640x360・20fps・2秒GOPで時刻入り映像を生成する。巻き戻しは2秒を1 Groupとして扱う。

## 3. C0/C1

C0の`.env`:

```sh
cd moq-edge
cat >.env <<'EOF'
MOQ_ORIGIN_URL=http://192.168.20.12:4443
MOQ_CLUSTER_ID=100
MOQ_EDGE_ID=edge-c0
EOF
docker compose -f compose.edge.yaml up -d
```

C1ではIDだけ変える。

```sh
cat >.env <<'EOF'
MOQ_ORIGIN_URL=http://192.168.20.12:4443
MOQ_CLUSTER_ID=101
MOQ_EDGE_ID=edge-c1
EOF
docker compose -f compose.edge.yaml up -d
```

Relayはhost networkでTCP/UDP `4443`を直接listenし、L4LBからトンネル解除されたパケットの送信元とVIPを維持する。既定では1 CPU・512MBに制限する。

## 4. DNSとCiscoルーター

Cloudflare DNSにProxyなしのAレコードを作る。

```text
moq.supurazako.com  A  219.100.95.114  DNS only
```

CiscoルーターからPiへの経路を追加する。

```text
ip route 219.100.95.114 255.255.255.255 192.168.20.12
```

Pi自身にもGUI用アドレスを設定する。

```sh
sudo ip address add 219.100.95.114/32 dev lo
```

割当`219.100.95.112/28`は既にBGP広告しているため、`.114/32`を追加広告しない。

この状態ではPiのhost serviceも`.114`宛でInternetから到達し得る。少なくともTCP `80/443`を許可してから、それ以外の`.114`宛INPUTをdropする（Dockerの公開portは別chainを通るため、適用後に必ず外部からCaddyへ到達できることも確認する）。

```sh
sudo iptables -I INPUT 1 -d 219.100.95.114 -p tcp \
  -m multiport --dports 80,443 -j ACCEPT
sudo iptables -I INPUT 2 -d 219.100.95.114 -j DROP
```

これにより外部公開対象はGUIのTCP `80/443`だけになり、Origin RelayはLAN IPにbindしたまま、SSHなどのhost serviceはGUI用IPでは受けない。上記ルールは再起動で消える環境があるため、現地では起動手順として再適用する。

## 5. 共通L4LB

既存の実機値に`-udpPort 4443`を加えて起動する。

```sh
sudo ./l4lb \
  -interface enp44s0 \
  -xdpMode generic \
  -lbBin ./lb-full.o \
  -xdpcapHookPath '' \
  -vip 219.100.95.113 \
  -vip6 2401:5e40:10ff:ff04::1 \
  -underlayMTU 1500 \
  -udpPort 4443 \
  -healthCheckEnabled=false \
  -dests 'fd00:4::7;e0:51:d8:1d:48:c3,fd00:4::8;84:a9:3e:1e:7f:b3,'
```

初期実装のUDP接続固定は送信元IP・送信元port・宛先portのhashを使う。NAT rebindingとQUIC Connection Migrationは対象外である。

## 6. 確認

```sh
# Pi
docker compose -f compose.publisher.yaml ps
docker compose -f compose.publisher.yaml exec router wget -qO- http://127.0.0.1:8080/healthz

# C0/C1
docker compose -f compose.edge.yaml ps
curl -fsS http://127.0.0.1:4443/certificate.sha256
```

外部Chromeで`https://moq.supurazako.com`を開き、次を確認する。

1. 2チャンネルのどちらも共通VIP経由で再生できる。
2. 12秒以上待って「10秒戻る」を押すと過去Groupから再生される。
3. 「LIVEへ戻る」で最新Groupへ再購読する。
4. C0/C1のどちらへ振り分けられてもTLS handshakeが成功する。

停止時は各directoryで対応するCompose fileに`down`を実行する。
