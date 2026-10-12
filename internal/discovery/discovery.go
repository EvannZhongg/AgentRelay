package discovery

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"time"
)

const Port = 8763
const query = "AgentRelay discover v1"

type Controller struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	Port        int    `json:"port"`
}

func Serve(ctx context.Context, name, fingerprint string, port int) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: Port})
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	data, _ := json.Marshal(Controller{Name: name, Fingerprint: fingerprint, Port: port})
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if string(buf[:n]) == query {
			_, _ = conn.WriteToUDP(data, addr)
		}
	}
}

func Discover(ctx context.Context) ([]Controller, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	targets := map[string]bool{"255.255.255.255": true, "127.0.0.1": true}
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || iface.Flags&net.FlagUp == 0 {
				continue
			}
			ip := ipnet.IP.To4()
			mask := ipnet.Mask
			if len(mask) != 4 {
				continue
			}
			broadcast := make(net.IP, 4)
			for i := range ip {
				broadcast[i] = ip[i] | ^mask[i]
			}
			targets[broadcast.String()] = true
		}
	}
	for target := range targets {
		_, _ = conn.WriteToUDP([]byte(query), &net.UDPAddr{IP: net.ParseIP(target), Port: Port})
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	found := map[string]Controller{}
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				break
			}
			return nil, err
		}
		var item Controller
		if json.Unmarshal(buf[:n], &item) != nil || item.Port < 1 || item.Port > 65535 || len(item.Fingerprint) != 64 {
			continue
		}
		item.URL = "https://" + net.JoinHostPort(addr.IP.String(), strconv.Itoa(item.Port))
		found[item.URL] = item
	}
	out := make([]Controller, 0, len(found))
	for _, item := range found {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, nil
}
