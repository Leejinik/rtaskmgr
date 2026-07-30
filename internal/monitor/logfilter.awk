# rtaskmgr log date-range filter (rtm-logfilter).
#
# Emits the lines of one log file that fall in [S, E], where S and E are numeric
# YYYYMMDDHHMMSS keys computed ON THE HOST (never on the client — a date picker and
# a host running TZ=UTC disagree by hours).
#
#   awk -v S=20260630000000 -v E=0 -v Y=2026 -v MAXPRE=5000 -f rtm-logfilter <file>
#
#   E=0 means "to the end". Y seeds the year for the formats that do not carry one.
#
# exit 0 = emitted something
# exit 3 = no recognisable timestamp anywhere (the file is undatable)
# exit 4 = timestamps found but none in range
# A stats line goes to stderr so the caller can prove what happened:
#   RTM_STAT lines=N dated=N emitted=N first=K last=K pretrunc=N tzseen=±HHMM
#
# ---------------------------------------------------------------------------
# THE RULE THAT MATTERS: STICKY-ON.
#
# Printing turns ON at the first recognised timestamp inside the range and only
# ever turns OFF at a recognised timestamp past the end. A line whose shape we do
# not recognise INHERITS the current state — it is never used to switch printing
# off. So an unknown line shape makes the output over-inclusive (harmless) instead
# of deleting content (which destroys the investigation).
#
# This is not hypothetical. mariadb.err interleaves four different timestamp shapes
# during a Galera SST: with a naive per-line test the mariabackup and WSREP_SST
# lines inherit the pre-midnight timestamp and vanish, so a collection for
# "from 06-30" keeps "failed with Broken pipe" and silently deletes the
# "rsync returned code 12" that explains it. Same for a Kafka broker death: the
# JVM's untimestamped OOM prologue and the hs_err_pid pointer are exactly what the
# logs were collected for.
#
# Two more consequences of the same principle:
#  - A timestamp EARLIER than the range never turns printing off once it is on.
#    mariadb.err is not monotonic (a separate SST process appends to it), so
#    treating an out-of-order line as "we are past the range" would truncate.
#  - Lines before the first timestamp (a rotated file always starts mid-entry, and
#    kafkaServer.out starts with a JVM banner) are buffered and flushed if that
#    first timestamp turns out to be in range.
#
# The comparison key is built POSITIONALLY from digits and turned into a number.
# It is never built by deleting punctuation: "2026-07-28T14:03:12.001+0900" with
# the separators stripped yields "20260728T140312", which awk coerces to 20260728
# and compares wrongly against a second-granularity boundary.
# ---------------------------------------------------------------------------

BEGIN {
    if (MAXPRE == 0) MAXPRE = 5000
    mn["Jan"]=1; mn["Feb"]=2; mn["Mar"]=3; mn["Apr"]=4;  mn["May"]=5;  mn["Jun"]=6
    mn["Jul"]=7; mn["Aug"]=8; mn["Sep"]=9; mn["Oct"]=10; mn["Nov"]=11; mn["Dec"]=12
    on = 0; anchored = 0; emitted = 0; lines = 0; nb = 0; pretrunc = 0
    first = 0; last = 0; flushed = 0; prevmo = 0; yr = Y + 0
}

# num builds the comparable key. Fields arrive as digit substrings; +0 makes them
# numbers, so no string coercion or separator handling is involved.
function num(y, mo, d, h, mi, s) {
    return (y + 0) * 10000000000 + (mo + 0) * 100000000 + (d + 0) * 1000000 \
         + (h + 0) * 10000 + (mi + 0) * 100 + (s + 0)
}

# mark applies the sticky-on rule for one recognised timestamp.
function mark(k) {
    anchored++
    if (first == 0) first = k
    last = k
    if (k >= S && (E == 0 || k <= E)) {
        on = 1
    } else if (E != 0 && k > E) {
        on = 0
    }
    # k < S while already on: leave `on` alone (see the non-monotonic note above).

    if (!flushed) {
        flushed = 1
        if (on) { for (i = 1; i <= nb; i++) print buf[i]; emitted += nb }
        nb = 0
    }
    if (on) { print; emitted++ }
    return
}

# yearFor reconstructs the year for the formats that omit it. Y seeds it and the
# year advances whenever the month rolls backwards, which is how a file that spans
# New Year is handled (Dec 31 → Jan 1).
function yearFor(mo) {
    if (prevmo != 0 && mo + 0 < prevmo - 6) yr = yr + 1
    prevmo = mo + 0
    return yr
}

