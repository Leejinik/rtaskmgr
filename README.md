# rtaskmgr — Linux 작업 관리자 (SSH)

RHEL8/9 호스트에 SSH로 접속해 **Windows 작업 관리자와 동일한 UI**로 프로세스별
CPU / 메모리 / 디스크 / 네트워크 사용량, PID, 서비스명(systemd unit)을 실시간으로
보여주는 데스크탑 툴. Wails v2 + Go + React-TS.

## 동작 방식

```
[rtaskmgr (내 PC)] ──SSH──▶ [RHEL8/9]
   1. capability probe (nethogs/pidstat/os-release)
   2. 임베드된 샘플러 바이너리를 /tmp 에 base64 업로드 + chmod
   3. 단일 스트리밍 세션에서 sampler 를 1초 루프로 실행
   4. /proc 기반 NDJSON 프레임을 매초 수신 → 작업관리자 테이블 렌더
```

- **샘플러**(`cmd/sampler`)는 의존성 없는 정적 리눅스 바이너리. `/proc` 만 읽어
  CPU%(전체 코어 대비, 100%=전 코어)·메모리%·디스크 I/O(`/proc/pid/io`)·
  서비스명(`/proc/pid/cgroup`→systemd unit)을 한 프레임으로 emit.
- **다중 호스트**: 호스트마다 탭 하나. 각 호스트는 독립 SSH 세션/고루틴.
- **sudo**: 체크 시 샘플러를 `sudo -S`로 실행 → 모든 프로세스의 디스크 I/O 수집
  (미사용 시 본인 소유 프로세스만 디스크값, 나머지는 `—`).
- **네트워크 컬럼**: 리눅스는 프로세스별 네트워크를 기본 제공하지 않음. nethogs
  연동 전까지 `—`(N/A). (후속 작업: `rpms/rhel8|9` 오프라인 설치 + nethogs 파싱)

## 로깅

- 접속 즉시 모든 프레임을 `~/.rtaskmgr/sessions/.capture-*.tmp` 에 1초 단위 버퍼링.
- **Ctrl+S**: 로그 이름을 묻고, 그 시점부터 `<이름>.ndjson` 으로 자동 누적 저장.
- **프로세스 더블클릭**: 해당 PID의 1초 단위 CPU/메모리/디스크 시계열 상세.
- **종료 시**: 아직 이름을 안 붙였으면 "저장/저장 안 함" 확인. 저장 안 하면 임시
  로그를 디스크에서 삭제.

## 빌드

샘플러를 먼저 리눅스용으로 빌드해 임베드한 뒤 앱을 빌드한다:

```bash
bash scripts/build-sampler.sh   # 또는: pwsh scripts/build-sampler.ps1
wails build                     # 개발: wails dev
```

산출물: `build/bin/rtaskmgr.exe`

## 보안 메모

읽기 전용 진단 도구지만 원격 `/tmp`에 샘플러 바이너리를 업로드/실행한다(인프라
점검용, 인가된 호스트에서만 사용). HostKey 검증은 현재 `InsecureIgnoreHostKey`
(사내망 전제) — 외부망 사용 시 known_hosts 검증으로 교체할 것.
