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

## 패킷 캡쳐 (tcpdump)

툴바 **📡 패킷 캡쳐…** — 연결된 호스트에서 특정 포트의 패킷을 덤프해 내 PC로 받아온다.
현장 대응 중 "포트 덤프 떠서 개발팀에 전달"을 앱 안에서 끝내기 위한 기능.

```
[rtaskmgr] ──sudo 1회──▶ [RHEL8/9]  base64로 실린 래퍼를 root가 /run 에 풀어 setsid 로 기동
                                    └─ tcpdump -Z <로그인유저> -w <pcap>  (파일은 로그인 유저 소유 0600)
[rtaskmgr] ──sudo 없이──▶ 목록 / 중지 / 다운로드 / 삭제
```

실행되는 명령은 이것과 동등하다(필터는 **정수·주소·고정 키워드로만 재조립**되며 입력
원문은 셸이나 argv에 도달하지 않는다):

```
tcpdump -i <iface> -w <pcap> -nn -B 4096 [-p] -s <0|256|96> [-c <n>] [-U] -Z <로그인유저> \
        ( host 10.0.0.5 or net 10.1.0.0/24 ) and ( port 3306 or portrange 8080-8090 )
```

- **호스트/포트 필터**: IP·CIDR·호스트명과 포트·포트범위를 각각 쉼표/공백으로 여러 개.
  괄호는 필수다 — pcap 문법에서 `and`가 `or`보다 강하게 묶이므로 없으면
  `host A or (host B and port 80)`으로 잘못 해석된다.
- `-XX`는 쓰지 않는다. 화면에 hex를 찍는 표시 옵션이라 `-w`로 저장할 때는 효과가 없고,
  `-s 0`이면 패킷 전체가 파일에 남아 Wireshark에서 그대로 보인다.
- `-p`가 기본(프로미스큐어스 미사용) — 운영 NIC 상태를 건드리지 않는다. 이 서버가
  주고받지 않는 트래픽까지 봐야 할 때만 모달에서 켠다.
- **3중 자동 중지**: 최대 시간 · 최대 용량 · 최소 여유 디스크. 어느 하나라도 걸리면
  래퍼가 savefile 을 정상적으로 닫고 사유를 `<pcap>.done` 에 남긴다
  (`deadline`/`maxsize`/`maxpackets`/`low-disk`/`signal`). 디스크 가드는 현재 기록
  속도를 반영하는 적응형(`MINFREE + rate×12초`)이다.
- **앱을 닫아도 계속**: `setsid` 로 SSH 채널에서 완전히 분리된다. 래퍼가 SIGKILL 되어도
  `timeout` 과 `ulimit -f` 가 커널 쪽에서 시간·용량 상한을 강제한다.
- **중지에 신호를 쓰지 않는다**: `.stop` 센티넬 파일만 만들고 래퍼가 스스로 마무리한다.
  강제 중지만 `.pid`/`.tpid` 에 기록된 PID 를 `comm` + cmdline 으로 확인한 뒤 신호한다
  (`pkill -f <경로>` 는 자기 부모 셸과 남의 `tail`/`scp` 까지 매칭하므로 전면 금지).
- **저장 위치**는 모달에서 고른다. `tmpfs`/`ramfs` 는 후보에서 제외된다 — tcpdump 는
  회선 속도로 쓰기 때문에 호스트 RAM 을 태우고 OOM killer 가 운영 프로세스를 고른다.
- **다운로드**는 스트리밍이다(원격 `gzip -1` → 로컬 임시파일 → 원자적 rename). gzip
  트레일러의 CRC32 로 종단 간 검증이 되고, 고른 경로에 반쪽 파일이 생기지 않는다.
  결과물은 `.pcap` **한 개뿐**이다(그대로 전달하면 된다). 호스트·NIC·필터·중지사유·드롭수는
  캡쳐 목록에서 확인한다.
- **비정상 종료를 감추지 않는다**: 서버 재부팅·OOM 으로 죽은 캡쳐는 `.done` 이 없으므로
  `interrupted`(⚠ 비정상 종료)로 표시된다. 이때 "정상 중지"로 승격시키지 않는다.
- tcpdump 는 설치하지 않는다(솔루션에 항상 존재하는 전제). 없으면 그 사실을 명확히 알린다.

## 빌드

샘플러를 먼저 리눅스용으로 빌드해 임베드한 뒤 앱을 빌드한다:

```bash
bash scripts/build-sampler.sh   # 또는: pwsh scripts/build-sampler.ps1
wails build                     # 개발: wails dev
```

산출물: `build/bin/rtaskmgr.exe`

(샘플러 산출물 `internal/agent/sampler-linux-amd64`는 git에 커밋돼 있으므로 CI에는
이 단계가 없다. `cmd/sampler`를 고쳤을 때만 다시 돌려 커밋하면 된다.)

### 리눅스 (Ubuntu 24.04 / 26.04)

```bash
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
wails build -tags webkit2_41    # → build/bin/rtaskmgr
wails dev   -tags webkit2_41
```

`-tags webkit2_41`은 **필수**다. Ubuntu가 24.04에서 `webkit2gtk-4.0`을 제거했는데
Wails v2는 아직 pkg-config를 4.0으로 가리켜서, 태그 없이는 `webkit2gtk-4.0.pc not found`로
실패한다. **크로스컴파일은 불가능**하므로(리눅스 백엔드가 cgo로 시스템 웹뷰를 링크) 각
타깃은 그 OS에서 빌드한다.

런타임 의존: `libwebkit2gtk-4.1-0`, `libgtk-3-0t64`, `libsoup-3.0-0`, 한글 표시용
`fonts-noto-cjk`, 이모지 라벨용 `fonts-noto-color-emoji`, 폴더 열기용 `xdg-utils`.
Ubuntu 24.04+ 데스크톱에는 전부 기본 포함. 릴리즈 바이너리(`rtaskmgr-linux-amd64`)는
CI가 **ubuntu-24.04**에서 빌드해 glibc 하한이 2.34다.

**리눅스에서 달라지는 것:**
- **파일 위치 열기**는 파일을 선택해 주지 않고 **폴더만** 연다(`xdg-open`). Explorer의
  `/select`나 Finder의 `open -R`에 해당하는 이식 가능한 방법이 리눅스엔 없다.
- **Wireshark로 열기**는 PATH의 `wireshark`를 찾는다(`sudo apt install wireshark`).
  Windows 설치 경로 탐색(`Wireshark\Wireshark.exe`)은 리눅스에서 매칭되지 않는다.
- **다운로드 전 여유 공간 검사**가 리눅스에도 생겼다(`statfs`). 로그 일괄 수집이
  수 GB를 당겨오므로 중간에 디스크가 차면 전송 전체가 헛돈다.

## 보안 메모

읽기 전용 진단 도구지만 원격 `/tmp`에 샘플러 바이너리를 업로드/실행한다(인프라
점검용, 인가된 호스트에서만 사용). HostKey 검증은 현재 `InsecureIgnoreHostKey`
(사내망 전제) — 외부망 사용 시 known_hosts 검증으로 교체할 것.
