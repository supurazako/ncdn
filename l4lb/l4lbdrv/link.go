package l4lbdrv

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

var LinkCheckFrequency = time.Second

// XDPMode controls how the kernel attaches the XDP program to a link.
type XDPMode string

const (
	XDPModeAuto    XDPMode = "auto"
	XDPModeGeneric XDPMode = "generic"
	XDPModeDriver  XDPMode = "driver"
)

// `LinkAttacher` monitors the link and reattaches the XDP program if it is
// detached.  This is a workaround to some buggy Linux environments where XDP
// programs are unattached from the interface spontaneously.
type LinkAttacher struct {
	fd int

	attachedLink    netlink.Link
	linkCheckTicker *time.Ticker
	linkCheckDone   chan struct{}
}

func AttachToLink(link netlink.Link, fd int, mode XDPMode) (*LinkAttacher, error) {
	flags, err := xdpModeFlags(mode)
	if err != nil {
		return nil, err
	}
	if flags == 0 {
		err = netlink.LinkSetXdpFd(link, fd)
	} else {
		err = netlink.LinkSetXdpFdWithFlags(link, fd, flags)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to attach to link %s: %w", link.Attrs().Name, err)
	}
	slog.Info("Attached balancer XDP program", slog.Int("fd", fd), slog.String("interface", link.Attrs().Name))

	a := &LinkAttacher{
		attachedLink:    link,
		linkCheckTicker: time.NewTicker(LinkCheckFrequency),
		linkCheckDone:   make(chan struct{}),
	}

	go func() {
		for {
			select {
			case <-a.linkCheckDone:
				break
			case <-a.linkCheckTicker.C:
				err := netlink.LinkSetXdpFdWithFlags(a.attachedLink, fd, flags|int(unix.XDP_FLAGS_UPDATE_IF_NOEXIST))
				if err == nil {
					slog.Warn("Reattached xdp prog successfully", slog.Int("fd", fd), slog.String("interface", link.Attrs().Name))
				} else if !errors.Is(err, unix.EBUSY) {
					slog.Error("Failed to attach to link", slog.String("interface", link.Attrs().Name), slog.String("error", err.Error()))
				}
			}
		}
	}()

	return a, nil
}

func xdpModeFlags(mode XDPMode) (int, error) {
	switch mode {
	case "", XDPModeAuto:
		return 0, nil
	case XDPModeGeneric:
		return int(unix.XDP_FLAGS_SKB_MODE), nil
	case XDPModeDriver:
		return int(unix.XDP_FLAGS_DRV_MODE), nil
	default:
		return 0, fmt.Errorf("invalid XDP mode %q (want auto, generic, or driver)", mode)
	}
}

func (a *LinkAttacher) Close() error {
	if a.attachedLink == nil {
		return nil
	}

	close(a.linkCheckDone)

	if err := netlink.LinkSetXdpFd(a.attachedLink, -1); err != nil {
		return fmt.Errorf("Failed to detach xdp prog (should be fd: %d) from link %s: %w", a.fd, a.attachedLink.Attrs().Name, err)
	}
	a.attachedLink = nil
	return nil
}
