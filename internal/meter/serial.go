package meter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.bug.st/serial"
)

// Command is a single ASCII control byte the LP-100A accepts.
type Command byte

const (
	CmdAlarm  Command = 'A' // increment SWR alarm setpoint
	CmdMode   Command = 'M' // increment display mode
	CmdToggle Command = 'F' // toggle peak/avg/tune
)

// ParseCommand maps the wire-protocol action name to the meter byte.
func ParseCommand(action string) (Command, bool) {
	switch action {
	case "alarm_step":
		return CmdAlarm, true
	case "mode_step":
		return CmdMode, true
	case "peak_toggle":
		return CmdToggle, true
	}
	return 0, false
}

type SerialConfig struct {
	Port         string
	Baud         int
	PollInterval time.Duration
}

// Owner is the single goroutine that holds the serial port and arbitrates
// reads (poll responses) and writes (control commands) so they never collide.
type Owner struct {
	cfg      SerialConfig
	commands chan Command
	out      chan<- Snapshot
	logger   *slog.Logger
}

func NewOwner(cfg SerialConfig, out chan<- Snapshot, logger *slog.Logger) *Owner {
	return &Owner{
		cfg:      cfg,
		commands: make(chan Command, 16),
		out:      out,
		logger:   logger,
	}
}

// Submit queues a command for the next interleave slot. Returns false if the
// queue is full so callers can NACK the requesting client.
func (o *Owner) Submit(c Command) bool {
	select {
	case o.commands <- c:
		return true
	default:
		return false
	}
}

// Run loops forever, opening the port and polling, reopening on error with
// exponential backoff. Returns only when ctx is cancelled.
func (o *Owner) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		err := o.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			o.logger.Warn("serial loop ended", "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (o *Owner) runOnce(ctx context.Context) error {
	portName := o.cfg.Port
	if portName == "" || portName == "auto" {
		var err error
		portName, err = autoDetectPort()
		if err != nil {
			return fmt.Errorf("autodetect: %w", err)
		}
	}

	mode := &serial.Mode{
		BaudRate: o.cfg.Baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	port, err := serial.Open(portName, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", portName, err)
	}
	defer port.Close()

	// 30 ms inter-byte timeout. The LP-100A response (~43 bytes at 115200 baud)
	// arrives in <5 ms; the first 30 ms of idle after the last byte signals
	// end-of-frame, mirroring the Node-RED reference's "newline":"50" config.
	const readTimeout = 30 * time.Millisecond
	if err := port.SetReadTimeout(readTimeout); err != nil {
		return fmt.Errorf("set read timeout: %w", err)
	}
	o.logger.Info("serial open", "port", portName, "baud", o.cfg.Baud)

	ticker := time.NewTicker(o.cfg.PollInterval)
	defer ticker.Stop()

	buf := make([]byte, 256)
	line := make([]byte, 0, 128)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := o.drainCommands(port); err != nil {
				return err
			}
			if _, err := port.Write([]byte{'P'}); err != nil {
				return fmt.Errorf("write poll: %w", err)
			}
			line = line[:0]
			frameDeadline := time.Now().Add(o.cfg.PollInterval - 10*time.Millisecond)
			if min := time.Now().Add(60 * time.Millisecond); frameDeadline.Before(min) {
				frameDeadline = min
			}
			for {
				n, err := port.Read(buf)
				if err != nil {
					return fmt.Errorf("read: %w", err)
				}
				if n == 0 {
					// Idle gap: end-of-frame if we have data, else still waiting.
					if len(line) > 0 || time.Now().After(frameDeadline) {
						break
					}
					continue
				}
				line = append(line, buf[:n]...)
				if i := indexByte(line, '\n'); i >= 0 {
					line = line[:i]
					break
				}
				if len(line) > 200 {
					line = line[:0]
					break
				}
			}
			if len(line) == 0 {
				continue
			}
			snap, err := Parse(string(line))
			if err != nil {
				o.logger.Debug("parse error", "err", err, "raw", string(line))
				continue
			}
			select {
			case o.out <- snap:
			case <-ctx.Done():
				return nil
			default:
				// Hub is slow; drop this sample. Next poll will replace it.
			}
		}
	}
}

// drainCommands writes any queued control bytes with a short interleave delay
// matching the Node-RED reference flow's 30ms gap. The poll that follows will
// reflect the new state.
func (o *Owner) drainCommands(port serial.Port) error {
	for {
		select {
		case cmd := <-o.commands:
			if _, err := port.Write([]byte{byte(cmd)}); err != nil {
				return fmt.Errorf("write cmd %c: %w", byte(cmd), err)
			}
			o.logger.Debug("sent command", "cmd", string(byte(cmd)))
			time.Sleep(30 * time.Millisecond)
		default:
			return nil
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func autoDetectPort() (string, error) {
	// Prefer the udev-assigned symlink if present.
	for _, hint := range []string{"/dev/ttyUSB_LPM", "/dev/serial/by-id/lp100a"} {
		if _, err := os.Stat(hint); err == nil {
			return hint, nil
		}
	}
	ports, err := serial.GetPortsList()
	if err != nil {
		return "", err
	}
	if len(ports) == 0 {
		return "", errors.New("no serial ports found")
	}
	for _, p := range ports {
		lp := strings.ToLower(p)
		if strings.Contains(lp, "usb") || strings.Contains(lp, "usbserial") {
			return p, nil
		}
	}
	return ports[0], nil
}
