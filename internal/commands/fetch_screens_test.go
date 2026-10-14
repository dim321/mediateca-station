package commands

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mediateca-station/internal/hub"
	"mediateca-station/internal/leases"
)

func fetchNow() time.Time {
	return time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC)
}

func TestFetchScreensSuccessJSON(t *testing.T) {
	res := FetchScreens(hub.Command{
		ID: "cmd_7b03539f-6ea8-4cf0-a719-1831b833f639", Type: "fetch_screens", ScreenAbsent: true,
	}, func() ([]leases.Pair, error) {
		return []leases.Pair{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}}, nil
	}, fetchNow())
	if !res.OK || res.Error != nil || res.Result == nil {
		t.Fatalf("%+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"command_id":"cmd_7b03539f-6ea8-4cf0-a719-1831b833f639","ok":true,"error":null,"finished_at":"2026-10-13T14:00:03Z","result":{"screens":[{"mac_address":"aa:bb:cc:dd:ee:ff","ip_address":"192.168.1.10"}]}}`
	if string(raw) != want {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensEmptyScreens(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd_empty", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return []leases.Pair{}, nil
	}, fetchNow())
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"screens":[]`) || strings.Contains(string(raw), "hostname") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensErrorOmitsPartialList(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd_bad", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return []leases.Pair{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}}, &leases.CodeError{Code: leases.CodeInvalid}
	}, fetchNow())
	if res.OK || res.Result != nil || res.Error == nil || *res.Error != leases.CodeInvalid {
		t.Fatalf("%+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "screens") || strings.Contains(string(raw), "aa:bb") || strings.Contains(string(raw), "result") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensFileCodes(t *testing.T) {
	codes := []string{leases.CodeMissing, leases.CodeUnreadable, leases.CodeInvalid}
	for _, code := range codes {
		res := FetchScreens(hub.Command{ID: "cmd", ScreenAbsent: true}, func() ([]leases.Pair, error) {
			return nil, &leases.CodeError{Code: code}
		}, fetchNow())
		if res.Error == nil || *res.Error != code || res.Result != nil {
			t.Fatalf("%s %+v", code, res)
		}
	}
}

func TestFetchScreensUnknownReadErrorHidesText(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return nil, errors.New("open /var/lib/misc/dnsmasq.leases: permission denied token=super-secret-token")
	}, fetchNow())
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || *res.Error != leases.CodeInvalid {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(string(raw), "/var/lib") || strings.Contains(string(raw), "super-secret-token") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensNonNullScreenDoesNotRead(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd", ScreenID: 7, ScreenAbsent: false, Type: "fetch_screens"}, func() ([]leases.Pair, error) {
		t.Fatal("read called")
		return nil, nil
	}, fetchNow())
	if res.OK || res.Result != nil || res.Error == nil || *res.Error != "invalid_command" {
		t.Fatalf("%+v", res)
	}
}
