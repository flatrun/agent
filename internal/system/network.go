package system

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type NetworkHealth struct {
	ExternalAccess bool              `json:"external_access"`
	DNS            DNSHealth         `json:"dns"`
	Interfaces     []NetworkInterface `json:"interfaces"`
	CheckedAt      time.Time         `json:"checked_at"`
}

type DNSHealth struct {
	Healthy  bool             `json:"healthy"`
	Resolvers []ResolverCheck `json:"resolvers"`
}

type ResolverCheck struct {
	Server  string `json:"server"`
	Healthy bool   `json:"healthy"`
	Latency int64  `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

type NetworkInterface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Flags     string   `json:"flags"`
}

type ServerInfo struct {
	Hostname   string             `json:"hostname"`
	PublicIPv4 string             `json:"public_ipv4"`
	PublicIPv6 string             `json:"public_ipv6"`
	Interfaces []NetworkInterface `json:"interfaces"`
}

var defaultDNSServers = []string{
	"8.8.8.8",
	"1.1.1.1",
	"9.9.9.9",
}

var dnsTestDomains = []string{
	"www.google.com",
	"www.cloudflare.com",
}

func GetServerInfo() (*ServerInfo, error) {
	info := &ServerInfo{}

	if hostname, err := getHostname(); err == nil {
		info.Hostname = hostname
	}

	info.Interfaces = getNetworkInterfaces()

	if ip, err := GetPublicIP("4"); err == nil {
		info.PublicIPv4 = ip
	}
	if ip, err := GetPublicIP("6"); err == nil {
		info.PublicIPv6 = ip
	}

	return info, nil
}

func CheckNetworkHealth(ctx context.Context) (*NetworkHealth, error) {
	health := &NetworkHealth{
		CheckedAt: time.Now(),
	}

	health.DNS = checkDNSHealth(ctx)
	health.ExternalAccess = checkExternalAccess(ctx)
	health.Interfaces = getNetworkInterfaces()

	return health, nil
}

func checkDNSHealth(ctx context.Context) DNSHealth {
	health := DNSHealth{Healthy: true}

	for _, server := range defaultDNSServers {
		check := checkResolver(ctx, server)
		health.Resolvers = append(health.Resolvers, check)
		if !check.Healthy {
			health.Healthy = false
		}
	}

	return health
}

func checkResolver(ctx context.Context, server string) ResolverCheck {
	check := ResolverCheck{Server: server}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", server+":53")
		},
	}

	resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := resolver.LookupHost(resolveCtx, dnsTestDomains[0])
	check.Latency = time.Since(start).Milliseconds()

	if err != nil {
		check.Healthy = false
		check.Error = err.Error()
	} else {
		check.Healthy = true
	}

	return check
}

func checkExternalAccess(ctx context.Context) bool {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", "1.1.1.1:443")
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func getNetworkInterfaces() []NetworkInterface {
	var result []NetworkInterface

	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		ni := NetworkInterface{
			Name:  iface.Name,
			Flags: iface.Flags.String(),
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ni.Addresses = append(ni.Addresses, addr.String())
		}

		if len(ni.Addresses) > 0 {
			result = append(result, ni)
		}
	}

	return result
}

func getHostname() (string, error) {
	return os.Hostname()
}

func GetPublicIP(version string) (string, error) {
	var endpoints []string
	if version == "6" {
		endpoints = []string{
			"https://api64.ipify.org",
			"https://ipv6.icanhazip.com",
		}
	} else {
		endpoints = []string{
			"https://api.ipify.org",
			"https://ipv4.icanhazip.com",
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, endpoint := range endpoints {
		resp, err := client.Get(endpoint)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}
		ip := strings.TrimSpace(string(body))
		if ip != "" && net.ParseIP(ip) != nil {
			return ip, nil
		}
	}

	return "", fmt.Errorf("failed to determine public IPv%s address", version)
}
