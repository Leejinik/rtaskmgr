#!/usr/bin/env bash
# kafka-thread-check.sh — Kafka 브로커 점검: ① 스레드별 사용률 ② 파티션별 초당 적재량과 복제 상태
# 조회만 한다. 설정·데이터를 바꾸지 않는다.
#
# 사용법 : 브로커가 도는 서버에서 root 또는 Kafka 실행 계정으로
#            bash kafka-thread-check.sh                 # 20초 측정, 토픽 liz.checkvalue.topic
#            bash kafka-thread-check.sh 60 다른.토픽     # 60초 측정, 토픽 지정
#            bash kafka-thread-check.sh --jstack        # 스레드를 이름으로 나눠 본다(아래 주의)
# 옵션   : --jstack  jstack 으로 스레드 이름을 읽어 네트워크·요청 처리·복제 스레드를 나눠 보여 준다.
#                    jstack 은 브로커 JVM 전체를 잠깐 멈춘다(safepoint, 한 번에 3회). 그래서 기본은 쓰지 않는다.
#                    2026-10-15 시험: 스레드 99개 JVM 에서 정지 한 번 최대 2.4ms, 종료 0회(JDK 8u504·11·17·21).
#                    브로커가 -Xrs(신호 처리 축소)로 떠 있으면 켜도 쓰지 않는다. 시험한 JDK 에서는 문제없었지만
#                    신호로 붙는 jstack 과 맞물리는 옵션이라 확인하지 않은 JDK 를 고려해 보수적으로 건너뛴다.
#                    jstack -F(강제 모드)는 프로세스 전체를 오래 세우므로 어떤 경우에도 쓰지 않는다.
#          --tsv     프로그램이 읽는 형식(탭 구분)으로 출력한다(rtaskmgr 용량 점검이 쓴다).
# 언제   : 수집량이 가장 많은 시간대에, 브로커마다 한 번씩
#
# 읽는 법
#  ① 스레드: 「가장 바쁜 것」이 기준.
#       네트워크 스레드가 높다   → num.network.threads 를 늘릴 근거
#       요청 처리 스레드가 높다 → num.io.threads 를 늘릴 근거
#     --jstack 없이는 Kafka 2.2 이상에서 두 스레드가 같은 이름(data-plane-kafk…)으로 보여 묶어서 보여 준다.
#  ② 파티션: 한 파티션에 쓰는 일은 스레드를 늘려도 빨라지지 않는다(한계에서도 스레드는 각 15% 안팎으로 보인다).
#       파티션당 초당 적재량이 PART_LIMIT 의 PART_PREP% 면 늘릴 준비, PART_MAX% 를 넘거나 복제가 빠지면(ISR 에서 빠짐. 비율보다 먼저 판정) → 파티션을 늘린다
#  둘 다 여유인데 collector 의 kafka queue 가 쌓이면 브로커가 원인이 아니다. Redis 와 collector 의 GC 를 본다.
WARN=${WARN:-50}                 # 스레드 사용률이 이 값 이상이면 「여유 적음」 (제안값)
HIGH=${HIGH:-70}                 # 이 값 이상이면 「증설 검토」 (제안값: Kafka 운영 권고 「유휴 30% 미만이면 과부하」)
PART_LIMIT=${PART_LIMIT:-3000000} # 파티션 1개가 복제를 유지하며 받은 초당 건수. 2026-10-12 AWS 2M 환경 실측(약 42바이트 CSV, gzip). 현장 실측값으로 바꿔 쓴다.
PART_PREP=${PART_PREP:-50}       # 파티션 적재량이 기준값의 이 % 이상이면 「늘릴 준비」 (판단값: 관제 제품이라 여유를 먼저 봄)
PART_MAX=${PART_MAX:-60}         # 이 % 이상이면 「파티션 늘림」 (판단값: 평시 운영 상한)

