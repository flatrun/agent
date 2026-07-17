package firewall

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// tableName is the single nftables table FlatRun owns. Enforcement only ever
// creates, replaces, or deletes this table, never the whole ruleset, so rules
// managed by Docker or the operator in other tables are left untouched.
const tableName = "flatrun_firewall"

// sshSession is the SSH connection the agent is reachable through, if any. It is
// used to keep that session (and future SSH) alive under a default-deny inbound
// policy.
type sshSession struct {
	clientIP   string
	serverPort int
}

// detectSSHSession reads SSH_CONNECTION ("client_ip client_port server_ip
// server_port"), set by sshd for a session. It reports the SSH port so a
// default-deny policy never locks the operator out.
func detectSSHSession() sshSession {
	parts := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(parts) != 4 {
		return sshSession{}
	}
	port, err := strconv.Atoi(parts[3])
	if err != nil || port <= 0 || port > 65535 {
		return sshSession{}
	}
	return sshSession{clientIP: parts[0], serverPort: port}
}

// saddrClause returns the nftables source/destination match for a CIDR, picking
// the address family, or "" for any address.
func addrClause(field, cidr string) string {
	if cidr == "" {
		return ""
	}
	family := "ip"
	if strings.Contains(cidr, ":") {
		family = "ip6"
	}
	return fmt.Sprintf("%s %s %s ", family, field, cidr)
}

// ruleLines renders one config rule to zero or more nftables statements. A rule
// with no protocol (or "any") and a port expands to both tcp and udp.
func ruleLines(r Rule, addrField string) []string {
	verdict := "accept"
	if r.Action == PolicyDeny {
		verdict = "drop"
	}
	addr := addrClause(addrField, r.CIDR)

	protos := []string{}
	switch r.Protocol {
	case "tcp", "udp":
		protos = []string{r.Protocol}
	default: // "", "any"
		if r.Port != 0 {
			protos = []string{"tcp", "udp"}
		}
	}

	if r.Port == 0 || len(protos) == 0 {
		// Match on address (and protocol, if a specific one was given) alone.
		proto := ""
		if r.Protocol == "tcp" || r.Protocol == "udp" {
			proto = "meta l4proto " + r.Protocol + " "
		}
		return []string{fmt.Sprintf("        %s%s%s", addr, proto, verdict)}
	}

	var lines []string
	for _, p := range protos {
		lines = append(lines, fmt.Sprintf("        %s%s dport %d %s", addr, p, r.Port, verdict))
	}
	return lines
}

// renderNftables translates a firewall config to an `nft -f` script for the
// FlatRun table. The script is idempotent: it recreates the table from scratch
// on every apply. Loopback and established/related traffic are always accepted,
// and the active SSH port is always accepted, so switching to a default-deny
// inbound policy never drops the operator's connection.
func renderNftables(cfg *Config, ssh sshSession) string {
	inbound := cfg.DefaultInbound
	if inbound == "" {
		inbound = PolicyAllow
	}
	outbound := cfg.DefaultOutbound
	if outbound == "" {
		outbound = PolicyAllow
	}

	var b strings.Builder
	b.WriteString("# Managed by FlatRun. Do not edit.\n")
	// add-then-delete makes the following table definition a clean replace even
	// on the first apply, when the table does not exist yet.
	fmt.Fprintf(&b, "add table inet %s\n", tableName)
	fmt.Fprintf(&b, "delete table inet %s\n", tableName)
	fmt.Fprintf(&b, "table inet %s {\n", tableName)

	// Input chain.
	fmt.Fprintf(&b, "    chain input {\n")
	fmt.Fprintf(&b, "        type filter hook input priority 0; policy %s;\n", policyVerdict(inbound))
	b.WriteString("        iif \"lo\" accept\n")
	b.WriteString("        ct state established,related accept\n")
	b.WriteString("        ct state invalid drop\n")
	if inbound == PolicyDeny {
		// Preserve SSH under a default-deny so the operator is not locked out.
		port := 22
		if ssh.serverPort != 0 {
			port = ssh.serverPort
		}
		fmt.Fprintf(&b, "        tcp dport %d accept\n", port)
	}
	for _, r := range cfg.Rules {
		if r.Direction != DirInbound {
			continue
		}
		for _, line := range ruleLines(r, "saddr") {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("    }\n")

	// Output chain.
	fmt.Fprintf(&b, "    chain output {\n")
	fmt.Fprintf(&b, "        type filter hook output priority 0; policy %s;\n", policyVerdict(outbound))
	b.WriteString("        oif \"lo\" accept\n")
	b.WriteString("        ct state established,related accept\n")
	for _, r := range cfg.Rules {
		if r.Direction != DirOutbound {
			continue
		}
		for _, line := range ruleLines(r, "daddr") {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("    }\n")

	b.WriteString("}\n")
	return b.String()
}

func policyVerdict(policy string) string {
	if policy == PolicyDeny {
		return "drop"
	}
	return "accept"
}
