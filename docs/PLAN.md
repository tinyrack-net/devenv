# devenv 계획

Kubernetes 위에서 동작하는 원격 개발 환경 플랫폼. 중앙 서버가 Kubernetes API 로 개발 환경 Pod 를 생성·삭제하고, 웹 콘솔에서 환경 CRUD, 이미지 카탈로그, 웹 터미널, VNC 데스크탑을 제공한다.

## 원칙

1. Helm 한 줄 설치: `helm install devenv oci://ghcr.io/tinyrack-net/charts/devenv`
2. 필수 외부 의존성 없음: cert-manager, 외부 DB, 특정 CNI·스토리지를 요구하지 않는다.
3. 기본값으로 어디서나 동작: 표준 클러스터에서 SSH(게이트웨이), 웹 터미널, VNC 가 동작한다.
4. 고급 기능은 클러스터 능력에 따라 opt-in: 전용 IP(Multus), LoadBalancer 등.

## 범위

포함: 환경 CRUD, 시작/중지, 사양 지정(클러스터 여유 내), SSH, 웹 터미널, VNC(데스크탑 이미지), 이미지 카탈로그, 삭제 시 리소스 완전 정리.

제외(후순위): 다중 사용자, 콘솔 내 이미지 빌드, VM, 루트 파일시스템 영속, 컨테이너 내 Docker.

## 아키텍처

```
[React SPA] ──HTTP/WS──> [devenv (Go 단일 바이너리, 단일 Deployment)]
                           ├─ REST API      환경/이미지/클래스/SSH 키
                           ├─ WS proxy      웹 터미널(pods/exec), VNC(TCP)
                           ├─ SSH gateway   ssh -p 2222 <env>@host
                           └─ Controller    controller-runtime
                                 │ reconcile
                                 ▼
  DevImage, DevEnvironmentClass, DevEnvironment ──owns──> Pod + PVC + Secret + Service
```

- 상태 저장: CRD + Secret. DB 없음.
- 웹훅 없음: 검증은 CRD CEL 규칙 + API 서버 레이어.
- SPA 는 `go:embed` 로 바이너리에 포함.

## 리소스 모델 (`devenv.tinyrack.net/v1alpha1`)

- `DevImage` (cluster): 이미지 카탈로그. `image`, `displayName`, `description`, `desktop`(VNC 제공 여부), `defaultResources`.
- `DevEnvironmentClass` (cluster): 관리자가 정의하는 클러스터 적응 레이어. `storage`, `network`, `limits`, `scheduling`, `podOverrides`.
- `DevEnvironment` (namespaced): `imageRef`, `classRef`, `resources`, `running`, `sshAuthorizedKeys`, `owner`.

`DevEnvironment` 삭제 시 OwnerReference 로 하위 리소스가 GC 된다.

### 네트워크 모드

| 모드 | 동작 | 요구 | 전용 IP | 전 포트 |
|---|---|---|---|---|
| gateway (기본) | 내장 SSH 게이트웨이가 사용자명으로 환경 라우팅 | 없음 | ✗ | `-L`/웹 프록시 |
| loadBalancer | 환경별 LoadBalancer Service | LB 구현체 | ✓ | ✗ (포트 선언) |
| multus | 관리자가 만든 NAD 참조, `network-status` 에서 IP 반영 | Multus + NAD | ✓ | ✓ |

### 스토리지

- `storageClassName` 비우면 기본 StorageClass. `mode: ephemeral` 이면 emptyDir.
- RWO 만 요구.

### 사양 검증

클래스 `limits` → 스케줄 가능한 노드 중 여유(allocatable − requested) 확인 → ResourceQuota.

## 이미지 규약

- 사용자 `dev` (uid/gid 1000), sudo 허용.
- `/home/dev` 영속 볼륨 마운트, 최초 기동 시 skel 복사.
- sshd 22, `authorized_keys` 는 `/etc/devenv/ssh/authorized_keys` 로 마운트.
- 데스크탑 이미지: VNC `127.0.0.1:5901` (서버가 SSH 터널로 접근). 자세한 내용은 docs/images.md.
- 프로세스 관리는 s6-overlay 또는 supervisord.

## 인증

최소 구현: 설치 시 관리자 비밀번호 자동 생성(Secret) → 로그인 → 서명된 HttpOnly 세션 쿠키. `Authenticator` 인터페이스로 추후 OIDC 등 교체.

## 저장소 구조

```
cmd/devenv/          엔트리
api/v1alpha1/        CRD 타입
internal/controller/ reconciler
internal/api/        REST
internal/auth/       인증
internal/gateway/    SSH 게이트웨이
internal/proxy/      WS 프록시
config/crd/          생성된 CRD
charts/devenv/       Helm 차트
web/                 React SPA
images/              개발 환경 이미지
```

## 마일스톤

진행 현황: M1~M5 완료(v0.3.0). 홈랩 배포는 tinyrack-net/homelab `apps/base/devenv`.

| 단계 | 내용 |
|---|---|
| M1 코어 | 저장소 골격, CRD 3종 + CEL, reconciler(gateway 모드, 스토리지 추상화, 시작/중지/삭제), envtest |
| M2 API·UI·인증 | REST, 사양 검증, 최소 인증, SPA |
| M3 접속 | SSH 게이트웨이, 웹 터미널, noVNC, base/desktop 이미지 |
| M4 배포 | Helm 차트(OCI), GitHub Actions(이미지·차트·kind e2e), 문서 |
| M5 네트워크 확장 | loadBalancer, multus(stable MAC), 홈랩 적용 |
