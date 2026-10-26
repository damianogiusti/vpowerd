#!/bin/sh
# vpowerd installer for Alpine (OpenRC) and Debian/Ubuntu (systemd).
#
#   wget -qO- https://raw.githubusercontent.com/damianogiusti/vpowerd/main/install.sh | sh
#
# Run it again to upgrade: the configuration is kept.
#
# The binaries come from the "edge" release, rebuilt by CI on every push to
# main. Every file is checked against its checksums.txt before install.
#
# Environment options:
#   VPOWERD_NO_START   set to 1 to install without enabling and starting
#   VPOWERD_BASE_URL   download the assets from another URL, or install
#                      offline from a local directory (absolute path)

# The whole script is a function called on the last line, so a download that
# stops half way through never runs a partial script.
main() {
	set -eu

	repo="damianogiusti/vpowerd"
	bin_path=/usr/local/bin/vpowerd

	say() { printf 'vpowerd: %s\n' "$*"; }
	die() {
		printf 'vpowerd: error: %s\n' "$*" >&2
		exit 1
	}

	[ "$(id -u)" -eq 0 ] || die "run as root"

	case "$(uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported architecture $(uname -m)" ;;
	esac

	base=${VPOWERD_BASE_URL:-"https://github.com/$repo/releases/download/edge"}
	case "$base" in
	/*) fetch() { cp "$1" "$2"; } ;; # offline install from a local directory
	*)
		if command -v wget >/dev/null 2>&1; then
			fetch() { wget -qO "$2" "$1"; }
		elif command -v curl >/dev/null 2>&1; then
			fetch() { curl -fsSL -o "$2" "$1"; }
		else
			die "wget or curl is required"
		fi
		;;
	esac
	command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

	if [ -d /run/systemd/system ]; then
		init=systemd
		files="vpowerd-linux-$arch vpowerd.service vpowerd.env.example 99-vpowerd.rules"
	elif command -v openrc-run >/dev/null 2>&1; then
		init=openrc
		files="vpowerd-linux-$arch vpowerd.openrc vpowerd.env.example"
	else
		die "no supported init system (OpenRC or systemd) found"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	say "downloading from $base"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "cannot download checksums.txt"
	for f in $files; do
		fetch "$base/$f" "$tmp/$f" || die "cannot download $f"
		expected=$(awk -v f="$f" '$2 == f { print $1 }' "$tmp/checksums.txt")
		[ -n "$expected" ] || die "$f is not in checksums.txt"
		actual=$(sha256sum "$tmp/$f" | cut -d ' ' -f 1)
		[ "$actual" = "$expected" ] || die "checksum mismatch for $f"
	done
	say "checksums verified"

	install -m 0755 "$tmp/vpowerd-linux-$arch" "$bin_path"
	say "installed $bin_path $("$bin_path" -version)"

	if [ "$init" = openrc ]; then
		if ! id vpowerd >/dev/null 2>&1; then
			grep -q '^dialout:' /etc/group || addgroup -S -g 20 dialout
			adduser -S -D -H -h /var/lib/vpowerd -s /sbin/nologin -G dialout vpowerd
			say "created user vpowerd (group dialout)"
		fi
		install -m 0755 "$tmp/vpowerd.openrc" /etc/init.d/vpowerd
		conf=/etc/conf.d/vpowerd
		if [ ! -f "$conf" ]; then
			# OpenRC sources conf.d as shell: variables must be exported.
			sed -e 's/^\([A-Z]\)/export \1/' -e 's/^#\([A-Z]\)/#export \1/' \
				"$tmp/vpowerd.env.example" >"$conf"
			chmod 0640 "$conf"
			say "created $conf"
		fi
	else
		install -m 0644 "$tmp/vpowerd.service" /etc/systemd/system/vpowerd.service
		if [ -d /etc/udev/rules.d ]; then
			install -m 0644 "$tmp/99-vpowerd.rules" /etc/udev/rules.d/99-vpowerd.rules
			udevadm control --reload 2>/dev/null || true
			udevadm trigger --subsystem-match=hidraw 2>/dev/null || true
		fi
		conf=/etc/vpowerd.env
		if [ ! -f "$conf" ]; then
			install -m 0640 "$tmp/vpowerd.env.example" "$conf"
			say "created $conf"
		fi
		systemctl daemon-reload
	fi

	device_seen=0
	for d in /dev/hidraw* /dev/ups /dev/ttyUSB*; do
		[ -e "$d" ] && device_seen=1
	done
	if [ "$device_seen" = 0 ]; then
		say "warning: no UPS device visible (/dev/hidraw*, /dev/ups, /dev/ttyUSB*)."
		say "         In a Proxmox LXC, pass it from the host: pct set <CTID> -dev0 /dev/ups,gid=20,mode=0660"
	fi

	if [ "${VPOWERD_NO_START:-0}" = 1 ]; then
		say "done, not started (VPOWERD_NO_START=1). Config: $conf"
		return 0
	fi

	if [ "$init" = openrc ]; then
		rc-update add vpowerd default >/dev/null
		rc-service vpowerd restart >/dev/null
	else
		systemctl enable vpowerd >/dev/null 2>&1
		systemctl restart vpowerd
	fi

	addr=$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n 1)
	say "running. Web UI: http://${addr:-<this-host>}:8080   Config: $conf"
}

main "$@"
