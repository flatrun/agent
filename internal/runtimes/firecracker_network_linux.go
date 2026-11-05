package runtimes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func (i *firecrackerInstance) networkKey() string {
	sum := sha256.Sum256([]byte(i.runtime.Scope + i.id))
	return fmt.Sprintf("%x", sum[:5])
}
func (i *firecrackerInstance) tap() string   { return "fr" + i.networkKey() }
func (i *firecrackerInstance) chain() string { return "FR" + i.networkKey() }
func (i *firecrackerInstance) prefix() netip.Prefix {
	if i.subnet != "" {
		prefix, _ := netip.ParsePrefix(i.subnet)
		return prefix
	}
	sum := sha256.Sum256([]byte(i.id))
	return subnetFor(uint32(sum[0])<<16 | uint32(sum[1])<<8 | uint32(sum[2]))
}
func subnetFor(index uint32) netip.Prefix {
	value := (index & ((1 << 20) - 1)) << 2
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64 + byte(value>>16), byte(value >> 8), byte(value)}), 30)
}
func (i *firecrackerInstance) hostIP() string  { return i.prefix().Addr().Next().String() }
func (i *firecrackerInstance) guestIP() string { return i.prefix().Addr().Next().Next().String() }
func command(ctx context.Context, binary string, args ...string) error {
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, out)
	}
	return nil
}
func (i *firecrackerInstance) setupNetwork(ctx context.Context) error {
	if err := os.MkdirAll("/run/frfc", 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile("/run/frfc/network.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	routes, err := exec.CommandContext(ctx, "ip", "-j", "-4", "route", "show", "table", "all").Output()
	if err != nil {
		return err
	}
	var entries []struct {
		Dst string `json:"dst"`
	}
	if err = json.Unmarshal(routes, &entries); err != nil {
		return err
	}
	occupied := []netip.Prefix{}
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(entry.Dst)
		if err != nil {
			if address, err := netip.ParseAddr(entry.Dst); err == nil {
				prefix = netip.PrefixFrom(address, 32)
			}
		}
		if prefix.IsValid() && prefix.Bits() != 0 {
			occupied = append(occupied, prefix)
		}
	}
	sum := sha256.Sum256([]byte(i.id))
	start := uint32(sum[0])<<16 | uint32(sum[1])<<8 | uint32(sum[2])
	found := false
	for offset := uint32(0); offset < 1<<20; offset++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		candidate := subnetFor(start + offset)
		conflict := false
		for _, prefix := range occupied {
			if candidate.Overlaps(prefix) {
				conflict = true
				break
			}
		}
		if !conflict {
			i.subnet = candidate.String()
			found = true
			break
		}
	}
	if !found {
		return errors.New("no free Firecracker /30 subnet in 100.64.0.0/10")
	}
	if err = i.writeState(i.runtime.directory(i.id)); err != nil {
		return err
	}
	forwarding, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(forwarding)) != "1" {
		return errors.New("egress requires net.ipv4.ip_forward=1")
	}
	if err = command(ctx, "ip", "tuntap", "add", "dev", i.tap(), "mode", "tap", "user", "65534"); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join("/proc/sys/net/ipv6/conf", i.tap(), "disable_ipv6"), []byte("1"), 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = command(ctx, "ip", "addr", "add", i.hostIP()+"/30", "dev", i.tap()); err != nil {
		return err
	}
	if err = command(ctx, "iptables", "-w", "1", "-N", i.chain()); err != nil {
		return err
	}
	rules := [][]string{
		{"-A", i.chain(), "!", "-s", i.guestIP() + "/32", "-j", "DROP"},
	}
	for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/3"} {
		rules = append(rules, []string{"-A", i.chain(), "-d", cidr, "-j", "DROP"})
	}
	rules = append(rules, []string{"-A", i.chain(), "!", "-o", "fr+", "-j", "ACCEPT"}, []string{"-A", i.chain(), "-j", "DROP"})
	for _, rule := range i.ipv6Rules("-I") {
		if err = command(ctx, "ip6tables", append([]string{"-w", "1"}, rule...)...); err != nil {
			return err
		}
	}
	rules = append(rules, i.networkRules("-I")...)
	for _, rule := range rules {
		if err = command(ctx, "iptables", append([]string{"-w", "1"}, rule...)...); err != nil {
			return err
		}
	}
	return command(ctx, "ip", "link", "set", i.tap(), "up")
}
func (i *firecrackerInstance) networkRules(action string) [][]string {
	appendAction := action
	if action == "-I" {
		appendAction = "-A"
	}
	return [][]string{
		{action, "INPUT", "-i", i.tap(), "-j", "DROP"},
		{action, "FORWARD", "-i", i.tap(), "-j", i.chain()},
		{appendAction, "FORWARD", "-o", i.tap(), "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{appendAction, "FORWARD", "-o", i.tap(), "-j", "DROP"},
		{"-t", "nat", action, "POSTROUTING", "-s", i.guestIP() + "/32", "!", "-o", "fr+", "-j", "MASQUERADE"},
	}
}
func (i *firecrackerInstance) cleanupNetwork(ctx context.Context) error {
	var errs []error
	if _, err := os.Stat(filepathForTap(i.tap())); err == nil {
		errs = append(errs, command(ctx, "ip", "link", "delete", i.tap()))
	} else if !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, entry := range []struct {
		binary string
		rules  [][]string
	}{{"ip6tables", i.ipv6Rules("-D")}, {"iptables", i.networkRules("-D")}} {
		for _, rule := range entry.rules {
			check := append([]string{}, rule...)
			for n, value := range check {
				if value == "-D" {
					check[n] = "-C"
				}
			}
			exists, err := ruleExists(ctx, entry.binary, check)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if exists {
				errs = append(errs, command(ctx, entry.binary, append([]string{"-w", "1"}, rule...)...))
			}
		}
	}
	exists, err := ruleExists(ctx, "iptables", []string{"-S", i.chain()})
	errs = append(errs, err)
	if exists {
		if err = command(ctx, "iptables", "-w", "1", "-F", i.chain()); err != nil {
			errs = append(errs, err)
		} else {
			errs = append(errs, command(ctx, "iptables", "-w", "1", "-X", i.chain()))
		}
	}
	return errors.Join(errs...)
}
func ruleExists(ctx context.Context, binary string, args []string) (bool, error) {
	out, err := exec.CommandContext(ctx, binary, append([]string{"-w", "1"}, args...)...).CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("%s: %w: %s", binary, err, out)
}
func filepathForTap(tap string) string { return "/sys/class/net/" + tap }

func (i *firecrackerInstance) ipv6Rules(action string) [][]string {
	return [][]string{{action, "INPUT", "-i", i.tap(), "-j", "DROP"}, {action, "FORWARD", "-i", i.tap(), "-j", "DROP"}, {action, "FORWARD", "-o", i.tap(), "-j", "DROP"}}
}
