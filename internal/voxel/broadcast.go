package voxel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"mediateca-station/internal/stream"
)

const fifoOpenTimeout = 5 * time.Second

type Process interface {
	Pid() int
	Wait() error
	Kill() error
	Running() bool
}

type Runner interface {
	Start(ctx context.Context, argv ...string) (Process, error)
}

type ExecRunner struct {
	LogFile string
}

func (r ExecRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if r.LogFile != "" {
		f, err := os.OpenFile(r.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		cmd.Stdout = f
		cmd.Stderr = f
		// File stays open for the lifetime of the process; closed on Wait via SetFinalizer-free path:
		// process exit does not close our fd automatically when cmd releases — close after Wait.
		p := &osProcess{cmd: cmd, done: make(chan struct{}), log: f}
		if err := cmd.Start(); err != nil {
			_ = f.Close()
			return nil, err
		}
		go func() {
			_ = cmd.Wait()
			_ = f.Close()
			close(p.done)
		}()
		return p, nil
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &osProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type osProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	log  *os.File
}

func (p *osProcess) Pid() int    { return p.cmd.Process.Pid }
func (p *osProcess) Wait() error { return p.cmd.Wait() }
func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *osProcess) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return p.cmd.Process != nil
	}
}

type Manager struct {
	Bin     string
	FifoDir string
	DataDir string
	Bind    string // broadcast_bind; required for Builtin
	Builtin bool   // in-process streamer instead of exec BroadcastBin
	Runner  Runner

	mu        sync.Mutex
	procs     map[int]Process      // port → process (legacy voxel_bin)
	streamers map[int]*stream.Port // port → builtin streamer
}

func (m *Manager) FifoPath(port int) string {
	return filepath.Join(m.FifoDir, fmt.Sprintf("stream_%d.ts", port))
}

func (m *Manager) Ensure(ctx context.Context, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Builtin {
		return m.ensureBuiltin(ctx, port)
	}
	return m.ensureExec(ctx, port)
}

func (m *Manager) ensureBuiltin(ctx context.Context, port int) error {
	if m.streamers == nil {
		m.streamers = map[int]*stream.Port{}
	}
	if p, ok := m.streamers[port]; ok && p.Running() {
		return nil
	}
	if old, ok := m.streamers[port]; ok {
		_ = old.Stop()
		delete(m.streamers, port)
	}
	if m.Bind == "" {
		return fmt.Errorf("broadcast_bind is required for builtin streamer")
	}
	fifo := m.FifoPath(port)
	p, err := stream.Start(ctx, m.Bind, port, fifo)
	if err != nil {
		return err
	}
	m.streamers[port] = p
	return nil
}

func (m *Manager) ensureExec(ctx context.Context, port int) error {
	if m.procs == nil {
		m.procs = map[int]Process{}
	}
	if p, ok := m.procs[port]; ok && p.Running() {
		return nil
	}
	delete(m.procs, port)
	fifo := m.FifoPath(port)
	if err := ensureFifo(fifo); err != nil {
		return err
	}
	voxelDir := filepath.Join(m.DataDir, "voxel")
	if err := os.MkdirAll(voxelDir, 0o700); err != nil {
		return err
	}
	runner := m.Runner
	if runner == nil {
		runner = ExecRunner{LogFile: filepath.Join(voxelDir, fmt.Sprintf("broadcast_%d.log", port))}
	}
	proc, err := runner.Start(ctx, m.Bin, fifo, fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("start %s: %w", m.Bin, err)
	}
	// Brief settle: catch immediate exec/permission/bind failures.
	time.Sleep(50 * time.Millisecond)
	if !proc.Running() {
		return fmt.Errorf("broadcast exited immediately (bin=%s fifo=%s port=%d; see %s)", m.Bin, fifo, port, filepath.Join(voxelDir, fmt.Sprintf("broadcast_%d.log", port)))
	}
	m.procs[port] = proc
	pidPath := filepath.Join(voxelDir, fmt.Sprintf("broadcast_%d.pid", port))
	return os.WriteFile(pidPath, []byte(strconv.Itoa(proc.Pid())+"\n"), 0o600)
}

func (m *Manager) Stop(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Builtin {
		if p, ok := m.streamers[port]; ok {
			_ = p.Stop()
			delete(m.streamers, port)
		}
		return nil
	}
	p, ok := m.procs[port]
	if !ok {
		return nil
	}
	_ = p.Kill()
	delete(m.procs, port)
	return nil
}

func (m *Manager) OpenFifo(port int) (*os.File, error) {
	path := m.FifoPath(port)
	f, err := openFifoWriter(path, fifoOpenTimeout)
	if err == nil {
		return f, nil
	}
	m.mu.Lock()
	builtin := m.Builtin
	var running bool
	bin := m.Bin
	if builtin {
		if p := m.streamers[port]; p != nil {
			running = p.Running()
		}
	} else if p := m.procs[port]; p != nil {
		running = p.Running()
	}
	m.mu.Unlock()
	if !running {
		if builtin {
			return nil, fmt.Errorf("open %s: builtin streamer on :%d is not running: %w", path, port, err)
		}
		return nil, fmt.Errorf("open %s: broadcast on :%d exited (bin=%s): %w", path, port, bin, err)
	}
	if errors.Is(err, syscall.ENXIO) {
		return nil, fmt.Errorf("open %s: timed out waiting for broadcast reader on :%d: %w", path, port, err)
	}
	return nil, err
}

func openFifoWriter(path string, timeout time.Duration) (*os.File, error) {
	// Linux FIFO deadlock: O_WRONLY waits for a reader, O_RDONLY waits for a writer.
	// Hold an O_RDWR fd so the write open can proceed; broadcast then opens O_RDONLY.
	hold, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer hold.Close()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			return f, nil
		}
		lastErr = err
		if errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EINTR) {
			if time.Now().After(deadline) {
				return nil, lastErr
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		return nil, err
	}
}

func ensureFifo(path string) error {
	if st, err := os.Stat(path); err == nil {
		if st.Mode()&os.ModeNamedPipe != 0 {
			f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err == nil {
				_ = f.Close()
				return nil
			}
			// ENXIO: no reader yet, but this process may write once broadcast opens the read end.
			if errors.Is(err, syscall.ENXIO) {
				return nil
			}
			if os.IsPermission(err) || errors.Is(err, syscall.EACCES) {
				if remErr := os.Remove(path); remErr != nil {
					return fmt.Errorf("fifo %s not writable: %w (remove: %v)", path, err, remErr)
				}
			} else {
				return err
			}
		} else if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syscall.Mkfifo(path, 0o600)
}
