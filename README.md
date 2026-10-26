# vpowerd

A small headless daemon for Voltronic-based UPS units (sold under many brands,
for example Tecnoware). It replaces the ViewPower monitor service.

- Talks to the UPS over USB (`/dev/hidrawN`) or RS232, with no native code and no libusb.
- Serves a web dashboard and a JSON API.
- Publishes to MQTT with Home Assistant discovery.
- Runs in Docker or in a Proxmox LXC. One static binary, about 6 MB of RAM.

The protocol logic comes from a reverse engineering of ViewPower. See
[docs/REA-findings.md](docs/REA-findings.md) for what was observed and what
is still unknown.

> **Status:** runs on a Tecnoware 800VA (`PMV`, USB `0665:5161`) over hidraw.
> Other Voltronic-based units are untested; see
> [First run on real hardware](#first-run-on-real-hardware).

## Install

On Alpine (OpenRC) or Debian/Ubuntu (systemd), as root:

```sh
wget -qO- https://raw.githubusercontent.com/damianogiusti/vpowerd/main/install.sh | sh
```

`curl -fsSL … | sh` works too. The script:

- downloads the static binary for your CPU (amd64 or arm64) from the `edge`
  release, which CI rebuilds on every push to `main`;
- checks every file against `checksums.txt` and stops on a mismatch;
- creates the service (OpenRC or systemd) and a config file, then starts it;
- keeps your configuration when you run it again to upgrade.

Then open `http://<host>:8080` and set the MQTT broker from the page.

Piping a script into `sh` means trusting this repository. If you prefer,
download `install.sh`, read it, then run it. Offline install from a folder
with the release files: `VPOWERD_BASE_URL=/path/to/files sh install.sh`.

To remove it:

```sh
rc-service vpowerd stop; rc-update del vpowerd                      # Alpine
systemctl disable --now vpowerd                                      # Debian
rm -f /usr/local/bin/vpowerd /etc/init.d/vpowerd /etc/conf.d/vpowerd \
      /etc/systemd/system/vpowerd.service /etc/vpowerd.env
rm -rf /var/lib/vpowerd   # saved settings
```

## Why not ViewPower

ViewPower crashes and grows in memory over time. The analysis found concrete
causes. For example, the USB library calls `GetStringUTFChars` twice for every
command and never releases the strings. The detection code can also loop
forever if the UPS stops answering. vpowerd keeps every buffer bounded. Every
device call has a deadline, and the history uses fixed-size ring buffers.

## Supported devices

Detection follows the same order as ViewPower. It sends `M` and reads the
model letter.

| `M` reply | Protocol | Battery charge | Runtime |
|---|---|---|---|
| `V` | `PMV` | estimated from battery voltage (ViewPower formula) | not reported |
| `H`, `I`, `J` | `PMV_H`, `PMV_I`, `PMV_J` | reported by the UPS (`QI`) | reported (`QI`) |
| no reply, but `Q1` works | `MEGATEC` (fallback, not in ViewPower) | estimated | not reported |

Not supported yet: the `QPI` family (`P00`…`P99`, mostly online UPS units),
`PMV_Simplify` (`M` = `P`/`T`) and Modbus. When the daemon finds one of
these, it says which one in `last_error` on `/api/status`. Adding one needs the
real replies of the device.

USB IDs that are auto-detected: `0665:5161`, `2e66:0100-0102`, `2e28:0001`,
`10af:0002`, `09ae:3028`.

## Quick start with Docker

On the machine where the UPS USB cable is plugged in:

```sh
# 1. Stable device name and permissions
sudo cp deploy/99-vpowerd.rules /etc/udev/rules.d/
sudo udevadm control --reload && sudo udevadm trigger
ls -l /dev/ups            # -> hidrawN

# 2. Edit environment in docker-compose.yml (MQTT, control), then:
docker compose up -d --build
```

Open `http://<host>:8080`.

If the UPS is unplugged and plugged again, the kernel can give it a new
`hidrawN`. The container keeps the old node, so restart it
(`docker compose restart`). The LXC setup below has the same limit.

Try it without a UPS:

```sh
make run-mock      # http://localhost:8080, simulated PMV_J device
```

## Proxmox LXC

Tested layout: the UPS USB cable on the Proxmox host, vpowerd in an
unprivileged Alpine container. Debian works too: the installer detects
OpenRC or systemd.

1. On the **Proxmox host**, give the device a stable name:

   ```sh
   echo 'SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0665", ATTRS{idProduct}=="5161", SYMLINK+="ups"' \
     > /etc/udev/rules.d/99-vpowerd.rules
   udevadm control --reload && udevadm trigger && ls -l /dev/ups
   ```
   If the UPS was passed through to a VM before, remove it from the VM first.
2. Pass it to the container (Proxmox VE 8.1 or later). `gid=20` is `dialout`
   in Alpine and Debian:

   ```sh
   pct set <CTID> -dev0 /dev/ups,gid=20,mode=0660
   ```
   Inside the container the node is `/dev/ups`. vpowerd detects that it is a
   hidraw node from its device number, not from its name.
3. Inside the container, run the one-line installer from [Install](#install).
   The default `VPOWERD_DEVICE=auto` finds `/dev/ups` by its device number.

On Alpine the service runs as user `vpowerd` under `supervise-daemon`, which
restarts it if it exits. Logs go to syslog: `logread -f` or
`/var/log/messages`.

If the UPS is unplugged and plugged again, the host may give it another
`hidrawN`. Restart the container so `dev0` picks up the new node.

## Home Assistant

### MQTT (recommended)

Open the web UI, section **MQTT / Home Assistant**: enter the broker URL and
credentials, press **Test connection**, then **Save**. The daemon reconnects
right away, with no restart. Settings are stored in
`/var/lib/vpowerd/settings.json` (mode 0600, the password is in clear text
there). Once saved, they override the `VPOWERD_MQTT_*` variables, which are
only first-run defaults. If `VPOWERD_API_TOKEN` is set, saving needs it too.
Without a token, anyone on your network who can open the page can change the
broker.

Entities appear by themselves under
one device: mode, voltages, frequency, load, battery voltage and charge,
runtime (when reported), temperature (when reported), utility power, battery
low, fault, self test, beeper.

With `VPOWERD_ALLOW_CONTROL=true` you also get buttons: 10 s battery test,
cancel test, toggle beeper, cancel shutdown. **Shutdown is never exposed as a
button.** It is available only on the REST API, with explicit delays.

Topics, with the default prefix and id:

| Topic | Content |
|---|---|
| `vpowerd/ups/state` | flat JSON, retained |
| `vpowerd/ups/availability` | `online` / `offline` (also the MQTT last will) |
| `vpowerd/ups/command` | `test_10s`, `test_cancel`, `beeper_toggle`, `shutdown_cancel` |

### REST only

```yaml
rest:
  - resource: http://vpowerd.lan:8080/api/status
    scan_interval: 10
    sensor:
      - name: UPS input voltage
        value_template: "{{ value_json.status.input_voltage }}"
        unit_of_measurement: V
        device_class: voltage
      - name: UPS battery charge
        value_template: "{{ value_json.status.battery_charge }}"
        unit_of_measurement: "%"
        device_class: battery
    binary_sensor:
      - name: UPS on battery
        value_template: "{{ value_json.status.mode in ['battery', 'battery_test'] }}"
```

## HTTP API

| Method | Path | Notes |
|---|---|---|
| GET | `/api/status` | identity, rating, latest status, connection state |
| GET | `/api/history` | samples in the history window, oldest first |
| GET | `/api/events` | last 500 events |
| GET | `/api/capabilities` | whether control is enabled and a token is required |
| POST | `/api/command` | `{"kind":"test_10s"}`; shutdown: `{"kind":"shutdown","delay_seconds":120,"restore_minutes":1}` |
| GET | `/api/settings/mqtt` | broker settings and connection state; never returns the password |
| PUT | `/api/settings/mqtt` | save and apply; omit `password` to keep it, `""` to clear it |
| POST | `/api/settings/mqtt/test` | try a connection with the given settings |
| GET | `/api/raw?q=QS` | raw reply of a read-only query from an allow list, for diagnostics |
| GET | `/healthz` | process alive |
| GET | `/readyz` | 204 only when the UPS is connected |

`/api/command`, `/api/raw` and the settings writes need `Authorization: Bearer <token>` when
`VPOWERD_API_TOKEN` is set.

About `restore_minutes: 0`: some firmwares read it as "restore immediately",
others as "never restore". Test it on your unit before you rely on it.

## Configuration

Every flag has an environment variable. Flags win.

| Env | Default | Meaning |
|---|---|---|
| `VPOWERD_DEVICE` | `auto` | `auto`, a hidraw node (`/dev/hidraw1`, `/dev/ups`), or a serial port (`/dev/ttyUSB0`, 2400 8N1) |
| `VPOWERD_TRANSPORT` | `auto` | `auto`, `hidraw` or `serial`; auto checks the device number in sysfs |
| `VPOWERD_LISTEN` | `:8080` | HTTP address |
| `VPOWERD_POLL_INTERVAL` | `10s` | minimum `2s` |
| `VPOWERD_HISTORY_WINDOW` | `24h` | in-memory history, capped at 20 000 samples |
| `VPOWERD_DEVICE_ID` | `ups` | MQTT topics and HA unique IDs (`a-z0-9_-`) |
| `VPOWERD_ALLOW_CONTROL` | `false` | enable commands |
| `VPOWERD_API_TOKEN` | – | Bearer token for commands and raw queries |
| `VPOWERD_STATE_DIR` | `/var/lib/vpowerd` | where settings saved from the UI live |
| `VPOWERD_MQTT_URL` | – | first-run default, e.g. `tcp://broker:1883`; the UI overrides it |
| `VPOWERD_MQTT_USER` / `_PASSWORD` | – | broker credentials |
| `VPOWERD_MQTT_PREFIX` | `vpowerd` | topic prefix |
| `VPOWERD_MQTT_DISCOVERY_PREFIX` | `homeassistant` | HA discovery prefix |
| `VPOWERD_MOCK` | – | `V`, `H`, `I` or `J`: use the simulator |
| `VPOWERD_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## First run on real hardware

1. Stop ViewPower and any NUT driver first: only one program can use the device.
   ViewPower detaches the kernel HID driver and never re-attaches it. After
   stopping it, re-bind the interface (find it with `lsusb -t`), e.g.
   `echo -n 2-1:1.0 | sudo tee /sys/bus/usb/drivers/usbhid/bind`, or replug the
   cable. `upsMonitor.service` also leaves its Tomcat JVM running after a stop.
2. Check that the kernel created the node: `ls /dev/hidraw*` and
   `cat /sys/class/hidraw/hidraw*/device/uevent | grep HID_ID`.
   You should see `0003:00000665:00005161` or another ID from the table above.
3. Start vpowerd with `VPOWERD_LOG_LEVEL=debug` and open `/api/status`.
4. If detection fails, send the output of these queries. They are read-only
   and use the token if you set one:

   ```sh
   for q in M QS Q1 F I QI QPI QID; do curl -s "http://<host>:8080/api/raw?q=$q"; echo; done
   ```

   `/api/raw` works only after a device is detected. If nothing is detected,
   the log shows the replies to `M` and `QPI`.

Things that may need a fix on real hardware. These are hypotheses, not
observed problems:

- Some USB-serial bridges answer in reports that end without CR. The reader
  then times out. The fix is a small change in `readReply`.
- If `/dev/hidrawN` never appears, the kernel HID driver did not bind. That
  needs a libusb transport, which is not implemented on purpose (it needs cgo).

## Development

No local Go toolchain is needed. Every target runs Go inside Docker.

```sh
make test       # go vet + go test -race
make dist       # dist/: binaries, service files, checksums.txt (as CI does)
make image      # Docker image
make run-mock   # simulator on :8080
```

Layout:

```
cmd/vpowerd          wiring, signals, healthcheck
internal/ups         domain types (units, modes, commands)
internal/transport   hidraw, serial, mock, USB discovery
internal/megatec     dialect detection, parsers, charge estimate, commands
internal/monitor     session loop, reconnect, history, events
internal/api         REST API and embedded web UI
internal/mqtt        state publishing and HA discovery
```

## Disclaimer

This is an independent project. It is not affiliated with or endorsed by
Voltronic Power, the authors of ViewPower, or any UPS brand. ViewPower and
other names are trademarks of their owners. The repository contains no code
from ViewPower: the protocol was studied for interoperability, as described
in [docs/REA-findings.md](docs/REA-findings.md). Use it at your own risk; a
UPS protects real hardware.
