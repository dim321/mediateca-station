package commands

import (
	"errors"
	"time"

	"mediateca-station/internal/hub"
	"mediateca-station/internal/leases"
)

func FetchScreens(cmd hub.Command, read func() ([]leases.Pair, error), now time.Time) hub.CommandResult {
	finished := now.UTC()
	if !cmd.ScreenAbsent {
		msg := "invalid_command"
		return hub.CommandResult{CommandID: cmd.ID, OK: false, Error: &msg, FinishedAt: finished}
	}
	pairs, err := read()
	if err != nil {
		msg := publicLeaseCode(err)
		return hub.CommandResult{CommandID: cmd.ID, OK: false, Error: &msg, FinishedAt: finished}
	}
	screens := make([]hub.ScreenEndpoint, 0, len(pairs))
	for _, pair := range pairs {
		screens = append(screens, hub.ScreenEndpoint{MACAddress: pair.MAC, IPAddress: pair.IP})
	}
	return hub.CommandResult{
		CommandID:  cmd.ID,
		OK:         true,
		FinishedAt: finished,
		Result:     &hub.ScreensResult{Screens: screens},
	}
}

func publicLeaseCode(err error) string {
	var ce *leases.CodeError
	if errors.As(err, &ce) {
		switch ce.Code {
		case leases.CodeMissing, leases.CodeUnreadable, leases.CodeInvalid:
			return ce.Code
		}
	}
	return leases.CodeInvalid
}
