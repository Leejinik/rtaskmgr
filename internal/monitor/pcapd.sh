#!/bin/bash
# rtaskmgr remote packet-capture wrapper (rtm-pcapd).
#
# Runs as root under setsid, fully detached from the SSH channel, and OWNS the
# tcpdump child: every exit path (deadline / maxsize / maxpackets / low-disk /
# user stop / tcpdump failure) closes the savefile cleanly, records the reason in
# <pcap>.done and hands the files to the login user. NEVER signal tcpdump from
# outside this wrapper: that skips the finalization and orphans the capture.
#
# usage: rtm-pcapd -o PCAP -i IF -m MAXSEC -b MAXBYTES -d MINFREEMB -n UID
#                  [-N USER] [-s SNAP] [-c MAXPKT] [-p] [-f] -- [bpf words...]
set -u                 # set -e is deliberately absent: a benign non-zero command
umask 0077             #   inside the poll loop must not kill the wrapper.
set -C                 # noclobber => '>' uses O_CREAT|O_EXCL, which FAILS on a
export LC_ALL=C        #   symlink. Keeps counter/df output parseable, too.

[ "${1:-}" = --probe ] && exit 0
die2(){ echo "rtm-pcapd: bad args" >&2; exit 2; }
n2(){ [ "$1" -ge 2 ] || die2; }

PCAP=; IF=any; MAXSEC=600; MAXB=0; MINFREE=1024; UID_=; USER_=; SNAP=0; MAXPKT=0; PROMISC=0; FLUSH=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o) n2 $#; PCAP=$2; shift 2;;   -i) n2 $#; IF=$2;      shift 2;;
    -m) n2 $#; MAXSEC=$2; shift 2;; -b) n2 $#; MAXB=$2;    shift 2;;
    -d) n2 $#; MINFREE=$2; shift 2;; -n) n2 $#; UID_=$2;   shift 2;;
    -N) n2 $#; USER_=$2; shift 2;;  -s) n2 $#; SNAP=$2;    shift 2;;
    -c) n2 $#; MAXPKT=$2; shift 2;; -p) PROMISC=1; shift;;
    -f) FLUSH=1; shift;;            --) shift; break;;
    *)  die2;;                      # an unknown arg must never be skipped: a
  esac                              #   word-split path would shift forever
