package voxel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type Process interface {
	Pid() int
	Wait() error
	Kill() error
	Running() bool
}

type Runner interface {
	Start(ctx context.Context, argv ...string) (Process, error)
}

type ExecRunner struct{}

func (ExecRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{cmd: cmd}, nil
}

type osProcess struct{ cmd *exec.Cmd }

func (p *osProcess) Pid() int      { return p.cmd.Process.Pid }
func (p *osProcess) Wait() error   { return p.cmd.Wait() }
func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *osProcess) Running() bool {
	if p.cmd.ProcessState != nil && p.cmd.ProcessState.Exited() {
		return false
	}
	return p.cmd.Process != nil
}

type Manager struct {
	Bin     string
	FifoDir string
	DataDir string
	Runner  Runner

	mu    sync.Mutex
	procs map[int]Process // port → process
}

func (m *Manager) FifoPath(port int) string {
	return filepath.Join(m.FifoDir, fmt.Sprintf("stream_%d.ts", port))
}

func (m *Manager) Ensure(ctx context.Context, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs == nil {
		m.procs = map[int]Process{}
	}
	if p, ok := m.procs[port]; ok && p.Running() {
		return nil
	}
	fifo := m.FifoPath(port)
	if err := ensureFifo(fifo); err != nil {
		return err
	}
	if m.Runner == nil {
		m.Runner = ExecRunner{}
	}
	proc, err := m.Runner.Start(ctx, m.Bin, fifo, fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	m.procs[port] = proc
	_ = os.MkdirAll(filepath.Join(m.DataDir, "voxel"), 0o700)
	pidPath := filepath.Join(m.DataDir, "voxel", fmt.Sprintf("broadcast_%d.pid", port))
	return os.WriteFile(pidPath, []byte(strconv.Itoa(proc.Pid())+"\n"), 0o600)
}

func (m *Manager) Stop(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[port]
	if !ok {
		return nil
	}
	_ = p.Kill()
	delete(m.procs, port)
	return nil
}

func (m *Manager) OpenFifo(port int) (*os.File, error) {
	return os.OpenFile(m.FifoPath(port), os.O_WRONLY, 0)
}

func ensureFifo(path string) error {
	if st, err := os.Stat(path); err == nil {
		if st.Mode()&os.ModeNamedPipe != 0 {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syscall.Mkfifo(path, 0o600)
}
