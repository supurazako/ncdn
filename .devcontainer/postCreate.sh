#!/bin/bash

sudo apt update
sudo apt install --no-install-recommends -y \
    libssl-dev sshpass dnsutils ethtool \
    supervisor iputils-ping tcpdump bind9-dnsutils \
    build-essential libbpf-dev clang llvm bpftool iperf jq nmap

go mod download
