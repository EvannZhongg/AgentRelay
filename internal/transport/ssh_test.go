package transport

import (
	"context"
	"strings"
	"testing"

	"AgentRelay/internal/protocol"
)

func TestSSHRejectsUnsafeConfigurationBeforeLaunching(t *testing.T) {
	valid := protocol.SSHConfig{Host: "localhost", User: "builder", Port: 22}
	for _, host := range []string{"-oProxyCommand=bad", "host name", "user@host", "host\nother"} {
		c := valid
		c.Host = host
		if _, err := (SSH{Config: c}).Run(context.Background(), "echo test", nil); err == nil {
			t.Fatalf("accepted host: %q", host)
		}
	}
	args := (SSH{Config: valid}).args("native-bootstrap")
	joined := strings.Join(args, " ")
	for _, required := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "-F none", "native-bootstrap"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s", required)
		}
	}
}
func TestSSHOutputIsBounded(t *testing.T) {
	b := boundedBuffer{limit: 3}
	n, err := b.Write([]byte("abcde"))
	if n != 5 || err != nil || b.String() != "abc" || !b.overflow {
		t.Fatalf("bounded writer: %+v", b)
	}
}
