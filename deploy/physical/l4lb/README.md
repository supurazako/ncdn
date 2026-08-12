# L4LB

このdirectoryにはL4LBのLinux amd64 binaryと、XDP program `lb-full.o`が配置される。

`lb-full.o`のDWARF情報はL4LB driverがmap構造を読み取るために必要なので、XDP objectだけはstripしていない。

最初に読み取り専用の調査scriptを実行する。引数を省略するとdefault routeのNICを選ぶ。

```sh
./inspect.sh
./inspect.sh enp1s0
```

NIC、IP address、MAC address、MTU、driver、route、neighbor、XDP実行環境と、起動設定の雛形が表示される。このscriptはIP addressやrouteを変更せず、XDPもattachしない。

起動例：

```sh
sudo ./l4lb \
  -interface enp1s0 \
  -lbBin ./lb-full.o \
  -xdpMode auto \
  -vip 192.0.2.10 \
  -vip6 2001:db8:100::10 \
  -underlayMTU 1500 \
  -selectionAlgorithm rendezvous \
  -udpPort 4443 \
  -dests '<L4LB IPv6>;<L4LB MAC>,<L7LB IPv6>;<L7LB MAC>,'
```

`-dests`の先頭entryはL4LB自身で、2件目以降がL7LBである。同一L2 subnetでは各PCの実MAC addressを指定する。

`-selectionAlgorithm`ではTCP flowをL7LBへ割り振る方式を選択できる。同じflowは同じL7LBへ送られる。

- `modulo`: hash値をL7LB数で割る単純な方式（default）。台数変更時には多くのflowの行き先が変わる。
- `rendezvous`: backendの追加・削除時の移動を抑える。通常の運用候補。
- `maglev`: lookup tableによって均等性と台数変更時の安定性を両立する。

RendezvousとMaglevのtableは起動時またはdestination更新時にcontrol planeで生成する。XDPのpacket処理は、どちらも1回のeBPF map lookupで行う。

`-udpPort 4443`を指定すると、TCPに加えてVIP宛UDP/4443を同じdestination poolへ転送する。これはC0/C1上のMoQ Edge Relay用である。`0`（default）ではUDP転送を無効化し、それ以外のVIP宛UDPはdropする。初期実装ではUDP flowを送信元IP・送信元port・宛先portで固定する。

実行前に、NIC名、IP address、MAC address、VIPのroute、MTUを実環境の値へ置き換える。XDP driver modeに対応しないNICでは`-xdpMode generic`を使用する。
