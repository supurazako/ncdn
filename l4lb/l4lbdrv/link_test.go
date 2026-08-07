package l4lbdrv

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestXDPModeFlags(t *testing.T) {
	tests := []struct {
		name string
		mode XDPMode
		want int
	}{
		{name: "empty defaults to auto", mode: "", want: 0},
		{name: "auto", mode: XDPModeAuto, want: 0},
		{name: "generic", mode: XDPModeGeneric, want: int(unix.XDP_FLAGS_SKB_MODE)},
		{name: "driver", mode: XDPModeDriver, want: int(unix.XDP_FLAGS_DRV_MODE)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xdpModeFlags(tt.mode)
			if err != nil {
				t.Fatalf("xdpModeFlags(%q): %v", tt.mode, err)
			}
			if got != tt.want {
				t.Fatalf("xdpModeFlags(%q) = %d, want %d", tt.mode, got, tt.want)
			}
		})
	}
}

func TestXDPModeFlagsRejectsUnknownMode(t *testing.T) {
	if _, err := xdpModeFlags("invalid"); err == nil {
		t.Fatal("xdpModeFlags accepted an unknown mode")
	}
}
