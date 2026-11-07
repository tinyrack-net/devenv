# 계획: 콘솔 v2 (다중 사용자, OIDC, 워크스페이스)

웹 콘솔을 React + daisyUI로 다시 만든다. 목표는 두 가지다.

1. 사람이 환경에 직접 들어가 작업하거나 에이전트 작업에 개입할 때 실제로 쓸 수 있고 편한 화면.
2. 여러 사용자가 쓰는 플랫폼. 공유 자원(관리자가 관리하는 이미지·클래스)과 개인 자원(내 환경·이미지·토큰)을 나눈다. 로그인은 OIDC.

범위 밖: 파일 탐색기와 편집기. 파일 작업은 터미널, SSH(VS Code Remote-SSH 등), 에이전트로 한다.

## 원칙

- 설치는 여전히 Helm 하나. DB, cert-manager, 특정 CNI를 요구하지 않는다. 상태는 CRD와 Secret에 둔다.
- 설정은 성격별로 나눈다. 배포 설정(정적)은 설정 파일, 플랫폼 자원은 CRD, 사용자 데이터는 사용자 CRD.
- 인증 설정은 배포 설정에만 둔다. UI에서 바꾸면 GitOps와 충돌하고, 잘못 바꾸면 로그인이 막힌다. UI는 보여 주기만 한다.
- 지금 설치(비밀번호 + MCP 토큰 하나)는 설정 변경 없이 그대로 동작한다.

## 1. 사용자와 권한

### 사용자 `DevUser` (cluster scope, 새 CRD)

```yaml
apiVersion: devenv.tinyrack.net/v1alpha1
kind: DevUser
metadata:
  name: winetree94            # 사용자 이름(DNS label). 환경 owner에 들어간다
spec:
  displayName: "Winetree"
  email: me@example.com
  role: admin                 # admin | user (기본 user)
  disabled: false
  identities:                 # 로그인 수단. OIDC는 issuer+sub로 묶는다
    - provider: issuary
      subject: "8f2c…"
  sshKeys:                    # 기존 devenv-user-* ConfigMap을 대체
    - name: laptop
      publicKey: ssh-ed25519 AAAA… me@laptop
  limits:                     # 비우면 설정 파일의 기본값
    maxEnvironments: 10
    maxRunning: 5
    cpu: "16"
    memory: 64Gi
    disk: 500Gi
  preferences:                # 콘솔 설정(테마, 터미널 글꼴 등). 서버는 해석하지 않는 JSON
    theme: dark
status:
  lastLoginAt: …
  groups: [devenv-users]      # 마지막 로그인 때 IdP가 준 그룹
```

- OIDC로 처음 로그인하면 자동으로 만든다(설정 `auth.oidc[].autoCreate`, 허용 그룹·이메일 조건 통과 시). 끄면 관리자가 미리 만든 사용자만 로그인한다.
- 사용자 이름은 `preferred_username` → 이메일 앞부분 → `sub` 순으로 정하고 DNS label로 바꾼다. 겹치면 숫자를 붙인다.
- `disabled`이면 세션, 토큰, SSH 게이트웨이 접속이 모두 거부된다. 세션은 요청마다 사용자를 캐시에서 확인하므로 바로 끊긴다.

### 관리자 판정

다음 중 하나면 관리자다. 이슈어리처럼 그룹 클레임이 없는 IdP도 쓸 수 있게 여러 방법을 둔다.

- `DevUser.spec.role: admin`
- 로그인 그룹이 `auth.oidc[].adminGroups`에 있음
- 설정 `auth.admins`(사용자 이름 또는 이메일)에 있음
- 로컬 비밀번호 관리자 계정

### 자원별 범위

| 자원 | 공유 | 개인 |
|---|---|---|
| 이미지 `DevImage` | 관리자가 등록, 전체 또는 그룹에 공개 | 사용자가 등록, 본인만 사용 |
| 클래스 `DevEnvironmentClass` | 관리자만 관리. 그룹으로 사용 제한 가능(예: `lan` 클래스는 관리자 그룹만) | 없음 |
| 환경 `DevEnvironment` | 소유자가 다른 사용자·그룹과 공유 가능 | 기본 |
| SSH 키, 접근 토큰, 콘솔 설정 | 없음 | `DevUser`, 토큰 Secret |