{ lines++ }

# --- shapes that carry a year -------------------------------------------------

# ISO, optionally bracketed, space or T separated. Covers log4j/log4j2
# ("[2026-07-28 14:03:11,412]"), plain ISO rsyslog, zookeeper, mariadb, and the
# JVM's unified logging ("[2026-07-28T14:03:11.412+0900][info][gc]").
/^\[?[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9][ T][0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    p = (substr($0, 1, 1) == "[") ? 2 : 1
    if (tzseen == "" ) {
        if (match($0, /[+-][0-9][0-9]:?[0-9][0-9]\]/) > 0) tzseen = substr($0, RSTART, RLENGTH - 1)
    }
    mark(num(substr($0,p,4), substr($0,p+5,2), substr($0,p+8,2), \
             substr($0,p+11,2), substr($0,p+14,2), substr($0,p+17,2)))
    next
}

# ClickHouse: dots in the date. "2026.07.28 14:03:11.123456"
/^[0-9][0-9][0-9][0-9]\.[0-9][0-9]\.[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    mark(num(substr($0,1,4), substr($0,6,2), substr($0,9,2), \
             substr($0,12,2), substr($0,15,2), substr($0,18,2)))
    next
}

# mariabackup progress lines inside mariadb.err: "[00] 2026-06-30 00:01:13 …"
/^\[[0-9][0-9]\] [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    mark(num(substr($0,6,4), substr($0,11,2), substr($0,14,2), \
             substr($0,17,2), substr($0,20,2), substr($0,23,2)))
    next
}

# WSREP_SST script output: the timestamp is in TRAILING parentheses.
# "WSREP_SST: [ERROR] rsync returned code 12 (20260630 00:04:59.001)"
/\([0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    if (match($0, /\([0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/) > 0) {
        q = RSTART + 1
        mark(num(substr($0,q,4), substr($0,q+4,2), substr($0,q+6,2), \
                 substr($0,q+9,2), substr($0,q+12,2), substr($0,q+15,2)))
        next
    }
}

# Redis / Sentinel: "31337:M 28 Jul 2026 14:03:11.123 * …" — day before month.
/^[0-9]+:[A-Za-z] [ 0-9][0-9] [A-Z][a-z][a-z] [0-9][0-9][0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    if (match($0, /[ 0-9][0-9] [A-Z][a-z][a-z] [0-9][0-9][0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/) > 0) {
        q = RSTART
        mo = mn[substr($0, q+3, 3)]
        if (mo != "") {
            mark(num(substr($0,q+7,4), mo, substr($0,q,2), \
                     substr($0,q+12,2), substr($0,q+15,2), substr($0,q+18,2)))
            next
        }
    }
}

# --- shapes with NO year ------------------------------------------------------

# rsyslog traditional: "Jul 28 14:03:11" (the day is space-padded: "Jul  3").
/^[A-Z][a-z][a-z] [ 0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    mo = mn[substr($0,1,3)]
    if (mo != "") {
        mark(num(yearFor(mo), mo, substr($0,5,2), \
                 substr($0,8,2), substr($0,11,2), substr($0,14,2)))
        next
    }
}

# liz log4j2 FILE_LOG_PATTERN is "%d{MM-dd HH:mm:ss}": "07-28 14:03:11".
/^[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]/ {
    mo = substr($0,1,2)
    mark(num(yearFor(mo), mo, substr($0,4,2), \
             substr($0,7,2), substr($0,10,2), substr($0,13,2)))
    next
}

# --- everything else inherits the current state -------------------------------

# Before the first recognised timestamp: buffer, so a file that starts mid-entry
# (every size-rotated file, and kafkaServer.out's JVM banner) does not lose its head.
!flushed {
    if (nb < MAXPRE) buf[++nb] = $0
    else pretrunc++
    next
}

on { print; emitted++ }

END {
    printf("RTM_STAT lines=%d dated=%d emitted=%d first=%d last=%d pretrunc=%d tzseen=%s\n", \
           lines, anchored, emitted, first, last, pretrunc, (tzseen == "" ? "-" : tzseen)) > "/dev/stderr"
    if (anchored == 0) exit 3
    if (emitted == 0) exit 4
}
