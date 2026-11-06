# 계획: 에이전트 도구 P2 (서버측 구현, 단일 헬퍼 바이너리)

대상은 devenv MCP만 가진 에이전트다(로컬 셸·파일시스템 없음). 코딩 에이전트가 아닌 일반 채팅 클라이언트(OpenWebUI 등)에서도 쓰는 것을 전제로 한다. Bifrost code mode는 쓰지 않는다(직접 도구 호출).

## 원칙

- 컨테이너 이미지에 요구하는 것: sshd + sftp-server (desktop 이미지는 VNC 서버 추가). 그 외는 서버에서 처리하거나 헬퍼로 주입한다.
- 효율이 필요한 일은 헬퍼 `devenv-agent`에 넣는다. 헬퍼는 서버 이미지와 같은 이미지에서 Kubernetes image volume으로 마운트한다. 이미지 수정·패키지 설치가 없다.

## 기존 도구 점검 결과

| 문제 | 재현 | 원인 |
|---|---|---|
| `exec` 종료 코드가 항상 0 | `exit 7` → 0 | sshd가 명령을 세션 리더로 띄워서 `setsid`가 fork하고 부모가 바로 0으로 끝난다 |
| `exec` 타임아웃이 명령을 못 죽임 | 2초 타임아웃 뒤 `sleep 30` 생존 | 같은 원인. SSH 신호가 이미 끝난 부모에게 간다 |
| `start_process` 같은 id 중복 시작 | 에러 없이 기존 프로세스 반환 | pid 파일 갱신 타이밍으로 중복 검사가 틀린다 |
| `search`가 이미지에 따라 다르게 동작 | rg 없는 이미지에서 잘못된 정규식이 0건 | 컨테이너 도구 의존 |

단위 테스트가 가짜 sshd를 써서 못 잡았다. 실제 이미지(OpenSSH)로 도는 테스트를 추가한다.

측정(홈랩 LAN): 새 SSH 연결 59ms, 열린 연결의 새 세션 24ms. 지금은 도구 호출마다 새 연결이고 `read_process_output` 대기는 초당 최대 2연결이다.

## 설계

### 헬퍼 `devenv-agent`
- 정적 Go 바이너리. `cmd/devenv-agent`. 서버 이미지에 함께 넣고 환경 Pod에 image volume으로 `/opt/devenv/bin`에 마운트한다(읽기 전용).
- 서버는 SSH 세션에서 `/opt/devenv/bin/devenv-agent serve`를 실행하고 stdin/stdout으로 JSON 요청·응답을 주고받는다. 포트·추가 인증 없음.
- 기능:
  - `exec`: 새 프로세스 그룹으로 실행, 정확한 종료 코드, 타임아웃 시 그룹 전체 종료, 출력 상한(앞·뒤 보존), stdin·env·cwd, OOM 판정(cgroup `memory.events`의 `oom_kill` 증가).
  - 백그라운드 프로세스: 헬퍼 데몬(`devenv-agent daemon`, 첫 요청 때 자동 시작, 유닉스 소켓)이 자식 프로세스를 관리한다. 시작·목록·출력 읽기(오프셋, 대기, wait_for)·정지. 출력은 파일 링 버퍼.
  - 검색: `search`(Go 정규식, .gitignore, 바이너리·대용량 파일 건너뜀), `find_files`(glob).
  - 진단: 디스크 사용량(statfs), cgroup 메모리 사용량·OOM 카운터.
- 헬퍼가 없으면(사용자 정의 Pod spec 등) `exec`만 셸 경로로 동작하고 나머지는 "helper unavailable" 에러.

### 서버
- SSH 연결 풀: 환경별 연결을 재사용하고 유휴 시 닫는다. 환경 재시작·호스트 키 변경 시 재연결.
- 파일 도구는 SFTP 그대로. `read_file`은 이미지(png/jpeg/gif/webp)를 MCP ImageContent로(큰 이미지는 서버에서 축소), `encoding: base64`로 임의 바이너리. `write_file`도 base64.
- 진단: Pod 상태·이벤트·metrics-server + 헬퍼 정보. `restart_environment`.
- 데스크탑: 서버가 SSH 터널로 VNC(RFB)를 직접 말한다. `desktop_screenshot`(PNG), `desktop_click`, `desktop_scroll`, `desktop_type`, `desktop_key`.
- 이미지 미리 받기: `prepull: true` DevImage를 image volume으로 마운트하는 DaemonSet.

### Bifrost
- devenv 전용 Virtual MCP `devenv`를 만들고 클라이언트를 직접 모드(code mode 해제)로 등록한다. 기존 `dev`·`personal`에서는 뺀다.
- OpenWebUI에서 이 Virtual MCP를 도구 서버로 붙여 테스트한다.

## 단계

1. 헬퍼 + 연결 풀 + exec·백그라운드·검색 이전, 실제 이미지 통합 테스트
2. Bifrost 전용 Virtual MCP(직접 모드), 바이너리·이미지 파일, 진단, restart
3. 데스크탑(VNC 클라이언트)
4. 이미지 미리 받기
5. 문서, v0.8.0, 홈랩, OpenWebUI 검증

## 결과 (v0.8.0)

- 헬퍼 `devenv-agent`(3.8MB, `cmd/devenv-agent`)를 서버 이미지에 넣고 image volume으로 `/opt/devenv`에 마운트한다. 헬퍼가 없으면 `exec`만 셸로 동작한다.
- 버그 수정 확인(실제 이미지): `exit 7` → 7, 타임아웃 시 자식까지 종료, 같은 id 재시작 거부, 잘못된 정규식은 에러.
- OOM: 명령 하나가 아니라 컨테이너 전체가 OOM으로 재시작되는 경우가 많아서(셸 세션이 끊김), 서버가 재시작 횟수와 종료 이유(OOMKilled)를 보고 `oom_killed`로 알려 준다.
- `exec`가 셸 종료 시 프로세스 그룹을 죽이던 동작은 `app &`로 띄운 GUI 앱까지 죽여서 바꿨다. 백그라운드 자식은 남고, 출력 파이프를 잡고 있으면 셸 종료 0.5초 뒤 반환한다.
- 연결 풀: 따뜻한 `exec` 왕복 20-40ms.
- 데스크탑: 서버측 RFB 클라이언트(Raw 인코딩). 캡처 약 10ms. 한글 입력은 유니코드 keysym으로 된다.
- 이미지 미리 받기: DaemonSet이 이미지들을 image volume으로 마운트하고 서버 이미지(`devenv-agent sleep`)만 실행한다.
