# Docker image sources

Docker installs in `cn` probe the five public repositories below concurrently.
Global installs prefer Docker Hub and enter the same mirror selection when Hub
cannot serve the target tag or its pull fails. Each probe has one three-second
deadline covering `GET /v2/`, anonymous Bearer authentication, and `HEAD` of the
target tag. Available public repositories are tried in ascending probe latency;
`docker.langbot.app/langbot-public/rockchin/langbot` is the final fallback.

Before pulling automatically, lbctl resolves the tag from the canonical LangBot
registry, or Docker Hub if the canonical registry cannot supply it. It hashes the
returned manifest/index bytes and checks the registry's digest header when
present. Public repositories only receive a pull of that trusted `@sha256:`
reference. Docker's `RepoDigests` must contain the requested repository and
digest. A failed pull, missing digest, or mismatch rejects that source.
All three managed Compose services use the verified immutable reference.

`image_repository` records the successful source. Upgrade tries that source
first with the new version's trusted digest. If it fails, upgrade probes again,
reports the changed source, and saves it only after successful upgrade. The
existing backup and stop-before-switch safeguards remain in effect.

`--image-repository` persists `image_repository_explicit: true`: no automatic
selection or fallback takes place, including on upgrades. Explicitly selecting
one of the five public mirrors still requires the official digest and inspection
check. Other explicitly selected repositories are trusted as user-selected
sources: lbctl pulls their tag, inspects the digest, and pins the managed Compose
reference to that digest without contacting another registry. Existing custom repository
records are also treated as explicit. Adopted external Compose layouts retain
their existing image handling rather than being rewritten into managed layouts.

`lbctl doctor` lists all five candidates, Docker Hub, the canonical fallback,
their probe results and elapsed time, and the source it would use. A working
saved source takes precedence. These are network checks, not image pulls or
digest verification; before an installation exists the probed tag is `latest`.
Install and upgrade print source selection through the progress display in
English or Chinese, after successful pull and verification. No digest is claimed
verified before inspection succeeds.

Pull and inspection failures emit a separate `cli_operation` event with
`command=image_pull`, `step=image_pull`, `outcome=failed`, and
`error_code=image_pull`, even when another source succeeds. The enclosing
install/upgrade still reports its actual outcome. Repository URLs, Docker output,
and registry tokens are not included in these attempt events. Opt-out settings
are unchanged. Terminal install/upgrade failures also retain `step=image_pull`.

## Candidate provenance and network check

The allowlist is the five candidates supplied in LANG-522; it is not populated
from remote lists. Change `PublicImageRepositories` in
`internal/deploy/image_sources.go` to maintain it.

Snapshot: **2026-11-17, Singapore host, tag `v4.11.7`**. Latency is the full
registry and manifest probe, including authentication. It is not layer download
throughput or a guarantee of reachability from mainland China.

| Candidate | Source | Result | Probe time |
|---|---|---|---:|
| `docker.m.daocloud.io` | [DaoCloud public-image-mirror](https://github.com/DaoCloud/public-image-mirror) | Available | 2066 ms |
| `docker.1ms.run` | [1ms service](https://1ms.run) | Available | 2227 ms |
| `dockerproxy.net` | [Registry endpoint](https://dockerproxy.net/v2/) supplied in LANG-522 | Available | 1437 ms |
| `hub.rat.dev` | [Registry endpoint](https://hub.rat.dev/v2/) supplied in LANG-522 | Available through redirect to `docker.1ms.run` | 2301 ms |
| `docker.xuanyuan.me` | [Registry endpoint](https://docker.xuanyuan.me/v2/) supplied in LANG-522 | Unavailable: HTTP 403 browser challenge | 57 ms |
| `docker.langbot.app/langbot-public/rockchin/langbot` | LangBot canonical registry | Available | 1209 ms |
| `rockchin/langbot` | Docker Hub | Available | 1773 ms |

The automatic candidate list excludes `docker.xuanyuan.me`: its HTTP 403 browser
challenge cannot be completed by the CLI. It also excludes `hub.rat.dev`: the
redirect reaches `docker.1ms.run`, which is already probed directly, so the alias
adds latency without an independent fallback. Existing stored repositories and
explicit `--image-repository` values remain supported, including redirects.

## Regression coverage

`internal/deploy/image_sources_test.go` uses local TLS registries for latency
ordering, partial outages, the shared three-second deadline, Bearer challenges
and redirects, pull errors, digest mismatch, canonical fallback, explicit-source
behavior, trusted manifest integrity, and reuse of pinned Compose layouts.
`internal/app/image_sources_test.go` executes consecutive upgrades against local
HTTP fixtures, checking source reselection, persisted digests, reuse without
reprobing, and preservation of the running deployment on pull failure.

Real Docker validation uses a separate Compose project, isolated lbctl home,
port 25322, no adapter/debug ports, and telemetry disabled. Installation of
`v4.11.7` in forced `cn` selected `docker.1ms.run` and verified index digest
`sha256:0fbc152b94759f6aaa0e798a9c594e3cef2d0379ac506f3631102bafcc463a64`.
Upgrade to `v4.11.8` reused that source, verified
`sha256:eb3d37bb38261bce87db0c331ac7222b9d302c62166f20f54dda7f29dfbdb489`,
and returned a healthy versioned endpoint. All three Compose services referenced
the new digest, which was also present in Docker's `RepoDigests`.
The test used Docker CLI 27.5.1 with daemon 20.10.24; the test data directory was
owned by the invoking user so the existing upgrade backup checks could read it.