JSTACK=0; TSV=0; POS=()
for a in "$@"; do
  case "$a" in
    --jstack) JSTACK=1 ;;
    --tsv) TSV=1 ;;
    -h|--help) awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    -*) echo "모르는 옵션: $a (--jstack, --tsv)"; exit 2 ;;
    *) POS+=("$a") ;;
  esac
done
W=${POS[0]:-20}
TOPIC=${POS[1]:-liz.checkvalue.topic}

# 사람이 읽는 줄(say)과 프로그램이 읽는 줄(rec)은 --tsv 로 갈린다.
say(){ [ "$TSV" = 1 ] || printf '%s\n' "$*"; }
rec(){ [ "$TSV" = 1 ] || return 0; local IFS=$'\t'; printf '%s\n' "$*"; }
warn(){ if [ "$TSV" = 1 ]; then printf 'W\t%s\n' "$1"; else echo "  ※ $1"; fi; }
die(){ if [ "$TSV" = 1 ]; then printf 'E\t%s\n' "$1"; else echo "$1"; fi; exit 1; }
# 외부 명령이 멈춰도 점검이 끝나도록 시간 제한을 건다(timeout 이 없는 서버는 그대로 실행).
lim(){ local s=$1; shift; if command -v timeout >/dev/null 2>&1; then timeout "$s" "$@"; else "$@"; fi; }

case "$W" in ''|*[!0-9]*|0) die "측정 시간은 1 이상의 초 단위 숫자여야 합니다: $W" ;; esac

