package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	socketName = "ddcfast.sock"
)

type request struct {
	Command string  `json:"command"`
	Display string  `json:"display,omitempty"`
	Value   string  `json:"value,omitempty"`
	Scale   float64 `json:"scale,omitempty"`
	Async   bool    `json:"async,omitempty"`
}

type response struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Message  string    `json:"message,omitempty"`
	Display  *Display  `json:"display,omitempty"`
	Displays []Display `json:"displays,omitempty"`
	Current  int       `json:"current,omitempty"`
	Max      int       `json:"max,omitempty"`
	Target   int       `json:"target,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := serveMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "list":
		if err := clientMain("list", os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "refresh":
		if err := clientMain("refresh", os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "brightness", "contrast":
		if err := clientMain(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "power":
		if err := clientMain("power", os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s list [--config <path>]\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, "  %s refresh [--config <path>]\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, "  %s {brightness|contrast} {+N|-N|N} --display <selector> [--scale 0.75] [--async] [--config <path>]\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, "  %s power {on|off} --display <selector> [--config <path>]\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, "  %s serve [--socket <path>] [--config <path>] [--restore-state|--no-restore-state]\n", filepath.Base(os.Args[0]))
}

func clientMain(command string, args []string) error {
	req, socketPath, configPath, err := parseClientArgs(command, args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := sendWithDaemon(ctx, socketPath, configPath, req)
	if err != nil {
		resp, err = executeRequest(req)
		if err != nil {
			return err
		}
	}

	return printResponse(req.Command, resp)
}

func parseClientArgs(command string, args []string) (request, string, string, error) {
	flagArgs, positional, err := normalizeArgs(args)
	if err != nil {
		return request{}, "", "", err
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	display := fs.String("display", "", "display selector")
	scale := fs.Float64("scale", 1.0, "logical max scale, e.g. 0.75")
	socketPath := fs.String("socket", defaultSocketPath(), "daemon socket path")
	configPath := fs.String("config", defaultConfigPath(), "config file path")
	async := fs.Bool("async", false, "queue the request and return immediately")

	switch command {
	case "list", "refresh":
		if err := fs.Parse(flagArgs); err != nil {
			return request{}, "", "", err
		}
		if len(positional) != 0 {
			return request{}, "", "", fmt.Errorf("%s does not accept positional arguments", command)
		}
		return request{Command: command}, *socketPath, *configPath, nil

	case "brightness", "contrast":
		if err := fs.Parse(flagArgs); err != nil {
			return request{}, "", "", err
		}
		if len(positional) != 1 {
			return request{}, "", "", fmt.Errorf("%s requires one value like 42, +5, or -5", command)
		}
		if *scale <= 0 {
			return request{}, "", "", errors.New("--scale must be > 0")
		}
		return request{
			Command: command,
			Display: *display,
			Value:   positional[0],
			Scale:   *scale,
			Async:   *async,
		}, *socketPath, *configPath, nil

	case "power":
		if err := fs.Parse(flagArgs); err != nil {
			return request{}, "", "", err
		}
		if len(positional) != 1 {
			return request{}, "", "", errors.New("power requires one value: on or off")
		}
		mode := strings.ToLower(positional[0])
		if mode != "on" && mode != "off" {
			return request{}, "", "", fmt.Errorf("invalid power mode %q", positional[0])
		}
		return request{
			Command: command,
			Display: *display,
			Value:   mode,
		}, *socketPath, *configPath, nil
	}

	return request{}, "", "", fmt.Errorf("unsupported command %q", command)
}

func normalizeArgs(args []string) ([]string, []string, error) {
	knownValueFlags := map[string]bool{
		"--display": true,
		"--scale":   true,
		"--socket":  true,
		"--config":  true,
	}
	knownBoolFlags := map[string]bool{
		"--async": true,
	}

	flagArgs := make([]string, 0, len(args))
	positional := make([]string, 0, 1)

	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]

		switch {
		case knownValueFlags[arg]:
			if idx+1 >= len(args) {
				return nil, nil, fmt.Errorf("missing value for %s", arg)
			}
			flagArgs = append(flagArgs, arg, args[idx+1])
			idx++
		case knownBoolFlags[arg]:
			flagArgs = append(flagArgs, arg)
		case strings.HasPrefix(arg, "--display="), strings.HasPrefix(arg, "--scale="), strings.HasPrefix(arg, "--socket="), strings.HasPrefix(arg, "--config="):
			flagArgs = append(flagArgs, arg)
		case strings.HasPrefix(arg, "--async="):
			flagArgs = append(flagArgs, arg)
		default:
			positional = append(positional, arg)
		}
	}

	return flagArgs, positional, nil
}

func printResponse(command string, resp response) error {
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "request failed"
		}
		return errors.New(resp.Error)
	}

	switch command {
	case "list":
		for _, d := range resp.Displays {
			connector := d.Connector
			if connector == "" {
				connector = fmt.Sprintf("disp:%d", d.DisplayNo)
			}
			fmt.Printf("%s bus:%d disp:%d model:%s serial:%s\n", connector, d.BusNo, d.DisplayNo, emptyFallback(d.Model, "-"), emptyFallback(d.Serial, "-"))
		}
	case "refresh":
		fmt.Println(resp.Message)
	default:
		fmt.Println(resp.Message)
	}

	return nil
}

func executeRequest(req request) (response, error) {
	switch req.Command {
	case "list":
		displays, err := listDisplays()
		if err != nil {
			return response{}, err
		}
		out := make([]Display, 0, len(displays))
		for _, d := range displays {
			out = append(out, d.Display)
		}
		return response{OK: true, Displays: out}, nil

	case "refresh":
		if err := redetectDisplays(); err != nil {
			return response{}, err
		}
		return response{OK: true, Message: "display cache refreshed"}, nil

	case "brightness":
		return executeScaledFeature(req, vcpBrightness)

	case "contrast":
		return executeScaledFeature(req, vcpContrast)

	case "power":
		return executePower(req)
	}

	return response{}, fmt.Errorf("unsupported command %q", req.Command)
}

func executeScaledFeature(req request, code uint8) (response, error) {
	display, err := resolveDisplay(req.Display)
	if err != nil {
		return response{}, err
	}

	current, err := getFeature(display, code)
	if err != nil {
		return response{}, err
	}
	if current.Max <= 0 {
		return response{}, fmt.Errorf("feature 0x%02X on %s has invalid max value %d", code, displayLabel(display.Display), current.Max)
	}

	target, err := computeScaledTarget(current, req.Value, req.Scale)
	if err != nil {
		return response{}, err
	}

	if err := setFeature(display, code, target); err != nil {
		return response{}, err
	}

	name := "brightness"
	if code == vcpContrast {
		name = "contrast"
	}

	return response{
		OK:      true,
		Display: &display.Display,
		Current: current.Current,
		Max:     current.Max,
		Target:  target,
		Message: fmt.Sprintf("%s set to %d/%d on %s", name, target, current.Max, displayLabel(display.Display)),
	}, nil
}

func computeScaledTarget(current FeatureValue, raw string, scale float64) (int, error) {
	if scale <= 0 {
		return 0, errors.New("scale must be > 0")
	}

	scaledMax := int(math.Round(float64(current.Max) * scale))
	if scaledMax < 1 {
		scaledMax = 1
	}
	if scaledMax > current.Max {
		scaledMax = current.Max
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("missing value")
	}

	switch raw[0] {
	case '+', '-':
		delta, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid relative value %q", raw)
		}

		currentScaled := int(math.Round(float64(current.Current) * 100 / float64(scaledMax)))
		targetScaled := clamp(currentScaled+delta, 0, 100)
		return int(math.Round(float64(targetScaled) * float64(scaledMax) / 100)), nil
	default:
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid absolute value %q", raw)
		}
		value = clamp(value, 0, 100)
		return int(math.Round(float64(value) * float64(scaledMax) / 100)), nil
	}
}

func executePower(req request) (response, error) {
	display, err := resolveDisplay(req.Display)
	if err != nil {
		return response{}, err
	}

	var values []int
	switch req.Value {
	case "on":
		values = []int{0x01}
	case "off":
		values = []int{0x05, 0x04}
	default:
		return response{}, fmt.Errorf("invalid power mode %q", req.Value)
	}

	var lastErr error
	for _, v := range values {
		if err := setFeature(display, vcpPowerMode, v); err == nil {
			return response{
				OK:      true,
				Display: &display.Display,
				Target:  v,
				Message: fmt.Sprintf("power %s on %s", req.Value, displayLabel(display.Display)),
			}, nil
		} else {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = errors.New("power operation failed")
	}
	return response{}, lastErr
}

func defaultSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	return filepath.Join(runtimeDir, socketName)
}

func sendWithDaemon(ctx context.Context, socketPath, configPath string, req request) (response, error) {
	if resp, err := sendRequest(ctx, socketPath, req); err == nil {
		return resp, nil
	}

	if err := ensureDaemon(ctx, socketPath, configPath); err != nil {
		return response{}, err
	}

	return sendRequest(ctx, socketPath, req)
}

func sendRequest(ctx context.Context, socketPath string, req request) (response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		return response{}, err
	}

	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, err
	}

	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func ensureDaemon(ctx context.Context, socketPath, configPath string) error {
	if err := spawnDaemon(socketPath, configPath); err != nil {
		return err
	}

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for daemon")
		case <-ticker.C:
			conn, err := net.DialTimeout("unix", socketPath, 80*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func spawnDaemon(socketPath, configPath string) error {
	lockFile, err := os.OpenFile(socketPath+".spawn.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()

	if conn, err := net.DialTimeout("unix", socketPath, 80*time.Millisecond); err == nil {
		conn.Close()
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "serve", "--socket", socketPath, "--config", configPath)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}

func serveMain(args []string) error {
	opts, err := parseServeArgs(args)
	if err != nil {
		return err
	}

	config, err := loadDaemonConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	if opts.RestoreOverride != nil {
		config.RestoreOnStart = *opts.RestoreOverride
	}

	service, err := newDaemonService(config, defaultStatePath())
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(opts.SocketPath), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(opts.SocketPath); err == nil {
		_ = os.Remove(opts.SocketPath)
	}

	l, err := net.Listen("unix", opts.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		l.Close()
		_ = os.Remove(opts.SocketPath)
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go service.handleConn(conn)
	}
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
