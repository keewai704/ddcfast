package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type daemonConfig struct {
	RestoreOnStart      bool `json:"restore_on_start"`
	RestoreRetryCount   int  `json:"restore_retry_count"`
	RestoreRetryDelayMs int  `json:"restore_retry_delay_ms"`
}

type serveOptions struct {
	SocketPath      string
	ConfigPath      string
	RestoreOverride *bool
}

type workerResult struct {
	resp response
	err  error
}

type workerRequest struct {
	fn   func(*ddcRuntime) (response, error)
	resp chan workerResult
}

type featureBatch struct {
	queued  []request
	waiters []chan workerResult
}

type daemonService struct {
	workerCh chan workerRequest

	batchesMu sync.Mutex
	batches   map[string]*featureBatch
}

func defaultDaemonConfig() daemonConfig {
	return daemonConfig{
		RestoreOnStart:      false,
		RestoreRetryCount:   20,
		RestoreRetryDelayMs: 500,
	}
}

func defaultConfigPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".config", "ddcfast", "config.json")
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "ddcfast", "config.json")
}

func defaultStatePath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".local", "state", "ddcfast", "state.json")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "ddcfast", "state.json")
}

func loadDaemonConfig(path string) (daemonConfig, error) {
	cfg := defaultDaemonConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.RestoreRetryCount < 1 {
		cfg.RestoreRetryCount = 1
	}
	if cfg.RestoreRetryDelayMs < 0 {
		cfg.RestoreRetryDelayMs = 0
	}
	return cfg, nil
}

func parseServeArgs(args []string) (serveOptions, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socketPath := fs.String("socket", defaultSocketPath(), "daemon socket path")
	configPath := fs.String("config", defaultConfigPath(), "config file path")
	restoreState := fs.Bool("restore-state", false, "restore saved brightness and contrast on startup")
	noRestoreState := fs.Bool("no-restore-state", false, "do not restore saved brightness and contrast on startup")

	if err := fs.Parse(args); err != nil {
		return serveOptions{}, err
	}
	if fs.NArg() != 0 {
		return serveOptions{}, errors.New("serve does not accept positional arguments")
	}
	if *restoreState && *noRestoreState {
		return serveOptions{}, errors.New("cannot combine --restore-state and --no-restore-state")
	}

	opts := serveOptions{
		SocketPath: *socketPath,
		ConfigPath: *configPath,
	}
	if *restoreState {
		value := true
		opts.RestoreOverride = &value
	}
	if *noRestoreState {
		value := false
		opts.RestoreOverride = &value
	}
	return opts, nil
}

func newDaemonService(config daemonConfig, statePath string) (*daemonService, error) {
	svc := &daemonService{
		workerCh: make(chan workerRequest),
		batches:  make(map[string]*featureBatch),
	}

	ready := make(chan error, 1)
	go svc.workerLoop(config, statePath, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return svc, nil
}

func (svc *daemonService) workerLoop(config daemonConfig, statePath string, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	rt, err := newDDCRuntime(config, statePath)
	ready <- err
	if err != nil {
		return
	}

	for req := range svc.workerCh {
		resp, err := req.fn(rt)
		req.resp <- workerResult{resp: resp, err: err}
		close(req.resp)
	}
}

func (svc *daemonService) run(fn func(*ddcRuntime) (response, error)) (response, error) {
	resultCh := make(chan workerResult, 1)
	svc.workerCh <- workerRequest{
		fn:   fn,
		resp: resultCh,
	}
	result := <-resultCh
	return result.resp, result.err
}

func (svc *daemonService) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: err.Error()})
		return
	}

	resp, err := svc.executeRequest(req)
	if err != nil {
		resp = response{OK: false, Error: err.Error()}
	}

	_ = json.NewEncoder(conn).Encode(resp)
}

func (svc *daemonService) executeRequest(req request) (response, error) {
	switch req.Command {
	case "list":
		return svc.run(func(rt *ddcRuntime) (response, error) {
			displays, err := rt.listCachedDisplays()
			if err != nil {
				return response{}, err
			}
			return response{OK: true, Displays: displays}, nil
		})
	case "refresh":
		return svc.run(func(rt *ddcRuntime) (response, error) {
			if err := rt.refreshDisplays(); err != nil {
				return response{}, err
			}
			return response{OK: true, Message: "display cache refreshed"}, nil
		})
	case "brightness":
		return svc.enqueueFeature(req, vcpBrightness)
	case "contrast":
		return svc.enqueueFeature(req, vcpContrast)
	case "power":
		return svc.run(func(rt *ddcRuntime) (response, error) {
			return rt.applyPower(req)
		})
	default:
		return response{}, fmt.Errorf("unsupported command %q", req.Command)
	}
}

func (svc *daemonService) enqueueFeature(req request, code uint8) (response, error) {
	wait := !req.Async
	key := featureBatchKey(req)
	var waiter chan workerResult
	if wait {
		waiter = make(chan workerResult, 1)
	}

	start := false
	initialRequests := []request{req}
	var initialWaiters []chan workerResult
	if wait {
		initialWaiters = []chan workerResult{waiter}
	}

	svc.batchesMu.Lock()
	batch := svc.batches[key]
	if batch == nil {
		svc.batches[key] = &featureBatch{}
		start = true
	} else {
		batch.queued = append(batch.queued, req)
		if wait {
			batch.waiters = append(batch.waiters, waiter)
		}
		initialRequests = nil
		initialWaiters = nil
	}
	svc.batchesMu.Unlock()

	if start {
		go svc.flushFeatureBatch(key, code, initialRequests, initialWaiters)
	}

	if !wait {
		return response{
			OK:      true,
			Message: fmt.Sprintf("%s queued on %s", req.Command, emptyFallback(strings.TrimSpace(req.Display), "default display")),
		}, nil
	}

	result := <-waiter
	return result.resp, result.err
}

func (svc *daemonService) flushFeatureBatch(key string, code uint8, requests []request, waiters []chan workerResult) {
	currentRequests := requests
	currentWaiters := waiters

	for {
		resp, err := svc.run(func(rt *ddcRuntime) (response, error) {
			return rt.applyFeatureRequests(currentRequests, code)
		})
		result := workerResult{resp: resp, err: err}
		for _, waiter := range currentWaiters {
			waiter <- result
			close(waiter)
		}

		svc.batchesMu.Lock()
		batch := svc.batches[key]
		if batch == nil || len(batch.queued) == 0 {
			delete(svc.batches, key)
			svc.batchesMu.Unlock()
			return
		}

		currentRequests = append([]request(nil), batch.queued...)
		currentWaiters = append([]chan workerResult(nil), batch.waiters...)
		batch.queued = nil
		batch.waiters = nil
		svc.batchesMu.Unlock()
	}
}

func featureBatchKey(req request) string {
	parts := []string{
		req.Command,
		strings.ToLower(strings.TrimSpace(req.Display)),
	}
	if req.Command == "brightness" {
		parts = append(parts, strconv.FormatFloat(req.Scale, 'f', 6, 64))
	}
	return strings.Join(parts, "\x00")
}