# pgrep -f 는 명령줄 앞 4096바이트만 본다. 클래스패스가 길면 끝에 있는 kafka.Kafka 를 못 찾으므로 /proc 을 직접 본다.
PID=$(grep -alzx 'kafka\.Kafka' /proc/[0-9]*/cmdline 2>/dev/null | head -1 | cut -d/ -f3)
[ -z "$PID" ] && die "Kafka 브로커 프로세스를 찾지 못했습니다."
[ -r /proc/$PID/task ] || die "프로세스 정보를 읽을 권한이 없습니다. root 또는 Kafka 실행 계정으로 실행하세요."
ARGV=$(tr '\0' '\n' < /proc/$PID/cmdline)
JEXE=$(readlink -f /proc/$PID/exe 2>/dev/null)
JBIN=""; [ -n "$JEXE" ] && JBIN=$(dirname "$JEXE")
# Kafka 도구(kafka-topics.sh 등)는 JAVA_HOME 이나 PATH 의 java 로 뜬다. 둘 다 없는 서버가 있어 브로커의 java 를 쓴다.
if [ -n "$JBIN" ] && ! { [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/java" ]; }; then export JAVA_HOME=$(dirname "$JBIN"); fi
# 셸에 JMX_PORT 가 잡혀 있으면 도구 JVM 이 브로커와 같은 포트를 열려다 실패한다.
unset JMX_PORT KAFKA_JMX_OPTS
TMP=$(mktemp -d /tmp/kafka-thread-check.XXXXXX) || exit 1
trap 'rm -rf "$TMP"' EXIT
HZ=$(getconf CLK_TCK 2>/dev/null || echo 100)
CONF=$(printf '%s\n' "$ARGV" | grep -m1 'server.*\.properties$')
cfg(){ [ -r "$CONF" ] && grep -E "^$1=" "$CONF" | tail -1 | cut -d= -f2-; }
KBIN=$(cd "$(dirname "$CONF")/../bin" 2>/dev/null && pwd)
PORT=$(cfg listeners | sed -E 's#^[^:]*://[^:]*:([0-9]+).*#\1#'); BS=localhost:${PORT:-9092}

# ---- 스레드 이름표 ----
# 기본(short): /proc/<pid>/task/*/comm. JVM 이 OS 에 알려 주는 이름이라 15자에서 잘린다.
# --jstack(full): jstack 의 전체 이름. 브로커가 잠깐 멈추므로 옵션이다.
MODE=short; WHY="jstack 을 쓰지 않았습니다(기본). 스레드를 이름으로 나누려면 --jstack"; JMS=0
if [ "$JSTACK" = 1 ]; then
  WHY=""
  if printf '%s\n' "$ARGV" | grep -qx -- '-Xrs'; then
    WHY="브로커가 -Xrs(신호 처리 축소)로 떠 있어 jstack 을 쓰지 않았습니다(보수적 기본값)"
  elif printf '%s\n' "$ARGV" | grep -qx -- '-XX:+DisableAttachMechanism'; then
    WHY="브로커가 JVM 연결을 막아 두어(-XX:+DisableAttachMechanism) jstack 을 쓰지 않았습니다"
  else
    JST=""
    for c in "$JBIN/jstack" "$JBIN/../../bin/jstack" "${JAVA_HOME:-}/bin/jstack"; do
      [ -n "$JBIN" ] && [ -x "$c" ] && { JST=$c; break; }
    done
    OWN=$(stat -c %U /proc/$PID 2>/dev/null); RUNAS=()
    if [ -z "$JST" ]; then
      WHY="jstack 이 없습니다(JDK 가 아니라 JRE 만 설치된 서버)"
    elif [ "$OWN" != "$(id -un)" ]; then
      # jstack 은 브로커와 같은 계정이어야 붙는다. root 면 그 계정으로 바꿔 실행한다.
      if [ "$(id -u)" = 0 ] && command -v runuser >/dev/null 2>&1; then RUNAS=(runuser -u "$OWN" --)
      else WHY="브로커 실행 계정($OWN)이 아니어서 jstack 을 쓸 수 없습니다"; fi
    fi
    if [ -z "$WHY" ]; then
      J0=$(date +%s%N)
      if lim 30 "${RUNAS[@]}" "$JST" "$PID" > "$TMP/js" 2>/dev/null && grep -q 'nid=0x' "$TMP/js"; then
        JMS=$(( ($(date +%s%N) - J0) / 1000000 ))
        sed -n -E 's/^"([^"]*)".*nid=(0x[0-9a-f]+).*/\2 \1/p' "$TMP/js" | while read -r hex name; do echo "$((hex)) $name"; done > "$TMP/map"
        MODE=full
      else
        WHY="jstack 이 실패했습니다"
      fi
    fi
  fi
fi
if [ "$MODE" = short ]; then
  for t in /proc/$PID/task/*; do echo "${t##*/} $(cat "$t/comm" 2>/dev/null)"; done > "$TMP/map"
fi

snap(){ awk '{n=split(FILENAME,a,"/"); sub(/.*\) /,""); print a[n-1], $12+$13}' /proc/$PID/task/*/stat 2>/dev/null; }
# jstat 은 JVM 이 원래 열어 두는 성능 카운터 파일을 읽기만 한다(JVM 에 붙지 않고 멈추게 하지 않는다).
gct(){ [ -n "$JBIN" ] && [ -x "$JBIN/jstat" ] && lim 15 "$JBIN/jstat" -gcutil "$PID" 2>/dev/null | tail -1 | awk '$NF ~ /^[0-9.]+$/ {print $NF}'; }
# kafka-get-offsets.sh 는 Kafka 3.0 부터 있다. 그 전 버전(2.7 등)은 GetOffsetShell 을 직접 부른다.
offs(){ if [ -x "$KBIN/kafka-get-offsets.sh" ]; then lim 60 "$KBIN/kafka-get-offsets.sh" --bootstrap-server $BS --topic "$TOPIC" 2>/dev/null
        elif [ -x "$KBIN/kafka-run-class.sh" ]; then lim 60 "$KBIN/kafka-run-class.sh" kafka.tools.GetOffsetShell --broker-list $BS --topic "$TOPIC" --time -1 2>/dev/null; fi | grep -E '^[^:]+:[0-9]+:[0-9]+$' | sort > "$1"; date +%s.%N > "$1.t"; }

offs "$TMP/o1"
G1=$(gct); snap > "$TMP/s1"; T1=$(date +%s.%N)
sleep "$W"
snap > "$TMP/s2"; T2=$(date +%s.%N); G2=$(gct)
offs "$TMP/o2"
DT=$(awk -v a="$T1" -v b="$T2" 'BEGIN{print b-a}')
NET=$(cfg num.network.threads); IOT=$(cfg num.io.threads); HEAP=$(printf '%s\n' "$ARGV" | grep -m1 '^-Xmx'); CORES=$(nproc)

say "Kafka 브로커 점검 — $(hostname), $(date '+%F %T'), 측정 ${W}초, pid $PID"
say "설정: num.network.threads=$NET  num.io.threads=$IOT  힙=$HEAP  코어 ${CORES}개"
rec H "$(hostname)" "$(date '+%F %T')" "$W" "$PID" "$NET" "$IOT" "$HEAP" "$CORES" "$MODE" "$JMS" "$WHY"
say
say "① 스레드 사용률"
# 구분 이름 앞의 숫자는 표의 순서다. short 에서 Kafka 2.2 미만은 이름이 kafka-network-t / kafka-request-h 로 갈려 나눌 수 있다.
awk -v dt="$DT" -v hz="$HZ" -v mode="$MODE" -v warn="$WARN" -v high="$HIGH" -v tsv="$TSV" '
  FILENAME==ARGV[1]{nm[$1]=substr($0,index($0," ")+1); next}
  FILENAME==ARGV[2]{a[$1]=$2; next}
  ($1 in a){ p=($2-a[$1])/hz/dt*100; n=nm[$1]; c="";
    if(mode=="full"){ if(n~/network-thread/)c="1"; else if(n~/request-handler/)c="3"; else if(n~/ReplicaFetcher/)c="4" }
    else { if(n~/^data-plane-kafk/)c="2"; else if(n~/^kafka-network-t/)c="1"; else if(n~/^kafka-request-h/)c="3"; else if(n~/^ReplicaFetcherT/)c="4" }
    all+=p; if(c=="")next;
    if(p>=0.5){cnt[c]++; sum[c]+=p}; tot[c]++; if(p>mx[c])mx[c]=p }
  END{
    lab["1"]="네트워크 스레드"; lab["2"]="네트워크+요청 처리 스레드"; lab["3"]="요청 처리 스레드"; lab["4"]="복제 스레드";
    if(!tsv) printf "  %-24s %6s %12s %8s %8s   %s\n", "구분", "일하는 수", "가장 바쁜 것", "평균", "합계", "판정";
    for(i=1;i<=4;i++){ c=i ""; if(!(c in tot))continue; shown++; v=mx[c];
      j=(c=="4") ? "-" : (v>=high ? "증설 검토" : (v>=warn ? "여유 적음" : "여유 있음"));
      avg=(cnt[c]?sum[c]/cnt[c]:0);
      if(tsv) printf "T\t%s\t%d\t%d\t%.1f\t%.1f\t%.0f\t%s\n", lab[c], cnt[c], tot[c], v, avg, sum[c], j;
      else printf "  %-20s %4d/%-4d %10.1f%% %7.1f%% %7.0f%%   %s\n", lab[c], cnt[c], tot[c], v, avg, sum[c], j }
    if(tsv) printf "C\t%.0f\n", all; else printf "  브로커 전체 CPU: %.0f%% (코어 %.1f개 분량)\n", all, all/100;
    if(!shown){ m="스레드 이름이 OS 에 보이지 않아(JDK 8 등) 스레드별로 나누지 못했습니다. 나누려면 --jstack";
      if(tsv) printf "W\t%s\n", m; else printf "  ※ %s\n", m } }' "$TMP/map" "$TMP/s1" "$TMP/s2"
if [ -n "$G1" ] && [ -n "$G2" ]; then
  GCP=$(awk -v a="$G1" -v b="$G2" -v dt="$DT" 'BEGIN{printf "%.1f", (b-a)/dt*100}')
  say "  GC 로 멈춘 시간: ${GCP}%"; rec G "$GCP"
fi
if [ "$MODE" = short ]; then
  [ "$JSTACK" = 1 ] && warn "$WHY"
  [ "$JSTACK" = 1 ] || say "  ※ $WHY"
else
  say "  ※ jstack 을 썼습니다(${JMS}ms). 그동안 브로커가 잠깐 멈췄습니다."
fi
say
say "② 파티션별 적재량과 복제 상태 — 토픽 $TOPIC"
if [ -s "$TMP/o1" ] && [ -s "$TMP/o2" ]; then
  lim 60 "$KBIN/kafka-topics.sh" --bootstrap-server $BS --describe --topic "$TOPIC" 2>/dev/null | grep 'Partition:' > "$TMP/desc"
  join -t: -1 2 -2 2 <(sort -t: -k2,2 "$TMP/o1") <(sort -t: -k2,2 "$TMP/o2") | awk -F: -v dt="$(awk -v a="$(cat "$TMP/o1.t")" -v b="$(cat "$TMP/o2.t")" 'BEGIN{print b-a}')" -v lim="$PART_LIMIT" -v prep="$PART_PREP" -v pmax="$PART_MAX" -v desc="$TMP/desc" -v tsv="$TSV" -v topic="$TOPIC" '
    BEGIN{ while((getline l < desc)>0){ p=l; sub(/.*Partition: /,"",p); sub(/[^0-9].*/,"",p); ld=l; sub(/.*Leader: /,"",ld); sub(/[^0-9-].*/,"",ld); rp=l; sub(/.*Replicas: /,"",rp); sub(/[ \t].*/,"",rp); is=l; sub(/.*Isr: /,"",is); sub(/[ \t].*/,"",is); L[p]=ld; RP[p]=rp; IS[p]=is }
           if(!tsv) printf "  %-8s %14s %12s %8s %-12s   %s\n", "파티션", "초당 적재량", "한계 대비", "리더", "복제(ISR)", "판정" }
    { p=$1; rate=($5-$3)/dt; pct=rate/lim*100; ok=(RP[p]==IS[p] || length(RP[p])==length(IS[p]));
      j=(!ok ? "파티션 늘림" : (pct>=100 ? "한계" : (pct>=pmax ? "파티션 늘림" : (pct>=prep ? "늘릴 준비" : "여유 있음"))));
      if(tsv) printf "P\t%s\t%.0f\t%.1f\t%s\t%s\t%d\t%s\n", p, rate, pct, L[p], IS[p], ok, j;
      else printf "  %-8s %10.1f만 건 %10.0f%% %8s %-12s   %s\n", p, rate/10000, pct, L[p], IS[p] (ok?"":" (빠짐)"), j; tot+=rate }
    END{ if(tsv) printf "S\t%.0f\t%.0f\t%s\n", tot, lim, topic;
         else printf "  토픽 합계: 초당 %.1f만 건 (한계 기준값: 파티션당 %.0f만 건)\n", tot/10000, lim/10000 }'
else
  M="Kafka 도구(${KBIN:-?})로 토픽을 조회하지 못했습니다. 토픽 이름과 접속 주소($BS)를 확인하세요."
  if [ "$TSV" = 1 ]; then printf 'W\t%s\n' "$M"; else echo "  $M"; fi
fi
say
say "※ 「일하는 수」는 측정 동안 0.5% 이상 쓴 스레드 수 / 전체 수, 평균은 일하는 스레드만의 평균입니다."
say "※ 초당 적재량은 복제까지 끝난 위치(high watermark)가 늘어난 속도입니다."
