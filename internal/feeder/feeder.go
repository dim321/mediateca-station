package feeder

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

type PaceMode string

const (
	PaceRealtime PaceMode = "realtime"
	PaceMax      PaceMode = "max"
)

type Feeder struct {
	ChunkSize int
	Pace      PaceMode
}

func (f *Feeder) chunk() int {
	if f.ChunkSize <= 0 {
		return 18800
	}
	return f.ChunkSize
}

// FeedFile copies path to dst. PaceMax writes as fast as possible (tests).
// PaceRealtime sleeps to approximate wall-clock duration = fileSize / bitrate
// using MPEG-TS 188-byte packets at a fixed assumed bitrate derived from
// durationHint if > 0; otherwise writes at ~chunk per 40ms (25 chunks/s)
// which is good enough for M1 smoke — prefer durationHint from schedule.
func (f *Feeder) FeedFile(ctx context.Context, path string, dst io.Writer) error {
	return f.FeedFileDuration(ctx, path, dst, 0)
}

func (f *Feeder) FeedFileDuration(ctx context.Context, path string, dst io.Writer, duration time.Duration) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	buf := make([]byte, f.chunk())
	var written int64
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if f.Pace != PaceMax && duration > 0 && size > 0 {
				target := time.Duration(float64(duration) * float64(written) / float64(size))
				if sleep := target - time.Since(start); sleep > 0 {
					timer := time.NewTimer(sleep)
					select {
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					case <-timer.C:
					}
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// WriteNullPackets writes count MPEG-TS null packets (PID 0x1FFF).
func (f *Feeder) WriteNullPackets(dst io.Writer, count int) error {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = 0x1f
	pkt[2] = 0xff
	pkt[3] = 0x10
	for i := 0; i < count; i++ {
		if _, err := dst.Write(pkt); err != nil {
			return fmt.Errorf("null packet: %w", err)
		}
	}
	return nil
}
