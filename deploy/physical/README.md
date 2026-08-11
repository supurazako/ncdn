# 物理PoPへの配置

リポジトリrootで次を実行すると、Linux amd64向けの配布物をrole別に生成する。

```sh
make deploy
```

生成先：

```text
dist/linux-amd64/
├── l4lb/
│   ├── l4lb
│   ├── lb-full.o
│   └── README.md
├── l7lb/
│   ├── l7lb
│   └── README.md
└── origin/
    ├── origin
	├── templates/
	├── static/
    └── README.md
```

各PCには担当roleのdirectoryだけをコピーする。IP address、route、IPv6 tunnel、VIPは各READMEを確認しながら手動で設定する。

個別に生成する場合：

```sh
make deploy-l4lb
make deploy-l7lb
make deploy-origin
```

L4LBの全variantは通常のdeployには含めない。性能比較に必要な場合だけ`make l4lb-variants`を実行する。

L4LBのXDP object生成には`clang`、`llc`、`llvm-strip`、libbpf headerが必要である。これらを含むdevcontainer内での実行を推奨する。