done
[ -n "$PCAP" ] && [ -n "$UID_" ] || die2
case "$PCAP" in /*) ;; *) die2;; esac
case "$UID_" in ''|*[!0-9]*) die2;; esac

LOG="$PCAP.log"; DONE="$PCAP.done"; PIDF="$PCAP.pid"; TPIDF="$PCAP.tpid"
STOPF="$PCAP.stop"; STARTF="$PCAP.started"; DIR=${PCAP%/*}
SELFDIR="${RTM_SELFDIR:-}"; TP=; TD=; reason=done; dffail=0

# The sudo command line is visible in `ps` to every local user, so the capture
# path is predictable. Claim every sidecar name up-front under noclobber; if one
# already exists the path was pre-empted -> refuse rather than follow a symlink.
for p in "$PCAP" "$LOG" "$DONE" "$PIDF" "$TPIDF" "$STOPF" "$STARTF"; do
  if [ -e "$p" ] || [ -L "$p" ]; then echo "rtm-pcapd: pre-existing $p" >&2; exit 3; fi
done
: > "$LOG"   || exit 3
: > "$PIDF"  || exit 3
: > "$TPIDF" || exit 3
echo $$ >| "$PIDF"
# Hand the sidecars over IMMEDIATELY, not at the end. umask 0077 makes everything
# root writes root-owned 0600, and the client reads this directory as the LOGIN
# user with no sudo (that is the whole point of the -Z ownership model). Without
# this, `cat <pcap>.pid <pcap>.tpid` fails with EACCES, the liveness check sees no
# pids at all, and a perfectly healthy capture is reported as "interrupted" while
# its file keeps growing. Ownership is on the inode, so the later `>|` writes to
# these same files keep it.
chown -h "$UID_" "$LOG" "$PIDF" "$TPIDF" 2>/dev/null

hand_over(){
  chown -h "$UID_" "$PCAP" "$LOG" "$STARTF" 2>/dev/null
  chmod 0600 "$PCAP" "$LOG" 2>/dev/null
}
finish(){
  trap '' TERM INT HUP                 # non-reentrant: a second TERM must not
  g=${1:-25}                           #   re-enter and exit early mid-flush
  if [ -n "$TP" ] && kill -0 "$TP" 2>/dev/null; then
    # $TP is `timeout`, not tcpdump. Signal the PROCESS GROUP so the real child
    # dies too; killing only $TP would leave tcpdump orphaned AND remove its own
    # time backstop. The plain-pid fallback covers hosts where timeout did not
    # become a group leader (it forwards signals to its child itself).
    kill -TERM -- -"$TP" 2>/dev/null || kill -TERM "$TP" 2>/dev/null
    i=0; while [ "$i" -lt "$g" ] && kill -0 "$TP" 2>/dev/null; do sleep 0.2; i=$((i+1)); done
    if kill -0 "$TP" 2>/dev/null; then
      kill -KILL -- -"$TP" 2>/dev/null || kill -KILL "$TP" 2>/dev/null
      [ -n "$TD" ] && kill -KILL "$TD" 2>/dev/null
      [ "$reason" = signal ] && reason=signal-forced
    fi
  fi
  wait "$TP" 2>/dev/null
  # Never claim a clean stop while something still holds the savefile. This only
  # REPORTS (reason=orphan); it never signals a process we did not start.
  still=0
  for e in /proc/[0-9]*; do
    [ -r "$e/comm" ] || continue
    read -r k < "$e/comm" 2>/dev/null || continue
    [ "$k" = tcpdump ] || continue
    c=$(tr '\0' ' ' < "$e/cmdline" 2>/dev/null) || continue
    case "$c" in *"$PCAP"*) still=1; break;; esac
  done
  [ "$still" = 1 ] && reason=orphan
  printf '%s\n' "$reason" > "$DONE" 2>/dev/null
  hand_over; chown -h "$UID_" "$DONE" 2>/dev/null; chmod 0600 "$DONE" 2>/dev/null
  [ "$reason" = orphan ] || rm -f "$PIDF" "$TPIDF" "$STOPF" 2>/dev/null
  [ -n "$SELFDIR" ] && rm -rf "$SELFDIR" 2>/dev/null
  exit 0
}
trap 'reason=signal; finish' TERM INT HUP

TDB=$(command -v tcpdump 2>/dev/null)
[ -n "$TDB" ] || for p in /usr/sbin/tcpdump /sbin/tcpdump /usr/bin/tcpdump; do
  [ -x "$p" ] && { TDB=$p; break; }
done
if [ -z "$TDB" ]; then
  echo "tcpdump: not found (PATH, /usr/sbin, /sbin, /usr/bin)" >| "$LOG"
  reason=no-tcpdump; finish        # no 0-byte pcap is created
fi

# -nn: never resolve names or ports. -s is always explicit, including "-s 0" for a
# full packet: tcpdump 4.x defaults to a 262144 snaplen, but an older build on some
# host would silently truncate at 68 bytes and hand over a useless capture.
ARGS=(-i "$IF" -w "$PCAP" -nn -B 4096 -s "$SNAP")
[ "$PROMISC" = 0 ]  && ARGS+=(-p)
[ "$MAXPKT" -gt 0 ] && ARGS+=(-c "$MAXPKT")
[ "$FLUSH" = 1 ]    && ARGS+=(-U)
[ -n "$USER_" ]     && ARGS+=(-Z "$USER_")

# Two backstops that survive this wrapper being SIGKILLed:
#   ulimit -f : kernel-enforced byte cap (SIGXFSZ, rc 153). -c 0 so the kernel
#               does not answer a full disk with a multi-GB core dump.
#   timeout   : an independent time cap in its own process.
HARDKB=$(( (MAXB > 0 ? MAXB : 8589934592) / 1024 * 2 ))
( ulimit -c 0; ulimit -f "$HARDKB"
  exec timeout -s TERM -k 10 "$((MAXSEC + 30))" "$TDB" "${ARGS[@]}" "$@" ) >/dev/null 2>|"$LOG" &
TP=$!

# Positive start signal. tcpdump opens the device and compiles/sets the BPF
# BEFORE opening the savefile, so "$PCAP exists" proves both succeeded. Checking
# child liveness in the same loop turns a bad NIC/filter into a sub-second
# failure instead of a silent "running" the operator waits 30 minutes on.
i=0
while [ "$i" -lt 50 ]; do
  [ -z "$TD" ] && TD=$(pgrep -P "$TP" 2>/dev/null | head -1)
  if [ -e "$PCAP" ]; then
    printf '%s %s\n' "$TP" "$TD" >| "$TPIDF"
    : > "$STARTF" && chown -h "$UID_" "$STARTF" 2>/dev/null
    break
  fi
  kill -0 "$TP" 2>/dev/null || { wait "$TP"; reason=error; finish 1; }
  sleep 0.1; i=$((i+1))
done
[ -e "$STARTF" ] || { echo "tcpdump: savefile not opened within 5s" >> "$LOG"; reason=error; finish 5; }
hand_over

END=$(( $(date +%s) + MAXSEC )); prev=0; tick=0
while kill -0 "$TP" 2>/dev/null; do
  sleep 0.5 & SP=$!; wait "$SP"        # `wait` so the TERM trap fires at once;
  [ -e "$STOPF" ] && { reason=signal; finish; }   # .stop latency <= 0.5s
  tick=$((tick+1)); [ $((tick % 4)) -ne 0 ] && continue      # heavy checks every 2s
  [ "$(date +%s)" -ge "$END" ] && { reason=deadline; finish; }
  # timeout-wrap df/stat: a hung NFS mount would otherwise wedge this loop in D
  # state, where signals are not delivered -> deadline and .stop both dead.
  sz=$(timeout 5 stat -c%s "$PCAP" 2>/dev/null)
  case "${sz:-}" in ''|*[!0-9]*) sz=$prev;; esac
  [ "$MAXB" -gt 0 ] && [ "$sz" -ge "$MAXB" ] && { reason=maxsize; finish; }
  rate=$(( (sz - prev) / 2 )); [ "$rate" -lt 0 ] && rate=0; prev=$sz
  free=$(timeout 5 df -P -B1M "$DIR" 2>/dev/null | awk 'NR==2{print $4}')
  case "${free:-}" in
    ''|*[!0-9]*) dffail=$((dffail+1)); [ "$dffail" -ge 3 ] && { reason=low-disk; finish 5; }; continue;;
    *) dffail=0;;
  esac
  # Adaptive: a static floor is not a floor. 1GbE writes ~875MB during the 2s
  # poll plus the 5s TERM grace, which is most of the default 1024MB headroom.
  need=$(( MINFREE + rate * 12 / 1048576 ))
  [ "$free" -lt "$need" ] && { reason=low-disk; finish 5; }   # short grace: a
done                                                          #   truncated pcap
wait "$TP"; rc=$?                                             #   beats a full fs
case "$rc" in
  0)   reason=done
       [ "$MAXPKT" -gt 0 ] && grep -qE "^[[:space:]]*$MAXPKT packets captured" "$LOG" 2>/dev/null && reason=maxpackets;;
  124) reason=deadline;;
  153) reason=maxsize;;             # 128 + SIGXFSZ = the ulimit hard cap fired
  *)   reason=error;;
esac
finish
