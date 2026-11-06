# 계획: devenv MCP 서버

AI 에이전트가 MCP로 devenv 환경을 만들고, 명령을 실행하고, 정지·삭제할 수 있게 한다. 에이전트마다 독립된 개발 공간을 갖게 하는 것이 목적이다.

## 결정 사항

| 항목 | 결정 |
|---|---|
| 인증 | MCP 전용 Bearer 토큰 하나, 전체 권한(관리자와 동일) |
| 명령 실행 | `exec`(동기 실행)만. 파일 도구·장기 실행 프로세스는 범위 밖 |
| 수명 관리 | ~~유휴 시 자동 정지~~ → 제거(v0.6.0). 정지·삭제는 명시적으로만 |
| 연결 경로 | 홈랩 Bifrost에 Streamable HTTP + `Authorization` 헤더로 등록 |

## 서버 구조

- devenv 서버에 `/mcp` 엔드포인트를 추가한다. 공식 Go SDK `github.com/modelcontextprotocol/go-sdk`의 Streamable HTTP를 쓴다.
- `Authorization: Bearer <token>`. 토큰은 서버 키 Secret(`devenv-keys`, 키 `mcp-token`)에 자동 생성되고, 차트 값 `mcp.existingSecret`으로 바꿀 수 있다. 토큰은 `/mcp`에서만 유효하다.
- CRUD 로직은 REST API와 공유한다(`internal/envsvc`). 사양 검증, 클래스 상한, 여유 자원 확인이 그대로 적용된다.
- MCP로 만든 환경은 `spec.owner=mcp`, 라벨 `devenv.tinyrack.net/created-by=mcp`.

## 도구

| 도구 | 설명 |
|---|---|
| `list_images` | 사용 가능한 이미지 |
| `list_classes` | 클래스(네트워크·스토리지 모드, 상한) |
| `get_capacity` | 클래스별 최대 CPU·메모리·디스크 |
| `list_environments` | 환경 목록(`created_by_mcp_only` 필터) |
| `get_environment` | 상세 + SSH 접속 정보 |
| `create_environment` | 생성, `wait`이면 Running까지 대기(최대 5분) |
| `update_environment` | 사양·이미지·포트 변경 |
| `start_environment` / `stop_environment` | 시작·정지(`wait`) |
| `delete_environment` | 삭제(홈 볼륨 포함) |
| `exec` | `bash -lc`를 `dev`로 실행. `cwd`, `env`, `stdin`, `timeout_seconds`(기본 120, 최대 1800), `auto_start`. 결과는 `exit_code`, `stdout`, `stderr`, `timed_out`, `truncated` |

`exec` 출력은 스트림별 최대 256KB(넘으면 앞·뒤를 남기고 가운데 생략). 환경당 동시 실행 4개.

## 유휴 자동 정지 (v0.6.0에서 제거)

v0.5.0에서 `spec.idleTimeout`과 활동 기록으로 구현했지만 제거했다. LAN IP로 직접 하는 SSH·포트 접속을 서버가 관찰할 수 없어서, 실제로 쓰는 중인 환경을 정지시킬 수 있었기 때문이다. 정지·삭제는 사용자나 에이전트가 명시적으로 한다.

## 안전장치

- 모든 도구 호출을 구조화 로그로 남기고, 환경 이벤트에 요약을 기록한다.
- 토큰 재발급: Secret의 `mcp-token` 키를 지우고 서버를 재시작한다.

## 홈랩 연동

- Cilium: `bifrost-system/bifrost` → `devenv-system/devenv:8080` 허용.
- Bifrost 등록(관리자 API): `http://devenv.devenv-system.svc.cluster.local/mcp`, code mode, headers 인증, `dev`·`personal` Virtual MCP.
- 토큰 원본은 Bitwarden `bifrost-mcp (homelab secrets)`의 `DEVENV_MCP_TOKEN` 필드.

## 단계

1. 서비스 계층 분리, 토큰 인증, `/mcp` + 조회·CRUD 도구
2. `exec`
3. 유휴 자동 정지(v0.5.0, 이후 제거)
4. 문서, v0.5.0, 홈랩 배포·Bifrost 등록
5. 유휴 자동 정지 제거(v0.6.0)

## 결과 (v0.5.0, 2026-11-06)

- 구현: `internal/mcpserver`(도구·exec). REST 핸들러는 `Server.CreateEnvironment` 등 공용 함수로 분리했다.
- 홈랩: 0.5.0 배포. Cilium에서 `bifrost-system/bifrost` → `devenv-system/devenv:8080`을 허용했다.
- Bifrost: 클라이언트 `devenv`(code mode, headers 인증, `tools_to_execute=["*"]`)를 `POST /api/mcp/client`로 등록했다. 주소는 `http://devenv.devenv-system.svc.cluster.local/mcp`. `dev`(id 2)·`personal`(id 4) Virtual MCP에 연결했다.
- 토큰: Secret `devenv-system/devenv-keys`의 `mcp-token`. 원본은 Bitwarden `344e14cc-213f-410a-9273-b4f700a68590`의 `DEVENV_MCP_TOKEN` 필드.
- 검증: Bifrost code mode로 환경 생성 → `git clone` → Go 설치 → devenv 빌드(52MB) → 삭제.

### 운영 메모

- Bifrost code mode의 도구 호출은 60초에서 끊긴다. 오래 걸리는 작업은 `nohup ... > log 2>&1 &`로 띄우고 짧은 `exec`로 로그를 확인한다. 도구 설명에도 같은 안내가 있다.
- `delete_environment` 직후 `list_environments`에는 삭제 중인 환경이 잠시 보인다(foreground 삭제).

## v0.7.0: 코딩 도구 (2026-11-06)

- 도구 추가: `read_file`, `write_file`, `edit_file`, `list_dir`(SFTP), `search`, `find_files`(ripgrep, grep 대체), `start_process`, `read_process_output`, `list_processes`, `stop_process`(분리된 프로세스 그룹, 로그 `/tmp/devenv-proc`). 전체 21개.
- 이미지: base에 ripgrep·fd·file·patch. `devenv-agent`(Go, Node.js+corepack, uv, gh)를 `agent` DevImage로 등록.
- 버그: Node 아카이브가 `/usr/local/bin` 소유자를 1001로 바꿔 sshd가 `AuthorizedKeysCommand`를 거부했다. agent 이미지에서 소유자를 고치고, base entrypoint가 시작할 때 복구하도록 했다.
- Bifrost: 도구 목록을 캐시하고 reconnect·설정 수정으로는 갱신하지 않는다(per-call 연결 클라이언트). 도구를 바꾸면 클라이언트를 삭제·재등록하고 Virtual MCP `dev`(2)·`personal`(4)에 다시 연결해야 한다. 새 client_id `1882f868-39ab-4c38-94d2-55a49f449bfc`.
- Bifrost code mode는 스크립트 한 번(`executeToolCode`)이 60초를 넘으면 끊긴다. 도구 호출을 여러 개 묶으면 합계가 60초에 걸리므로, 환경 생성·빌드 대기는 호출을 나눈다. `read_process_output`의 `wait_seconds`는 40 이하로 쓴다.
- 검증: Bifrost로 agent 환경 생성 → clone → find_files/search/read_file → edit_file → start_process 빌드 → read_process_output 대기 → 변경이 바이너리에 반영됨을 확인 → 삭제.
