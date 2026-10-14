package leases

import (
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func requireInvalid(t *testing.T, data string) {
	t.Helper()
	pairs, err := Parse([]byte(data), fixedNow())
	if pairs != nil {
		t.Fatalf("pairs %+v", pairs)
	}
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeInvalid {
		t.Fatalf("err=%v", err)
	}
	if err.Error() != CodeInvalid {
		t.Fatalf("error string %q", err.Error())
	}
}

func TestParseOneActiveLease(t *testing.T) {
	data := "1791903600 AA:BB:CC:DD:EE:FF 192.168.1.10 living-room-tv *\r\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0] != (Pair{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}) {
		t.Fatalf("%+v", pairs)
	}
}

func TestParseSeveralLeasesOrderIndependent(t *testing.T) {
	first := "" +
		"1791903600 11:22:33:44:55:66 10.0.0.2 beta *\n" +
		"1791903600 aa:bb:cc:dd:ee:ff 10.0.0.1 alpha *\n"
	second := "" +
		"1791903600 aa:bb:cc:dd:ee:ff 10.0.0.1 alpha *\n" +
		"1791903600 11:22:33:44:55:66 10.0.0.2 beta *\n"
	a, err := Parse([]byte(first), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(second), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	got := map[Pair]int{}
	for _, p := range a {
		got[p]++
	}
	for _, p := range b {
		got[p]--
	}
	for _, n := range got {
		if n != 0 {
			t.Fatalf("a=%+v b=%+v", a, b)
		}
	}
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestParsePermanentLease(t *testing.T) {
	pairs, err := Parse([]byte("0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n"), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseExpiredExcluded(t *testing.T) {
	now := fixedNow()
	data := "1699999999 aa:bb:cc:dd:ee:01 10.0.0.1 old *\n" +
		"1700000000 aa:bb:cc:dd:ee:02 10.0.0.2 boundary *\n" +
		"1700000001 aa:bb:cc:dd:ee:03 10.0.0.3 future *\n"
	pairs, err := Parse([]byte(data), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].MAC != "aa:bb:cc:dd:ee:03" || pairs[0].IP != "10.0.0.3" {
		t.Fatalf("%+v", pairs)
	}
}

func TestParseEmptyFile(t *testing.T) {
	pairs, err := Parse(nil, fixedNow())
	if err != nil || pairs == nil || len(pairs) != 0 {
		t.Fatalf("err=%v pairs=%#v", err, pairs)
	}
	pairs, err = Parse([]byte(""), fixedNow())
	if err != nil || pairs == nil || len(pairs) != 0 {
		t.Fatalf("err=%v pairs=%#v", err, pairs)
	}
}

func TestParseBlankLinesIgnored(t *testing.T) {
	data := "\n  \n\t\n0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseInvalidLines(t *testing.T) {
	cases := []string{
		"not-a-time aa:bb:cc:dd:ee:ff 192.168.1.10 host *",
		"-1 aa:bb:cc:dd:ee:ff 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee:ff:01 192.168.1.10 host *",
		"100 AA-BB-CC-DD-EE-FF 192.168.1.10 host *",
		"100 aabbccddeeff 192.168.1.10 host *",
		"100 aa:bb:cc:dd:gg 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.256 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.001.010 host *",
		"100 aa:bb:cc:dd:ee:ff ::1 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10/24 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10 host",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10 host * extra",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			requireInvalid(t, line+"\n")
		})
	}
}

func TestParseActiveMACIPConflicts(t *testing.T) {
	requireInvalid(t, ""+
		"0 aa:bb:cc:dd:ee:ff 192.168.1.10 a *\n"+
		"0 aa:bb:cc:dd:ee:ff 192.168.1.11 b *\n")
	requireInvalid(t, ""+
		"0 aa:bb:cc:dd:ee:01 192.168.1.10 a *\n"+
		"0 aa:bb:cc:dd:ee:02 192.168.1.10 b *\n")
}

func TestParseExpiredRowDoesNotConflict(t *testing.T) {
	data := "1 aa:bb:cc:dd:ee:ff 192.168.1.10 old *\n" +
		"0 aa:bb:cc:dd:ee:ff 192.168.1.11 current *\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 || pairs[0].IP != "192.168.1.11" {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseDuplicatePairDeduped(t *testing.T) {
	data := "" +
		"0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n" +
		"0 AA:BB:CC:DD:EE:FF 192.168.1.10 other *\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}