CRD 변경:

- `DevImage.spec.owner`(비면 공유), `DevImage.spec.access.groups`(비면 전체)
- `DevEnvironmentClass.spec.access.groups`(비면 전체)
- `DevEnvironment.spec.owner`는 이제 실제로 적용한다. 목록 조회를 위해 라벨 `devenv.tinyrack.net/owner`도 붙인다.
- `DevEnvironment.spec.sharing: {users: [...], groups: [...]}`

### 환경 권한

| 동작 | 소유자 | 공유받은 사용자 | 관리자 |
|---|---|---|---|
| 보기, 터미널·데스크톱·SSH 접속, 시작·정지 | ✓ | ✓ | ✓ |
| 사양·이미지·포트·비밀번호 변경, 공유 설정, 삭제 | ✓ | | ✓ |

공유받은 사용자도 접속하므로 같은 터미널 세션을 함께 보며 작업할 수 있다(워크스페이스 참고).

### 사용량 제한

- 사용자별 환경 수, 실행 중인 수, CPU·메모리·디스크 합계. 기본값은 설정 파일 `users.defaultLimits`, 사용자별로 덮어쓸 수 있다. 관리자는 기본 무제한.
- 환경 생성·시작·사양 변경 때 기존 클래스 상한, 노드 여유와 함께 검사한다.

### 격리

- 환경 Pod끼리 통신을 막는 NetworkPolicy를 선택 사항으로 둔다(차트 `environments.isolation.enabled`). 켜면 환경으로 들어오는 연결은 devenv 서버(게이트웨이, 터미널, VNC)만 허용한다. 전용 IP 모드(multus, loadBalancer)는 LAN에서 직접 들어오므로 정책 대상이 아니다.
- 네임스페이스는 지금처럼 하나다. 사용자별 네임스페이스는 RBAC·쿼터 관리가 무거워서 하지 않는다.

## 2. 인증

### 로그인 수단

여러 공급자를 같이 켤 수 있다. 로그인 화면은 `GET /api/v1/auth/providers`를 보고 버튼을 그린다.

- `password`: 로컬 관리자 계정 하나(지금 방식). 비상용으로 남기고 설정에서 끌 수 있다.
- `oidc`: 여러 개 가능. Authorization Code + PKCE(S256), `state`·`nonce` 검증. `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`. 콜백은 `/api/v1/auth/oidc/{id}/callback`.
- `none`: 개발용(지금과 같음).

로그인 뒤에는 지금처럼 서버가 서명한 세션 쿠키를 쓴다. 쿠키에는 사용자 이름과 만료만 넣고 권한은 요청마다 `DevUser`에서 읽는다. IdP 토큰은 저장하지 않는다.

### 접근 토큰 (MCP, API)

- 사용자가 직접 발급한다(개인 설정 → 접근 토큰). 이름, 만료(선택), 범위(`mcp`, `api`)를 정한다. 값은 발급할 때 한 번만 보여 준다.
- 형식 `dvn_<id>_<secret>`. 서버 네임스페이스의 Secret `devenv-token-<id>`에 SHA-256 해시, 사용자, 범위, 만료를 저장한다. 마지막 사용 시각은 최대 5분에 한 번 기록한다.
- MCP 요청은 토큰 주인 권한으로 동작한다. 만든 환경의 소유자는 그 사용자이고, 라벨 `created-by=mcp`로 출처를 구분한다.
- 기존 전역 토큰(`devenv-keys`의 `mcp-token`, 차트 `mcp.existingSecret`)은 설정 `mcp.legacyToken.user`(기본: 로컬 관리자)의 토큰으로 계속 받는다. Bifrost 등록을 바꾸지 않아도 된다.
- 관리자는 모든 토큰을 보고 폐기할 수 있다.

### SSH 게이트웨이

