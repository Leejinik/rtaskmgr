# nethogs 오프라인 부트스트랩 번들

프로세스별 **네트워크** 컬럼은 nethogs(EPEL)가 있어야 채워진다. 운영 RHEL은 보통
인터넷/EPEL에 닿지 못하므로, 여기 RPM을 미리 보관했다가 호스트에 업로드해서
**오프라인 설치**한다. 설치 후 네트워크 테스트가 끝나면 **이전 의존성 상태로 롤백**한다.

## 번들 구성

```
rpms/rhel8/  nethogs-0.8.5-9.el8.x86_64.rpm   (EPEL 8)
             libpcap-1.9.1-5.el8.x86_64.rpm   (Rocky 8 BaseOS — 타깃에 없을 때 대비)
rpms/rhel9/  (예정)
```

nethogs 런타임 의존성: `libpcap`, `ncurses-libs`, `libstdc++`, `libgcc`, `glibc`.
glibc/libstdc++/libgcc/ncurses-libs 는 기본 OS에 항상 존재하므로 번들에는
nethogs + libpcap 만 담는다. (libpcap 도 대개 설치돼 있으나 최소 설치 호스트 대비)

## 설치 절차 (앱이 자동 수행할 흐름) — 172.17.64.24(RHEL 8.8)에서 검증됨

```bash
# 1) RPM 업로드 (앱은 base64 스트림으로 /tmp/nh 에 올림)
mkdir -p /tmp/nh   # + scp nethogs*.rpm libpcap*.rpm

# 2) 설치 전 트랜잭션 경계 기록 (롤백 기준점)
sudo dnf history list | head

# 3) 완전 오프라인 설치 (EPEL 메타데이터 조회 안 함, 서명검사 생략)
sudo dnf install --disablerepo='*' --nogpgcheck -y /tmp/nh/nethogs-*.rpm

# 4) 동작 확인 — 프로세스별 송수신(KB/s) 출력. 포맷: 프로그램/PID/UID\t송신\t수신
sudo nethogs -t -d 1        # 예: java/2290141/1000  365.7  303.0

# 5) 네트워크 테스트가 끝나면 롤백 — 이 트랜잭션이 설치한 것(nethogs)만 정확히 제거
sudo dnf history undo last --disablerepo='*' -y
#   → libpcap/ncurses-libs 등 기존 패키지는 건드리지 않음 (검증 완료)

# 6) 잔여 파일 정리
rm -rf /tmp/nh
```

## 출처

- nethogs: https://dl.fedoraproject.org/pub/epel/8/Everything/x86_64/Packages/n/nethogs-0.8.5-9.el8.x86_64.rpm
- libpcap: https://dl.rockylinux.org/pub/rocky/8/BaseOS/x86_64/os/Packages/l/libpcap-1.9.1-5.el8.x86_64.rpm
