# IPv6 PoP

この構成では利用者向けにIPv4/IPv6の両方を提供し、L4LBからPoP cacheへの転送アンダーレイをIPv6へ統一している。

## 通信経路

```text
U (client)
  -> R (router)
  -> IPv4/IPv6 VIP on LB
  -> XDP L4LB
  -> C0 or C1 (PoP cache)
  -> O (origin, IPv6)
```

L4LBからPoPへの転送には、IPv4トラフィックではIPv4-in-IPv6（IPIP6）、IPv6トラフィックではIPv6-in-IPv6（IP6IP6）を使う。どちらも外側ヘッダーはIPv6であり、レスポンスはPoPからRへ直接返る。

```text
IPv4 client packet:
[ outer IPv6: LB -> C0/C1 ][ inner IPv4: U -> IPv4 VIP ]

IPv6 client packet:
[ outer IPv6: LB -> C0/C1 ][ inner IPv6: U -> IPv6 VIP ]
```

## XDPのパケット処理方針

XDPは宛先と対応状況によって処理を分ける。

| パケット | XDP action |
|---|---|
| VIP以外 | `XDP_PASS`で通常のLinux処理へ渡す |
| VIP宛てTCP | IPv6でencapして`XDP_TX` |
| VIP宛てICMP error（対応するDSR TCP flowを引用） | 該当するPoP cacheへIPv6でencapして`XDP_TX` |
| VIP宛ての未対応プロトコル | `XDP_DROP` |
| VIP宛てIPv4 Options付き | `XDP_DROP` |
| VIP宛てIPv6拡張ヘッダー付き | `XDP_DROP` |
| 壊れたVIP宛てTCPパケット | `XDP_DROP` |

LBのloopbackにもIPv4/IPv6 VIPを設定する。通常のVIP宛てTCPはXDPで処理されるためLinuxまでは到達しない。このlocal routeは、設定不備などでVIP宛てパケットが誤って`XDP_PASS`された場合に、デフォルトルートでRへ戻り、RとLBの間をループすることを防ぐための安全策である。

L4LB自身がサイズ超過時に利用者へ返すFragmentation Needed / Packet Too Bigに加え、DSRの戻り通信に対して外部から返ってくるICMP errorをC0/C1へ配送する。echo requestなどのICMP error以外や、対応するTCP flowを引用していないpacketはDROPする。

| 用途 | IPv4 | IPv6 |
|---|---|---|
| VIP | `192.0.2.10` | `2001:db8:100::10` |
| LB | `192.168.88.20` | `2001:db8:0:1::20` |
| C0 | `192.168.88.10` | `2001:db8:0:1::10` |
| C1 | `192.168.88.11` | `2001:db8:0:1::11` |
| Origin | `192.168.88.30` | `2001:db8:0:1::30` |

## デュアルスタックでの確認

開発コンテナ内の三つのターミナルで順番に実行する。

```bash
cd /workspaces/ncdn/l4lb
sudo ./netns_setup.sh
```

```bash
cd /workspaces/ncdn/l4lb
./run-be.sh
```

```bash
cd /workspaces/ncdn/l4lb
./run-lb.sh
```

別のターミナルで確認する。

```bash
cd /workspaces/ncdn/l4lb
./verify-dualstack.sh
```

IPv4とIPv6の両方で、L4LBを通ったリクエストが最初は`MISS`、二回目は`HIT`になれば成功。

IPv4の検証はIPIP6、IPv6の検証はIP6IP6を通る。C0/C1側の`v6tun0`はLinuxの`ip6tnl mode any`を使い、両方の内側パケットをdecapする。

## MTUとICMP

PoP内部で測定したunderlay MTUを`UNDERLAY_MTU`として設定し、外側IPv6ヘッダーの40バイトを差し引いた値を内側IPパケットの上限とする。デフォルトは一般的なEthernetに合わせて1500である。

```text
inner IPv4/IPv6 packet  1460 bytes
outer IPv6 header       + 40 bytes
----------------------------------
underlay packet          1500 bytes（デフォルト設定）
```