- 키 인증: 접속하려는 환경에 권한이 있는 사용자(소유자, 공유받은 사용자)의 키면 허용한다. 키는 `DevUser.spec.sshKeys`에서 찾는다. 환경에 직접 넣은 `spec.sshAuthorizedKeys`도 계속 허용한다.
- 환경 안의 `authorized_keys`(전용 IP로 직접 접속할 때 쓰임)는 컨트롤러가 `spec.sshAuthorizedKeys` + 소유자 키 + 공유받은 사용자 키로 만든다. 지금처럼 생성 시점에 복사하지 않으므로 키를 바꾸면 모든 환경에 반영된다.
- 비밀번호 인증은 환경별 비밀번호 그대로.

## 3. 설정 분리

| 층 | 내용 | 저장 위치 | 바꾸는 곳 |
|---|---|---|---|
| 배포 설정 | 인증 공급자, 세션, 관리자 목록, MCP, SSH 공개 주소, 사용자 기본 제한 | 설정 파일 `devenv.yaml`(차트 `config:` → ConfigMap). 비밀은 `*File` 필드로 Secret 마운트 | Helm/GitOps. UI는 읽기 전용(비밀은 가림) |
| 플랫폼 자원 | 공유 이미지, 클래스, 사용자 | CRD | 관리자 UI 또는 GitOps |
| 사용자 데이터 | 개인 이미지, SSH 키, 토큰, 콘솔 설정 | CRD(`DevImage`, `DevUser`), Secret | 개인 설정 UI |

설정 파일:

```yaml
server:
  externalURL: https://devenv.example.com   # OIDC 콜백, 링크 생성에 사용
auth:
  session:
    ttl: 168h
    secureCookies: true
  admins: [winetree94, me@example.com]
  password:
    enabled: true
    username: admin
    passwordFile: /etc/devenv/secrets/admin-password
  oidc:
    - id: issuary
      name: Issuary
      issuer: https://auth.example.com
      clientID: devenv
      clientSecretFile: /etc/devenv/secrets/oidc-issuary
      scopes: [openid, profile, email]
      claims:
        username: preferred_username
        email: email
        name: name
        groups: groups
      autoCreate: true
      allowedGroups: []        # 비면 제한 없음
      allowedEmails: []        # 정확히 일치 또는 "*@example.com"
      adminGroups: []
users:
  defaultLimits:
    maxEnvironments: 5
    maxRunning: 3
    cpu: "8"
    memory: 32Gi
    disk: 200Gi
mcp:
  enabled: true
  legacyToken:
    user: admin
ssh:
  publicHost: ssh.example.com
  publicPort: 2222
```

- 서버는 `--config`(환경 변수 `DEVENV_CONFIG`)로 읽는다. 기존 플래그와 환경 변수는 계속 지원하며 설정 파일 값을 덮어쓴다.
- 차트는 `config:` 값을 그대로 ConfigMap으로 만들고, 기존 값(`auth.mode`, `auth.existingSecret`, `secureCookies`, `mcp.*`)은 설정 파일로 옮겨 렌더링한다. OIDC 비밀은 `auth.oidc[].existingSecret`로 받아 `/etc/devenv/secrets`에 마운트한다.
- 시작할 때 설정을 검증하고, 틀리면 이유를 남기고 종료한다. 관리자 화면 "시스템"에서 실제 적용된 설정을 보여 준다.

### 기존 데이터 옮기기 (서버 시작 시 1회, 멱등)

- 로컬 관리자 `DevUser`(이름은 `auth.password.username`, 역할 admin)를 만든다.
- `devenv-user-<hash>` ConfigMap의 SSH 키를 해당 `DevUser`로 옮긴다. ConfigMap은 옮긴 뒤 지운다.
- 소유자가 `mcp`인 환경은 `mcp.legacyToken.user`로 바꾼다. 소유자가 비었거나 없는 사용자인 환경은 관리자 소유로 둔다.
- 모든 환경에 owner 라벨을 붙인다.

## 4. 실시간 갱신

