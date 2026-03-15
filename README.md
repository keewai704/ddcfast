# ddcfast

Fast DDC/CI monitor control for Wayland desktops.

`ddcfast` wraps `libddcutil` with a small Go daemon so brightness, contrast, and power changes stay responsive under key repeat. It was built for desktop keybindings where plain `ddcutil` startup cost is too high.

## Features

- Brightness, contrast, and monitor power control
- Display selectors by connector, bus, display number, model, or serial
- Optional logical scaling for brightness, for example `75% hardware = 100% UI`
- Auto-spawned daemon with:
  - display cache
  - feature cache
  - request coalescing
  - long-lived display handles
- Optional restore of the last brightness/contrast values on login
- `--async` mode for instant-return keybinding integration

## Requirements

- Go 1.22+
- `libddcutil` with pkg-config metadata
- Linux with DDC/CI-capable monitors
- `/dev/i2c-*` access

Arch packages typically needed:

```bash
sudo pacman -S go ddcutil i2c-tools
```

You may also need the `i2c-dev` kernel module and DDC/CI enabled in the monitor OSD.

## Build

```bash
go build -o ddcfast .
```

Install wherever you want, for example:

```bash
install -Dm755 ddcfast ~/.local/bin/ddcfast
```

## Usage

List DDC-capable displays:

```bash
ddcfast list
```

Set brightness or contrast:

```bash
ddcfast brightness 60 --display DP-1 --scale 0.75
ddcfast contrast 40 --display DP-1
```

Relative changes:

```bash
ddcfast brightness +5 --display DP-1 --scale 0.75
ddcfast contrast -5 --display DP-1
```

Instant-return mode for keybindings:

```bash
ddcfast brightness +5 --display DP-1 --scale 0.75 --async
ddcfast contrast +5 --display DP-1 --async
```

Monitor power:

```bash
ddcfast power off --display DP-1
ddcfast power on --display DP-1
```

Refresh cached displays:

```bash
ddcfast refresh
```

## Display Selectors

`--display` accepts any of:

- connector name, for example `DP-1` or `card0-DP-1`
- `bus:<n>`
- `disp:<n>`
- model substring
- serial substring

## Daemon

The client talks to a Unix socket daemon. If the socket is missing, it auto-spawns one.

Run it explicitly:

```bash
ddcfast serve
```

Useful flags:

```bash
ddcfast serve --socket /run/user/1000/ddcfast.sock
ddcfast serve --config ~/.config/ddcfast/config.json
ddcfast serve --restore-state
ddcfast serve --no-restore-state
```

## Configuration

Default config path:

```text
~/.config/ddcfast/config.json
```

Example:

```json
{
  "restore_on_start": true,
  "restore_retry_count": 20,
  "restore_retry_delay_ms": 500
}
```

State is stored at:

```text
~/.local/state/ddcfast/state.json
```

## systemd User Service

Example files are under [`examples/`](./examples).

Install example service:

```bash
install -Dm644 examples/systemd-user/ddcfast.service ~/.config/systemd/user/ddcfast.service
install -Dm644 examples/config.json ~/.config/ddcfast/config.json
systemctl --user daemon-reload
systemctl --user enable --now ddcfast.service
```

## Architecture

The hot path is optimized for keybinding use:

- a single worker goroutine is locked to one OS thread for `libddcutil`
- display enumeration is cached
- feature reads are cached
- repeated requests are coalesced while one operation is in flight
- open display handles are reused instead of reopened for every request

That avoids the repeated `ddcutil` process startup and display rediscovery cost that makes shell-based solutions feel slow.

## Project Layout

- [`main.go`](./main.go): CLI, request parsing, auto-spawn client path
- [`daemon.go`](./daemon.go): daemon, worker loop, batching/coalescing
- [`ddcutil.go`](./ddcutil.go): `libddcutil` bindings, cache, persistence, restore

## License

MIT
