#!/usr/bin/env bash
# Redis 처리 상한 점검 — 읽기 전용(INFO 조회만 한다. 설정·데이터는 건드리지 않는다)
#
# 사용법:  bash redis-ceiling-check.sh [--tsv] [포트=5000] [구간 초=20] [횟수=3]
#   Redis 가 떠 있는 서버에서 마스터마다 한 번씩 돌린다. 부하가 가장 높은 시간대에 잰다.
#   비밀번호: REDISCLI_AUTH 환경변수. 없으면 REDIS_CONF(기본 /usr/local/liz/redis/redis.conf)의 requirepass 를 읽는다.
#   redis-cli 위치: REDIS_CLI 환경변수. 없으면 /usr/local/liz/redis/bin/redis-cli,
#                   그것도 없으면 그 포트의 redis-server 와 같은 폴더, 마지막으로 PATH 의 redis-cli.
#   --tsv: 프로그램이 읽는 형식(탭 구분)으로 출력한다(rtaskmgr 용량 점검이 쓴다).
#
# 읽는 법:
#   메인%   Redis 가 명령을 실행하는 스레드 하나의 CPU 사용률. 서버 전체 CPU 가 남아도 이 값이 상한을 정한다.
#   추정상한 지금 들어오는 양 ÷ 메인% × 포화점(SAT, 기본 94%). 지금 구성에서 이 마스터가 받을 수 있는 최대치 추정.
#   여유    추정상한까지 남은 비율.
#   2026-10-06 AWS 2M 환경 실측: 메인% 86~88 은 큐 없이 안정, 94~98 은 경계(더 올리면 쌓임).
#   판정: 90% 미만 = 여유 있음, 90~94% = 여유 적음, 94% 이상 = 상한 도달.
#   추정상한은 부하가 낮을 때 재면 실제보다 조금 낮게 나온다(이 환경에서 5% 안쪽).
TSV=0; POS=()
for a in "$@"; do
  case "$a" in
    --tsv) TSV=1 ;;
    -h|--help) awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    -*) echo "모르는 옵션: $a (--tsv)"; exit 2 ;;
    *) POS+=("$a") ;;
  esac
done
PORT=${POS[0]:-5000}; SEC=${POS[1]:-20}; N=${POS[2]:-3}; SAT=${SAT:-94}
die(){ if [ "$TSV" = 1 ]; then printf 'E\t%s\n' "$1"; else echo "$1"; fi; exit "${2:-1}"; }
for v in "$PORT" "$SEC" "$N"; do case "$v" in ''|*[!0-9]*|0) die "포트·구간·횟수는 1 이상의 숫자여야 합니다: $v" 2 ;; esac; done

CLI=${REDIS_CLI:-/usr/local/liz/redis/bin/redis-cli}
if [ ! -x "$CLI" ]; then
  # 그 포트로 뜬 redis-server 와 같은 폴더의 redis-cli (설치 경로가 다른 서버)
  for p in $(pgrep -x redis-server 2>/dev/null); do
    tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null | grep -Eq ":$PORT( |$)" || continue
    e=$(readlink -f /proc/$p/exe 2>/dev/null); [ -n "$e" ] && [ -x "$(dirname "$e")/redis-cli" ] && CLI=$(dirname "$e")/redis-cli && break
  done
  [ -x "$CLI" ] || CLI=redis-cli