- `GET /api/v1/stream` (SSE): 내가 볼 수 있는 환경의 변경(생성, 상태, 삭제)과 활동 기록을 보낸다. 컨트롤러 캐시의 informer를 그대로 쓴다.
- 콘솔은 주기적 조회를 없애고 SSE 이벤트로 TanStack Query 캐시를 갱신한다. 연결이 끊기면 자동 재연결하고 전체를 한 번 다시 받는다.

## 5. 콘솔 화면

### 기술

| 영역 | 선택 |
|---|---|
| 스타일 | Tailwind CSS v4 + daisyUI 5, 테마 light/dark/system |
| 라우팅, 서버 상태 | react-router, TanStack Query (유지) |
| API 타입 | Go 타입에서 TS 타입 생성(`tygo`). 손으로 쓴 타입 제거 |
| 화면 분할 | react-resizable-panels |
| 아이콘 | lucide-react |
| 터미널, 데스크톱 | xterm, noVNC (유지) |

### 화면 구성

```
/login                          로그인 (공급자 버튼 + 로컬 계정)
/                               내 환경 (공유받은 환경 포함)
/new                            환경 만들기
/env/:name                      워크스페이스 (전체 화면)
/env/:name/settings             환경 설정 (사양, 포트, 비밀번호, 공유, 삭제)
/images                         이미지 (공유 + 내 이미지)
/settings/profile|keys|tokens|preferences
/admin/users|environments|images|classes|tokens|activity|system
```

관리 화면은 왼쪽 사이드바 + 본문의 daisyUI 대시보드다. 워크스페이스는 사이드바 없이 화면 전체를 쓴다.

#### 환경 목록

- 카드/표 전환, 상태·사양·IP 표시, 소유자·공유 표시.
- 한 번에 열기(워크스페이스), 터미널, 데스크톱, SSH 명령 복사, 시작·정지.
- 필터: 내 것 / 공유받은 것 / 에이전트가 만든 것, 상태. 관리자는 전체 사용자 보기.

#### 환경 만들기

- 이미지 카드(공유 / 내 이미지, 데스크톱 여부, 설명), 클래스 선택과 남은 용량, 사양(내 사용량 제한과 함께 표시).
- 고급(포트, 비밀번호, 추가 SSH 키, 공유)은 접어 둔다.
- 만든 뒤 워크스페이스로 바로 가서 시작 과정(이미지 받기, 볼륨, 시작)을 실시간으로 보여 준다.

### 워크스페이스

```
┌────────────────────────────────────────────────────────────────────┐
│ ◀ │ my-env ● Running · agent · 4CPU/8Gi │ 공유 2 │ ⌘K │ 접속 ▾ │ ⏸ ⟳ ⋯ │
├────┬───────────────────────────────────────────────────────────────┤
│ ⚙  │ [bash] [build] [🖥 데스크톱] [dev-server 로그] [+]          ⇔ │
│ 🤖 │                                                               │
│ 🔌 │  $ pnpm dev                                                   │
│ 📊 │  ready on :3000                                               │
│ 📜 │                                                               │
│    ├───────────────────────────────────────────────────────────────┤
│    │ (분할) 데스크톱 / 다른 터미널                                    │
└────┴───────────────────────────────────────────────────────────────┘
 ⚙ 프로세스  🤖 에이전트 활동  🔌 포트  📊 상태  📜 이벤트
```