`mtu-config.sh`が、設定されたunderlay MTUから次の値を計算する。

```text
inner MTU = underlay MTU - outer IPv6 header 40
IPv4 MSS  = inner MTU - IPv4 header 20 - TCP header 20
IPv6 MSS  = inner MTU - IPv6 header 40 - TCP header 20
```

計算結果は`netns_setup.sh`がトンネルMTUとMSSへ、`run-lb.sh`がL4LBのBPF mapへ渡す。これによりICMPが通知するMTUも同じ設定から決まる。

VIP宛てのTCPパケットがinner MTUを超えた場合、L4LBはencapせず、送信元へ次のICMPエラーを`XDP_TX`で返す。

| 利用者側 | 応答 | 通知するMTU |
|---|---|---:|
| IPv4 | ICMP Destination Unreachable / Fragmentation Needed（Type 3, Code 4） | 設定から計算したinner MTU |
| IPv6 | ICMPv6 Packet Too Big（Type 2, Code 0） | 設定から計算したinner MTU |

ICMPv4応答は元のIPv4ヘッダーと続く8バイトを引用する。ICMPv6応答は、応答全体のIPv6パケットを最小IPv6 MTUの1280バイトに収めながら、元パケットを1232バイト引用する。どちらもIPアドレスとEthernetアドレスを送信元向けに反転し、チェックサムをL4LBで計算する。

次のカウンターで動作を確認できる。

- `MtuExceededPacketTotal`
- `Icmpv4FragNeededTotal`
- `Icmpv6PacketTooBigTotal`

### DSRの戻り通信に対するICMP error

C0/C1から利用者へのresponseはL4LBを通らず、DSRで直接返る。このresponseに対してrouterや利用者から返されたICMP errorは宛先がVIPであるため、L4LBへ到着する。

ICMP errorが引用するTCP responseには、送信元VIP、宛先client address、宛先client portが含まれる。L4LBはclient addressとclient portから通常の往路と同じhash keyを再構成し、選択されたcache nodeへICMP error全体をIPv6でencapする。IPv4のICMP errorはIPv4-in-IPv6、IPv6のICMPv6 errorはIPv6-in-IPv6となる。cache nodeの`v6tun0`がdecapし、VIPを持つlocal network stackへ渡す。

対応するerror typeは次の通り。

| IP family | error type |
|---|---|
| IPv4 | Destination Unreachable、Time Exceeded、Parameter Problem |
| IPv6 | Destination Unreachable、Packet Too Big、Time Exceeded、Parameter Problem |

引用されたpacketは、送信元が対応するVIP、IP headerの直後がTCPであることを確認する。条件を満たさないerrorはDROPする。

次のカウンターで配送結果を確認できる。

- `Icmpv4ErrorForwardedTotal`
- `Icmpv6ErrorForwardedTotal`
- `InvalidIcmpErrorTotal`

現在のL4LBはconnection stateを保持せず、現在のdestination数に対してhashを再計算する。そのため、元のTCP packetを転送してからICMP errorを受信するまでにdestination集合が変化すると、元とは異なるcache nodeへ配送する可能性がある。

模擬環境では、利用者側に出るRのlink MTUをIPv6最小MTUの1280へ一時的に下げ、大きなobjectを取得してPMTUDを確認できる。

```bash
sudo ip -n R link set netU mtu 1280
sudo ip netns exec U curl --noproxy "*" -4 -fsS --max-time 12 -o /dev/null \
  "http://192.0.2.10:8889/tailwind.v2.2.19.min.css?pmtu-v4=$(date +%s%N)"
sudo ip netns exec U curl --noproxy "*" -g -6 -fsS --max-time 12 -o /dev/null \
  "http://[2001:db8:100::10]:8889/tailwind.v2.2.19.min.css?pmtu-v6=$(date +%s%N)"
sudo ip -n R link set netU mtu 1500
```

