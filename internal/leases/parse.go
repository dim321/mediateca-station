package leases

import (
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	CodeMissing    = "lease_file_missing"
	CodeUnreadable = "lease_file_unreadable"
	CodeInvalid    = "invalid_lease_file"
)

type Pair struct {
	MAC string
	IP  string
}

type CodeError struct {
	Code string
}

func (e *CodeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func Parse(data []byte, now time.Time) ([]Pair, error) {
	deadline := now.UTC().Unix()
	seen := map[Pair]struct{}{}
	macIP := map[string]string{}
	ipMAC := map[string]string{}
	active := []Pair{}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return nil, invalid()
		}
		expires, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || expires < 0 {
			return nil, invalid()
		}
		mac, err := canonicalMAC(fields[1])
		if err != nil {
			return nil, invalid()
		}
		ip, err := canonicalIPv4(fields[2])
		if err != nil {
			return nil, invalid()
		}
		if expires != 0 && expires <= deadline {
			continue
		}
		pair := Pair{MAC: mac, IP: ip}
		if _, ok := seen[pair]; ok {
			continue
		}
		if prev, ok := macIP[mac]; ok && prev != ip {
			return nil, invalid()
		}
		if prev, ok := ipMAC[ip]; ok && prev != mac {
			return nil, invalid()
		}
		seen[pair] = struct{}{}
		macIP[mac] = ip
		ipMAC[ip] = mac
		active = append(active, pair)
	}
	return active, nil
}

func invalid() error {
	return &CodeError{Code: CodeInvalid}
}

func canonicalMAC(raw string) (string, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 6 {
		return "", invalid()
	}
	var b [6]byte
	for i, part := range parts {
		if len(part) != 2 {
			return "", invalid()
		}
		n, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return "", invalid()
		}
		b[i] = byte(n)
	}
	return sprintfMAC(b), nil
}

func sprintfMAC(b [6]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 17)
	for i := 0; i < 6; i++ {
		out[i*3] = hex[b[i]>>4]
		out[i*3+1] = hex[b[i]&0x0f]
		if i < 5 {
			out[i*3+2] = ':'
		}
	}
	return string(out)
}

func canonicalIPv4(raw string) (string, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.Is4() {
		return "", invalid()
	}
	return addr.String(), nil
}
