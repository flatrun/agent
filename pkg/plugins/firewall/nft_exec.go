package firewall

import (
	"bytes"
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

func (execNftRunner) ListRuleset() (string, error) {
	out, err := exec.Command("nft", "list", "ruleset").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
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