fi
command -v "$CLI" >/dev/null 2>&1 || die "redis-cli 를 찾지 못했습니다. REDIS_CLI 환경변수로 위치를 알려 주세요."
CONF=${REDIS_CONF:-/usr/local/liz/redis/redis.conf}
[ -z "$REDISCLI_AUTH" ] && [ -r "$CONF" ] && export REDISCLI_AUTH=$(awk '$1=="requirepass"{gsub(/"/,"",$2);print $2}' "$CONF")
# redis-cli 가 멈춰도 점검이 끝나도록 시간 제한을 건다.
cli(){ if command -v timeout >/dev/null 2>&1; then timeout 10 "$CLI" -p "$PORT" "$@"; else "$CLI" -p "$PORT" "$@"; fi; }
snap(){ cli info everything 2>/dev/null | tr -d '\r' | awk -F'[:,=]' -v t="$(date +%s.%N)" '
  /^total_commands_processed:/{tc=$2} /^cmdstat_set:/{sc=$3}
  /^used_cpu_user_main_thread:/{u=$2; has=1} /^used_cpu_sys_main_thread:/{s=$2}
  END{printf "%s %.0f %.0f %.6f %d\n", t, tc, sc, u+s, has}'; }
# printf 로 찍는다. print 는 소수를 유효숫자 6자리로 줄여서, 오래 돈 Redis(CPU 누적 수십만 초)에서는
# 메인 스레드 CPU 가 1초 단위로 잘려 20초 구간의 메인% 가 0% 아니면 5% 로만 나왔다(2026-10-15 발견).
I=$(cli info 2>/dev/null | tr -d '\r')
[ -z "$I" ] && die "Redis 에 접속하지 못했다 (포트 $PORT, 비밀번호 확인)"
# 첫 줄만 본다. Redis 7 의 INFO 에는 errorstat_NOAUTH 같은 통계 줄이 있어 전체에서 찾으면 오판한다.
case "${I%%$'\n'*}" in *NOAUTH*|*WRONGPASS*) die "Redis 비밀번호가 맞지 않는다 (포트 $PORT)";; esac
g(){ echo "$I" | awk -F: -v k="$1" '$1==k{print $2}'; }
IOT=$(cli config get io-threads 2>/dev/null | tail -1)
if [ "$TSV" = 1 ]; then
  printf 'H\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(hostname)" "$PORT" "$(g redis_version)" "$(g role)" "$(g connected_slaves)" "$(g repl_backlog_active)" "$IOT"
else
  echo "서버=$(hostname) 포트=$PORT 버전=$(g redis_version) 역할=$(g role) 리플리카수=$(g connected_slaves) 복제버퍼=$(g repl_backlog_active) io-threads=$IOT"
fi
if [ "$(g role)" = master ] && [ "$(g connected_slaves)" = 0 ] && [ "$(g repl_backlog_active)" = 1 ]; then
  M="리플리카가 없는데 복제 버퍼가 남아 있다. 버퍼가 풀릴 때까지(repl-backlog-ttl) 쓰기 한 건이 10~14% 더 비싸다."
  if [ "$TSV" = 1 ]; then printf 'W\t%s\n' "$M"; else echo "주의: $M"; fi
fi
P=$(snap); set -- $P
[ "$5" = 1 ] || die "이 Redis 는 메인 스레드 CPU 를 따로 알려 주지 않는다(6.0 미만). 대신 top -H -p \$(pgrep -f 'redis-server.*:$PORT') 에서 redis-server 스레드의 %CPU 를 본다." 2
[ "$TSV" = 1 ] || printf '%-9s %10s %10s %7s %9s %11s %6s\n' 시각 명령/초 SET/초 메인% 건당µs 추정상한/초 여유
for i in $(seq "$N"); do sleep "$SEC"; C=$(snap)
  echo "$P $C" | awk -v sat="$SAT" -v now="$(date +%T)" -v tsv="$TSV" '{dt=$6-$1; c=($7-$2)/dt; s=($8-$3)/dt; m=($9-$4)/dt*100; us=($7>$2)?($9-$4)*1e6/($7-$2):0; cap=(m>0)?c/m*sat:0;
    v=(m>=sat)?"상한 도달":(m>=90)?"여유 적음":"여유 있음"; free=(cap>0)?(1-c/cap)*100:0;
    if(tsv) printf "S\t%s\t%.0f\t%.0f\t%.1f\t%.3f\t%.0f\t%.0f\t%s\n", now, c, s, m, us, cap, free, v;
    else printf "%-9s %10.0f %10.0f %6.1f%% %9.3f %11.0f %5.0f%%  %s\n", now, c, s, m, us, cap, free, v}'
  P=$C; done
