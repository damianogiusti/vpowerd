# ViewPower reverse engineering findings

Source artifacts, the vendor's Linux installers:

| Artifact | Contents | Version hint |
|---|---|---|
| `ViewPower_linux_x64_text.tar.gz` | install4j installer; app ZIP at offset 16223 of the `.sh` | files dated 2025-10-22 |
| `installViewPowerHTML_Linux_x86_64.tar.gz` | InstallAnywhere installer; resources ZIP after the bundled JRE | files dated 2021-01 |

The newer package (`text`) is the reference. The older one was used to
cross-check: the protocol classes are the same.

Tools used: REA with the Ghidra 12.1.4 provider for the native library. CFR
0.152 for the Java jars, because REA had no Java decompiler provider in this
session (JEB missing).

Every statement below is labelled as:

- **Observed**: read directly in decompiled code.
- **Inferred**: a conclusion drawn from observed code, not executed.
- **Unknown**: not resolved.

## Architecture of ViewPower

- **Observed.** `StartMain` starts a Java process with an RMI registry on port 51099,
  an embedded Tomcat for the web UI, and a Derby database for history and events.
- **Observed.** `CommConnector` scans every serial port every 5 s and tries Modbus on
  each one. A separate thread lists USB devices every 5 s. Each found device
  gets a `WorkMonitor` thread.
- **Observed.** The protocol is chosen by `MonitorAdpater`: it tries the registered
  protocol classes in order until one's `matchProtocol` returns true. The PMV
  family comes first, then `P00`…`P99`, then Modbus `PTaurus`.

## USB transport (`jre/lib/libUSBDevice.so`)

Ghidra session on SHA-256 `df6658c0…8bf8`. Evidence IDs:
`ev_c999bc77…` (symbol list), `ev_7373ecb8…` (`ups_read`, `ups_write`,
`Java_usb_USBComm_sendCommand`, `swCalculateCRC`), `ev_4809a86c…` (JNI
helpers), `ev_fac7ae32…` (`ups_open`, `ups_list`, `ups_close`, `ups_get_fd`).

- **Observed.** The library is statically linked libusb-0.1.
- **Observed.** Write (`ups_write`): the command is split in 8 byte chunks. Each chunk is sent with
  `usb_control_msg(dev, 0x21, 0x09, 0x0200, 0, buf, 8, timeout)`, a HID
  SET_REPORT for output report ID 0.
- **Observed.** Read (`ups_read`): `usb_interrupt_read` of 8 bytes with a 400 ms timeout.
  It tolerates up to 4 timeouts before the first data and stops at the first
  timeout after data. It does not stop at CR; the Java side cuts at CR.
- **Observed.** The CRC (`swCalculateCRC`) is CRC-16 with a nibble table. Bytes equal to
  `(`, CR or LF are incremented. It is used only when `setUseCRC(1)`, which
  the PMV path never calls.
- **Inferred.** Writing a 9 byte buffer (`0x00` + 8 bytes) to `/dev/hidrawN`
  makes the kernel send the same SET_REPORT. Reading hidraw returns the same
  interrupt IN reports. vpowerd relies on this. It has not been tested on the
  real device yet.

### Defects found in the native library

- **Observed.** `Java_usb_USBComm_sendCommand` calls `GetStringUTFChars` twice per
  call (`usbID` and the command) and never calls `ReleaseStringUTFChars`.
  `Java_usb_USBComm_closeUSBPort` has the same pattern.
- **Inferred.** This leaks native memory on every command. The process sends
  several commands per poll, forever, so memory grows without limit.
- **Observed.** On any failed exchange, `sendCommand` calls `ups_close` and then
  `ups_open`. `ups_open` calls `usb_init`, `usb_find_busses` and
  `usb_find_devices` again.
- **Observed.** `ups_list`, called every 5 s, sets the slot handle to NULL for
  devices that are not online, without `usb_close`.
- **Inferred.** That leaks the open file descriptor of a device that went away.
- **Observed.** The `ups[]` table keeps `usb_device*` pointers across
  `usb_find_devices` calls.
- **Inferred.** libusb-0.1 frees removed devices on the next scan, so a
  replug can leave dangling pointers. This is a possible crash cause.

## Java side (`monitor/viewpowermonitor.jar`)

- **Observed.** `USBCommEx` registers these USB IDs: `0665:5161`, `2e66:0100`,
  `2e66:0101`, `2e66:0102`, `2e28:0001`, `10af:0002`, `09ae:3028`.
- **Observed.** `USBCommEx.sendCommand` sleeps 1 s after every command, inside a lock
  shared by all USB devices.
- **Observed.** `USBHandler.excuteCommand` appends CR and retries up to 2 more times
  on an empty reply or `(NAK`.
- **Observed.** `P33.matchProtocol` contains
  `while (ioPhase22 == null || "" || "(NAK") ioPhase22 = excuteSimpleCommand("QMD")`.
  The loop has no exit.
- **Inferred.** A device that answers `QPI` with `PI33`/`PI39` and then
  never answers `QMD` correctly freezes that thread forever.

### PMV dialect (ported to `internal/megatec`)

- **Observed.** Detection: `M` → `V` = PMV (rejected if `QPI` returns a `PI` id), `P`/`T` =
  PMV_Simplify, `H` = PMV_H, `I` = PMV_I, `J` = PMV_J. Up to 5 attempts while the reply is `(NAK`.
