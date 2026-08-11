# L4LB

このdirectoryにはL4LBのLinux amd64 binaryと、XDP program `lb-full.o`が配置される。

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
  -dests '<L4LB IPv6>;<L4LB MAC>,<L7LB IPv6>;<L7LB MAC>,'
```

`-dests`の先頭entryはL4LB自身で、2件目以降がL7LBである。同一L2 subnetでは各PCの実MAC addressを指定する。

実行前に、NIC名、IP address、MAC address、VIPのroute、MTUを実環境の値へ置き換える。XDP driver modeに対応しないNICでは`-xdpMode generic`を使用する。
