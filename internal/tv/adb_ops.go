package tv

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	recordRemotePath = "/sdcard/.mediateca_screenrecord.mp4"
	mainActivity     = "com.voxel.mediastream/.MainActivity"
)

type Runner interface {
	Run(ctx context.Context, argv ...string) error
	RunOut(ctx context.Context, argv ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ExecRunner) RunOut(ctx context.Context, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		if len(out) > 0 {
			return nil, fmt.Errorf("adb: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil, fmt.Errorf("adb: %w", err)
	}
	return out, nil
}

func DefaultWOL(mac string) error {
	if err := exec.Command("wakeonlan", mac).Run(); err == nil {
		return nil
	}
	return exec.Command("etherwake", mac).Run()
}

type Device struct {
	Serial string
	MAC    string
	Runner Runner
	WOL    func(mac string) error
}

func (d *Device) runner() Runner {
	if d.Runner != nil {
		return d.Runner
	}
	return ExecRunner{}
}

func (d *Device) Connect(ctx context.Context) error {
	return d.runner().Run(ctx, "adb", "connect", d.Serial)
}

func (d *Device) wolFn() func(string) error {
	if d.WOL != nil {
		return d.WOL
	}
	return DefaultWOL
}

func (d *Device) Wake(ctx context.Context) error {
	if err := d.Connect(ctx); err != nil {
		if d.MAC == "" {
			return err
		}
		if wolErr := d.wolFn()(d.MAC); wolErr != nil {
			return err
		}
		if err := d.Connect(ctx); err != nil {
			return err
		}
	}
	if err := d.runner().Run(ctx, "adb", "-s", d.Serial, "shell", "input", "keyevent", "KEYCODE_WAKEUP"); err != nil {
		return err
	}
	if d.MAC == "" {
		return nil
	}
	asleep, err := d.asleep(ctx)
	if err != nil || !asleep {
		return err
	}
	return d.wolFn()(d.MAC)
}

func (d *Device) asleep(ctx context.Context) (bool, error) {
	out, err := d.runner().RunOut(ctx, "adb", "-s", d.Serial, "shell", "dumpsys", "power")
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "mWakefulness=Asleep"), nil
}

func (d *Device) Sleep(ctx context.Context) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	return d.runner().Run(ctx, "adb", "-s", d.Serial, "shell", "input", "keyevent", "KEYCODE_SLEEP")
}

func (d *Device) Screenshot(ctx context.Context) ([]byte, error) {
	if err := d.Connect(ctx); err != nil {
		return nil, err
	}
	return d.runner().RunOut(ctx, "adb", "-s", d.Serial, "exec-out", "screencap", "-p")
}

func (d *Device) Record(ctx context.Context, seconds int, dest string) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	limit := strconv.Itoa(seconds)
	if err := d.runner().Run(ctx, "adb", "-s", d.Serial, "shell", "screenrecord", "--time-limit", limit, recordRemotePath); err != nil {
		return err
	}
	if err := d.runner().Run(ctx, "adb", "-s", d.Serial, "pull", recordRemotePath, dest); err != nil {
		return err
	}
	_ = d.runner().Run(ctx, "adb", "-s", d.Serial, "shell", "rm", recordRemotePath)
	return nil
}

func (d *Device) InstallAPK(ctx context.Context, apk string) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return d.runner().Run(ctx, "adb", "-s", d.Serial, "install", "-r", apk)
}

func (d *Device) LaunchMain(ctx context.Context) error {
	return d.runner().Run(ctx, "adb", "-s", d.Serial, "shell", "am", "start", "-n", mainActivity)
}

func (d *Device) EnsureApp(ctx context.Context, agent *Agent, apk string) error {
	if _, err := agent.Status(ctx); err == nil {
		return nil
	}
	if err := d.InstallAPK(ctx, apk); err != nil {
		return err
	}
	return d.LaunchMain(ctx)
}