- 탭과 분할(좌우·상하). 배치는 환경별로 브라우저에 저장한다.
- **터미널**: 세션이 서버 쪽에 남는다. 새로고침, 네트워크 끊김, 다른 기기에서도 같은 셸에 다시 붙는다. 여러 사람이 같은 세션을 동시에 볼 수 있다(공유받은 사용자, 관리자). 세션 목록, 이름 바꾸기, 종료. 글꼴·크기·테마 설정. 링크 클릭, 검색, 복사·붙여넣기.
- **데스크톱**: 화면 맞춤/원본 크기, 브라우저 크기에 맞춰 원격 해상도 변경(TigerVNC ExtendedDesktopSize), 전체 화면, 클립보드 양방향, 화질·압축 조절, 보기 전용 ↔ 조작 전환(에이전트가 조작 중일 때 보기 전용으로 지켜보기), Ctrl+Alt+Del 등 특수 키.
- **프로세스**: 에이전트가 `start_process`로 띄운 것을 포함한 백그라운드 프로세스 목록. 출력 실시간 보기, 중지. 콘솔에서 새 백그라운드 프로세스 시작.
- **에이전트 활동**: 이 환경에 대한 MCP 호출 기록. 시간, 사용자(토큰 이름), 도구, 요약(명령과 종료 코드, 수정한 파일 경로와 diff, 데스크톱 조작). 진행 중인 호출 표시. 실시간 갱신.
- **포트**: 환경 안에서 열린 TCP 포트 자동 감지(헬퍼가 `/proc/net/tcp*`를 읽음). 포트별로:
  - 전용 IP 모드면 `http://<ip>:<port>` 링크
  - 항상 `ssh -L` 포트 포워딩 명령 복사
- **상태**: CPU·메모리·디스크 사용량(헬퍼 `stats`), 재시작, 마지막 종료 사유(OOM 등).
- **이벤트**: Kubernetes 이벤트와 Pod 상태.
- **접속 메뉴**: SSH 명령, `~/.ssh/config` 조각, VS Code / Cursor 열기 링크(`vscode://vscode-remote/ssh-remote+<host>/home/dev`), JetBrains Gateway 안내, 전용 IP, 비밀번호 설정 여부.
- **명령 팔레트(⌘K)**: 환경 전환, 새 터미널, 데스크톱 열기, 시작·정지·재시작, 접속 정보 복사.
- 환경이 정지돼 있으면 "시작" 버튼과 시작 과정 표시. 터미널 탭은 시작되면 자동으로 다시 붙는다.

### 개인 설정

- 프로필(이름, 이메일, 로그인 수단, 그룹, 내 사용량과 제한)
- SSH 키
- 접근 토큰(MCP용 연결 정보와 설정 예시 포함: URL, 헤더)
- 콘솔 설정(테마, 터미널 글꼴·크기, 기본 이미지·클래스)

### 관리자

- 사용자: 목록, 역할, 사용 중지, 사용량 제한, 마지막 로그인, 사용자의 환경·토큰
- 환경: 전체 사용자 환경, 소유자 변경
- 이미지: 공유 이미지 관리, 공개 그룹
- 클래스: 목록과 상세(읽기). 사용 그룹 수정. 복잡한 스토리지·네트워크 설정은 YAML로 보여 주고 GitOps로 관리하도록 안내
- 토큰: 전체 토큰 보기·폐기
- 활동: 전체 MCP 호출 기록
- 시스템: 버전, 적용된 설정(비밀 가림), SSH 게이트웨이 정보, 노드 용량, 헬퍼·prepull 상태

## 6. 서버 작업 목록

| 영역 | 내용 |
|---|---|
| 설정 | `internal/config`: 파일 읽기, 기본값, 검증, 플래그·환경 변수 덮어쓰기, 비밀 가림 |
| 사용자 | `DevUser` CRD, `internal/users`(조회, 자동 생성, 이름 정하기, 관리자 판정, 제한 계산) |
| 인증 | `internal/auth`: 공급자 목록, OIDC, 세션, 토큰(`internal/tokens`), 미들웨어가 `Principal`(사용자, 관리자 여부, 그룹, 인증 수단)을 넣음 |
| 권한 | `internal/access`: 이미지·클래스·환경 권한 판정. REST, MCP, 게이트웨이, 터미널·VNC가 모두 사용 |
| API | 사용자·토큰·관리자 API, 환경 공유, 개인 이미지, 사용량, SSE, 포트·프로세스·터미널 세션 API |
| 컨트롤러 | `authorized_keys`에 소유자·공유 사용자 키 반영(DevUser 변경 시 재조정), owner 라벨, 선택적 NetworkPolicy |
| 헬퍼 | `pty` 세션(데몬이 PTY와 스크롤백 링 버퍼 보관, 여러 클라이언트 연결), `ports` |
| MCP | 토큰 주인 권한으로 동작, 활동 기록(`internal/activity`, 환경별 메모리 링 버퍼 + SSE) |
| 옮기기 | 위 "기존 데이터 옮기기" |

