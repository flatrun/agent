package firewall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// execNftRunner runs the real nft binary. It is used in production; tests inject
// a fake runner instead so they never touch the host firewall.
type execNftRunner struct{}

func newExecNftRunner() *execNftRunner { return &execNftRunner{} }

func (execNftRunner) Available() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

func (execNftRunner) ListTable() (string, error) {
	out, err := exec.Command("nft", "--json", "list", "tables").Output()
	if err != nil {
		return "", err
	}
	var state struct {
		Nftables []struct {
			Table *struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err = json.Unmarshal(out, &state); err != nil {
		return "", err
	}
	for _, item := range state.Nftables {
		if item.Table != nil && item.Table.Family == "inet" && item.Table.Name == tableName {
			data, e := exec.Command("nft", "list", "table", "inet", tableName).Output()
			return string(data), e
		}
	}
	return "", nil
}

func (execNftRunner) ApplyScript(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}