- **Observed.** Status `QS` is parsed by splitting on spaces. Index 0 is input V (after `(`),
  1 input fault V, 2 output V, 3 load %, 4 frequency, 5 battery V, last is the
  status bits. ViewPower ignores the temperature field for PMV.
- **Observed.** Status bits by character index: 0 utility fail, 1 battery low, 3 UPS failed,
  4 line-interactive, 5 self test, 7 beeper on. vpowerd also decodes 2
  (bypass/boost) and 6 (shutdown active) from the Megatec spec.
- **Observed.** Work mode: output < 20 V → standby (unless failed); else no utility fail
  and no test → line; test → battery test; utility fail → battery; failed bit
  always wins → fault.
- **Observed.** Charge for `V` (`calculateBatteryCapacity`): the battery voltage is first
  scaled to one 12 V block (÷2, ÷3, ÷4 for 24/36/48 V). On line: <11.6 → 0,
  ≥13.5 → 100, 13.3–13.5 → starts at 90 and climbs 1% every 12 min, else
  `90·(v−11.6)/1.7`. On battery: load <20% → `100·(v−10.2)/3.0` capped at 13.2 V,
  else `100·(v−10.2)/2.5` capped at 12.7 V. The state is static, so it is shared
  by every UPS.
- **Observed.** `QI` for H/I/J: field 0 is charge %, field 1 is runtime. The runtime is
  divided by 60 for display, so it is in seconds.
- **Observed.** Commands: `Q` toggles the beeper, `T` runs a 10 s test, `CT` cancels the
  test, `C` cancels a shutdown, and `S<n>R<mmmm>` shuts down. `n` is `.2`-style
  tenths under 1 min, else 2 digits. `mmmm` is the restore delay in minutes.

## Design choices in vpowerd that are not in ViewPower

- Per-cell battery voltage (< 3 V) is multiplied by 6 before the estimate.
  ViewPower would report 0% for such values.
- A plain Megatec fallback (`Q1`) for devices that do not answer `M`.
- Replies are read until CR, not until timeout, so there is no 1 s sleep per command.
- The charge estimator state is per device, not static.

## Test unit

A Tecnoware 800VA line-interactive UPS, USB `0665:5161` ("INNO TECH USB to
Serial"), single 12 V battery. Two sources of evidence: read-only HTTP GETs
to the vendor web UI while ViewPower ran (ViewPowerHTML V1.04-25300), then
vpowerd debug logs after ViewPower was stopped.

### Through the ViewPower web UI

- **Observed.** `GET /ViewPower/initDeviceTree` lists the device as
  `USB (id=<usb id>_PMV)`; `GET /ViewPower/monitoringWorkStatus` returns
  `protocolType: PMV`. So the unit answers `V` to `M`.
- **Observed.** Values: line-interactive, 1/1 phase, line mode, input 242.1 V,
  output 236.5 V at 49.8 Hz, load `016` %, battery 13.8 V, beeper on.
- **Observed.** First read: `batteryCapacity: 0`. A few minutes later:
  `batteryCapacity: 40`, with `batteryCapacitys = [0,0,0,0,0,100,100,100,100,100]`.
- **Observed.** In `WorkInfo.setBatteryCapacity` (viewpowerRMI.jar), the shown
  charge is the integer mean of the last 10 estimates. The mean is computed
  before the new value is shifted in.
- **Inferred.** The per-sample estimate was 100%, the same as the ported
  formula. The 0% was the smoothing window starting from zeros, likely after a
  restart of the service. vpowerd keeps the 10-sample mean but seeds it with
  the first sample (`chargeSmoother`).

### Direct replies to vpowerd

| Command | Reply |
|---|---|
| `M` | `V` |
| `QPI` | `QPI` (echo: unknown command) |
| `F` | `#230.0 005 12.00 50.0` |
| `I` | `I` (echo) |
| `QID` | `QID` (echo) |
| `QS` | `(240.2 240.2 236.5 016 49.8 13.8 --.- 00001001` |

- **Observed.** At boot the kernel binds `hid-generic` and creates a hidraw
  node. ViewPower's libusb code then detaches it: the interface shows driver
  `usbfs` and the hidraw node disappears. After ViewPower stops, writing the
  interface name (e.g. `2-1:1.0`) to `/sys/bus/usb/drivers/usbhid/bind`
  brings it back.
- **Observed.** While ViewPower ran, the kernel logged
  `usbfs: USBDEVFS_CONTROL failed ... rqt 33 rq 9 len 8 ret -110`. That is the
  `0x21/0x09` SET_REPORT found in `ups_write`, timing out.
- **Observed.** ViewPower memory: 246 MB (monitor) + 187 MB (Tomcat) RSS after
  21 minutes. vpowerd: under 10 MB.
- **Observed.** Writing 9 byte reports to hidraw and reading interrupt reports
  works: every exchange above went through hidraw. `QS` takes about 320 ms.
- **Inferred.** Runtime is not reported (PMV has no `QI`), so vpowerd does not
  create the runtime entity.
- **Observed.** The vendor's `upsMonitor.service` uses `KillMode=process`.
  Stopping it leaves the Tomcat JVM running.

## Unknowns
- **Unknown.** Whether `R0000` means "restore now" or "never" on this firmware.
- **Unknown.** The `QPI` family (`P00`…`P99`) and `PMV_Simplify` are not ported.
  Their processors exist in `monitor/processor/` of the decompiled jar.