터미널 세션 프로토콜: 서버가 SSH 세션에서 `devenv-agent pty attach <id> --cols --rows`를 실행하고 바이트를 그대로 중계한다. 크기 변경 등 제어는 별도 채널(같은 SSH 연결의 두 번째 세션 `pty resize`)로 보낸다. 데몬은 세션별로 최근 출력 1MB를 보관해 다시 붙을 때 화면을 복원한다. 헬퍼가 없는 환경은 지금처럼 일회성 셸로 동작한다.

## 7. 진행 순서

| 단계 | 내용 | 버전 |
|---|---|---|
| 1 | 설정 파일, `DevUser`, 공급자(비밀번호 + OIDC), 세션, 토큰, 권한, 사용량 제한, 옮기기, 게이트웨이·MCP 연동 | 0.9.0 |
| 2 | 콘솔 기반 재작성: daisyUI 레이아웃, 로그인, 환경 목록·만들기·설정, 이미지, 개인 설정, 관리자, SSE, 타입 생성 | 0.9.0 |
| 3 | 워크스페이스: 탭·분할, 유지되는 터미널 세션, 데스크톱 개선, 프로세스, 상태·이벤트, 접속 메뉴, 명령 팔레트 | 0.10.0 |
| 4 | 에이전트 활동, 포트 감지 | 0.9.0 |
| 5 | 홈랩: 이슈어리에 devenv 클라이언트 등록, 설정 파일로 전환, OIDC 로그인 확인 | — |

## 8. 검증

- 단위 테스트: 설정 검증, 이름 정하기, 관리자 판정, 권한 표, 제한 계산, 토큰 해시·만료, 세션.
- OIDC: 테스트용 가짜 IdP(`httptest`, 서명 키 직접 생성)로 로그인 전체 흐름, `state`·`nonce` 위조, 허용 조건, 자동 생성.
- envtest: 옮기기, `authorized_keys` 재조정, 공유.
- 헬퍼: PTY 세션 붙기·떼기·다시 붙기, 여러 클라이언트, 스크롤백 복원.
- kind 전체 흐름 + Playwright 화면 확인(두 사용자: 관리자, 일반 사용자. 공유, 제한, 토큰으로 MCP).
- 홈랩: 이슈어리 로그인, 기존 환경·Bifrost MCP 동작 유지 확인.

## 구현 결과 (0.9.0)

1~4단계를 한 번에 구현했다. 계획과 달라진 점:

- 포트 미리보기 URL(`<port>-<env>.<domain>`)은 빼고 후속 작업으로 남겼다. 와일드카드 DNS·인증서와 Host 기반 프록시, 쿠키 범위 설계가 따로 필요하다. 지금은 전용 IP 링크와 `ssh -L` 명령을 보여 준다.
- 환경 간 NetworkPolicy 격리도 후속 작업이다. 다중 사용자를 실제로 열기 전에 넣는다.
- MCP 대기는 기존대로 최대 5분이다. 5분이 지나면 오류 대신 현재 상태와 메모를 돌려준다. 앞단 게이트웨이(Bifrost 등)의 호출 제한은 게이트웨이 설정으로 맞추고 devenv에서 우회하지 않는다.
- API 타입은 OpenAPI 대신 `tygo`로 Go 구조체에서 바로 생성한다(`make types`).
- 의존성은 pnpm의 최소 공개 기간 정책 때문에 하루 이상 지난 버전으로 고정했다.

검증: `make test`(envtest 포함 13개 패키지), 가짜 IdP로 OIDC 전체 흐름, kind 업그레이드(로컬 관리자 생성, 소유자 라벨 이전), Playwright로 로그인부터 워크스페이스·새로고침 후 같은 셸 복원·토큰 발급·관리자 화면까지.
