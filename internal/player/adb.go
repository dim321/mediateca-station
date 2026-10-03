package player

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type Runner interface {
	Run(ctx context.Context, argv ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb: %w", err)
	}
	_ = out
	return nil
}

func New() *Player {
	return &Player{Runner: ExecRunner{}}
}

type Player struct {
	Runner Runner
	mapMu  sync.Mutex
	mu     map[string]*sync.Mutex
}

func (p *Player) Start(ctx context.Context, serial, mediaURL, mime string) error {
	unlock := p.lock(serial)
	defer unlock()
	if err := p.run(ctx, "adb", "connect", serial); err != nil {
		return err
	}
	return p.run(ctx, "adb", "-s", serial, "shell", "am", "start",
		"-a", "android.intent.action.VIEW",
		"-d", mediaURL,
		"-t", mime,
		"-p", "org.videolan.vlc")
}

func (p *Player) Stop(ctx context.Context, serial string) error {
	unlock := p.lock(serial)
	defer unlock()
	return p.run(ctx, "adb", "-s", serial, "shell", "am", "force-stop", "org.videolan.vlc")
}

func (p *Player) run(ctx context.Context, argv ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.Runner.Run(ctx, argv...)
}

func (p *Player) lock(serial string) func() {
	p.mapMu.Lock()
	if p.mu == nil {
		p.mu = map[string]*sync.Mutex{}
	}
	m, ok := p.mu[serial]
	if !ok {
		m = &sync.Mutex{}
		p.mu[serial] = m
	}
	p.mapMu.Unlock()
	m.Lock()
	return m.Unlock
}