どちらも、最初の大きなTCP responseに対してRがMTU 1280のICMP errorを返す。errorが元のcache nodeへ届くとTCPが送信packetを小さくし、downloadが完了する。確認後は、IPv4/IPv6両方を同じinterfaceで継続利用するためMTUを1500へ戻す。

### TCP MSSによる予防

C0/C1から利用者へ向かうデフォルトルートには、IPファミリーごとに計算した`advmss`を設定する。デフォルトのunderlay MTU 1500では次の値になる。

| 利用者側 | 計算 | 通知するMSS |
|---|---|---:|
| IPv4 | inner MTU 1460 − IPv4 20 − TCP 20 | 1420 |
| IPv6 | inner MTU 1460 − IPv6 40 − TCP 20 | 1400 |

C0/C1がSYN-ACKでこのMSSを通知すると、利用者のTCPスタックはC0/C1へ送るTCPデータをその値以下に分割する。これにより通常のTCP通信ではencap後もunderlay MTU 1500以内に収まる。

MSSは「相手から自分へ送るTCPデータ」の上限を通知する値である。そのため、クライアントからPoPへ向かうパケットを小さくする設定は、L4LBではなくTCP接続を受けるC0/C1のSYN-ACKに入れる。ICMPはMSSが守られなかった場合などのフォールバックとして残す。

### 本番環境での設定

まずL4LBから各cache nodeまで、想定するunderlay MTUのIPv6パケットが通ることを確認する。underlay MTU 1500を確認する例では、IPv6ヘッダー40バイトとICMPv6ヘッダー8バイトを差し引いた1452バイトをping payloadに指定する。

```bash
sudo ip netns exec LB ping -6 -M do -s 1452 -c 3 2001:db8:0:1::10
sudo ip netns exec LB ping -6 -M do -s 1452 -c 3 2001:db8:0:1::11
```

異なる値を一時的に使う場合は、topology作成、L4LB起動、検証へ同じ`UNDERLAY_MTU`を渡す。

```bash
sudo env UNDERLAY_MTU=1492 ./netns_setup.sh
UNDERLAY_MTU=1492 ./run-lb.sh
UNDERLAY_MTU=1492 ./verify-dualstack.sh
```

この場合はinner MTU 1452、IPv4 MSS 1412、IPv6 MSS 1392になる。通常利用するデフォルト値を変更する場合は`mtu-config.sh`の`UNDERLAY_MTU`を変更すれば、各スクリプトへ共通して反映される。

C0/C1の`v6tun0`は`encaplimit none`とし、MTUを計算したinner MTUへ明示設定する。現在のXDPが作る外側ヘッダーにはIPv6 Encapsulation Limit optionがないため、Linuxがそのoption用に8バイトを予約してMTUを1452へ下げる挙動を無効にしている。

## IPv6-only検証

通常の構成とは別に、各namespaceの外部インターフェースへIPv4アドレスを設定しない検証モードがある。

```bash
sudo ./netns_setup.sh --ipv6-only
./run-be.sh
./run-lb.sh
./verify-ipv6-only.sh
```

この検証により、通信が暗黙にIPv4へフォールバックしていないことを確認できる。通常のデュアルスタック構成へ戻すには、引数なしで`sudo ./netns_setup.sh`を再実行する。

このモードはIPv6クライアントのIP6IP6経路を検証する。IPv4-in-IPv6経路は通常のデュアルスタック検証で確認する。

## 現在の範囲

- L4LBが扱うトランスポートプロトコルはTCPのみ
- IPv6拡張ヘッダーは未対応で、IPv6ヘッダーの直後にTCPがあるパケットを扱う
- VIP宛てのUDP、対応するDSR TCP flowを引用しないICMP/ICMPv6、IPv4 Options、IPv6拡張ヘッダー付きパケットはDROPする
- IPv6 encapによる40バイトの増加に対し、内側MTU 1460、TCP MSS通知、ICMPによるPath MTU Discoveryを実装している
- DSRの戻り方向は利用者と同じIPファミリーを使うため、IPv4利用者への戻り経路までIPv6-onlyになるわけではない
- DNSのAAAA応答はまだこの段階に含めていない
