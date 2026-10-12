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
		if _, err := (SSH{Config: c, Workspace: "/work"}).Call(context.Background(), Request{}); err == nil {
			t.Fatalf("accepted host: %q", host)
		}
	}
	for _, root := range []string{"work", "C:\\work", "/work\x00"} {
		if err := ValidateSSH(valid, root); err == nil {
			t.Fatalf("accepted workspace %q", root)
		}
	}
	if err := ValidateSSH(valid, "/work space/'quoted'"); err != nil {
		t.Fatal(err)
	}
	args := (SSH{Config: valid, Workspace: "/work"}).args()
	joined := strings.Join(args, " ")
	for _, required := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "-F none", "python3 -u"} {
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
