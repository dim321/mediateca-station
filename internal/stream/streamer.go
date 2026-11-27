// Package stream provides an in-process live MPEG-TS HTTP server.
// Stage A: read from a named FIFO and fan-out to HTTP clients without
// blocking the feeder (slow clients are dropped or get drop-oldest).
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	clientBufPackets = 256 // ~48 KiB at 188-byte TS packets when chunked larger
	readBufSize      = 188 * 100
	fifoOpenTimeout  = 5 * time.Second
)

// Port is one live HTTP MPEG-TS listener bound to bind:port, fed from a FIFO.
type Port struct {
	bind string
	port int
	fifo string

	mu       sync.Mutex
	clients  map[uint64]*client
	nextID   uint64
	ln       net.Listener
	server   *http.Server
	fifoFile *os.File
	cancel   context.CancelFunc
	done     chan struct{}
	running  atomic.Bool
}

type client struct {
	id   uint64
	ch   chan []byte
	done chan struct{}
}

// Start listens on bind:port, opens fifoPath for reading, and fans out bytes
// to HTTP GET clients as video/mp2t. The listener stays up until Stop.
func Start(ctx context.Context, bind string, port int, fifoPath string) (*Port, error) {
	if bind == "" {
		return nil, fmt.Errorf("stream: bind is required")
	}
	if port <= 0 {
		return nil, fmt.Errorf("stream: invalid port %d", port)
	}
	if err := ensureFifo(fifoPath); err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(bind, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("stream listen %s: %w", addr, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	p := &Port{
		bind:    bind,
		port:    port,
		fifo:    fifoPath,
		clients: map[uint64]*client{},
		ln:      ln,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	p.running.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	p.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fifoFile, err := openFifoReader(fifoPath, fifoOpenTimeout)
	if err != nil {
		cancel()
		_ = ln.Close()
		p.running.Store(false)
		return nil, fmt.Errorf("stream open fifo %s: %w", fifoPath, err)
	}
	p.fifoFile = fifoFile

	go func() {
		err := p.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Listener died; stop fan-out.
			cancel()
		}
	}()
	go p.readLoop(runCtx)

	return p, nil
}

// Running reports whether the port streamer is still active.
func (p *Port) Running() bool {
	return p != nil && p.running.Load()
}

// ClientCount returns the number of active HTTP viewers.
func (p *Port) ClientCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

// Stop closes the HTTP listener and FIFO reader.
func (p *Port) Stop() error {
	if p == nil {
		return nil
	}
	if !p.running.Swap(false) {
		return nil
	}
	p.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	if p.fifoFile != nil {
		_ = p.fifoFile.Close()
	}
	<-p.done
	p.mu.Lock()
	for id, c := range p.clients {
		close(c.done)
		delete(p.clients, id)
	}
	p.mu.Unlock()
	return nil
}

func (p *Port) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	flusher.Flush()

	c := p.addClient()
	defer p.removeClient(c.id)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case chunk, ok := <-c.ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (p *Port) addClient() *client {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	c := &client{
		id:   p.nextID,
		ch:   make(chan []byte, clientBufPackets),
		done: make(chan struct{}),
	}
	p.clients[c.id] = c
	return c
}

func (p *Port) removeClient(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[id]; ok {
		delete(p.clients, id)
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
}

func (p *Port) readLoop(ctx context.Context) {
	defer close(p.done)
	buf := make([]byte, readBufSize)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := p.fifoFile.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			p.fanOut(chunk)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EBADF) {
				// Writer closed or we are stopping; keep listener alive and retry
				// until Stop cancels — matches stock broadcast gap semantics.
				select {
				case <-ctx.Done():
					return
				case <-time.After(20 * time.Millisecond):
					continue
				}
			}
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Millisecond):
					continue
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
	}
}

func (p *Port) fanOut(chunk []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		select {
		case c.ch <- chunk:
		default:
			// Drop-oldest: discard one queued chunk and retry once.
			select {
			case <-c.ch:
			default:
			}
			select {
			case c.ch <- chunk:
			default:
				// Still full — drop the slow client so the feeder is never blocked.
				select {
				case <-c.done:
				default:
					close(c.done)
				}
				delete(p.clients, id)
			}
		}
	}
}

func openFifoReader(path string, timeout time.Duration) (*os.File, error) {
	// O_RDWR does not block on Linux when the other end is missing, and still
	// allows reading once a writer attaches (same hold pattern as OpenFifo).
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ensureFifo(path string) error {
	if st, err := os.Stat(path); err == nil {
		if st.Mode()&os.ModeNamedPipe != 0 {
			f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
			if err == nil {
				_ = f.Close()
				return nil
			}
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
