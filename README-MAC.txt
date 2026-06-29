rtaskmgr — macOS 빌드 안내
============================

Wails는 Windows에서 macOS 바이너리로 크로스 컴파일을 지원하지 않습니다.
그래서 이 소스를 Mac에서 직접 빌드해야 합니다. 아래 순서대로 하면 됩니다.

이 앱은 Mac에서 빌드하더라도, 원격 RHEL 서버에는 linux/amd64 샘플러
(internal/agent/sampler-linux-amd64, 이미 동봉됨)를 업로드해서 동작합니다.
즉 Apple Silicon(M1~) Mac에서도 정상 동작합니다. Mac CPU 종류와 무관합니다.


1) 사전 설치 (한 번만)
----------------------
  # Homebrew 가 없다면 먼저 설치: https://brew.sh
  brew install go node
  go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0

  # wails 명령이 안 잡히면 PATH 에 Go bin 추가
  echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.zshrc
  source ~/.zshrc

  # 환경 점검 (Xcode Command Line Tools 등 안내가 나오면 따라 설치)
  wails doctor


2) 빌드
-------
  cd rtaskmgr

  # (선택) 임베드 샘플러를 다시 만들고 싶을 때만. 동봉본이 있으면 생략 가능.
  bash scripts/build-sampler.sh

  # 프론트엔드 의존성은 wails 가 알아서 npm install 합니다.
  wails build

  # Intel + Apple Silicon 둘 다 도는 유니버설 바이너리로 만들려면:
  # wails build -platform darwin/universal


3) 실행
-------
  open build/bin/rtaskmgr.app

  처음 실행 시 "확인되지 않은 개발자" 경고가 나오면:
  시스템 설정 > 개인정보 보호 및 보안 > "확인 없이 열기" 를 누르거나,
  Finder 에서 앱을 Control-클릭 > 열기.


문제 해결
---------
- `wails: command not found`  →  1)의 PATH 설정을 다시 확인.
- `xcode-select` 관련 오류    →  `xcode-select --install` 후 재시도.
- 빌드는 됐는데 앱이 안 열림   →  `wails doctor` 결과를 확인.
- 샘플러 관련 오류           →  `bash scripts/build-sampler.sh` 로 재생성.

동봉되지 않은 것 (Mac에서 자동 생성/설치됨):
  frontend/node_modules, frontend/dist, build/bin  — wails build 가 만듭니다.
