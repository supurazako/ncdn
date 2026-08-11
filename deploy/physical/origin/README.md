# Origin

このdirectoryにはOrigin serverのLinux amd64 binaryが配置される。

起動例：

```sh
./origin -nodeId O -listenAddr :8888
```

L7LB PCからTCP 8888へ到達できるIP addressとfirewallを設定する。
