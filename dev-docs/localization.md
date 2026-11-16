## Language and output

lbctl supports English (`en`) and Simplified Chinese (`zh-Hans`). Selection order:

1. Global `--lang` flag.
2. `LBCTL_LANG`, then `LANGBOT_LANG`.
3. `LC_ALL`, `LC_MESSAGES`, then `LANG`.
4. Windows preferred UI languages, falling back to the default UI language;
   macOS `AppleLanguages` when no environment override exists.
5. English. Unsupported languages select English. `C`, `POSIX` and their
   encoding variants carry no language information and are skipped, allowing
   Windows/macOS UI preferences or a lower-priority locale to take effect.

Network region selection is independent of display language.

Messages live in `internal/i18n`. English message text is the lookup key;
missing translations fall back to that text. Command execution selects one
process-wide language for its lifetime. Concurrent embedded `Execute` calls
with the same language/output mode share the session; differing modes wait
until the active session finishes. Existing source wording is retained for machine output,
including error messages, schema descriptions and recovery metadata. JSON
field names, error codes and operation IDs are not translated. Human help
still honors `--lang` when combined with `--output`.

The first public command writes the terms and anonymous telemetry notice to
stderr, including when telemetry is disabled. A `~/.langbot/terms-notice`
marker suppresses subsequent notices. `LANGBOT_TELEMETRY=0` or `DO_NOT_TRACK=1`
disables telemetry independently of language and notice state.

Progress is on stderr and stays silent for operations shorter than 400 ms.
Completed steps use a leading check mark and right-aligned elapsed time;
the active step shows its position in the operation plan. Repeated registration
checks share one visible step. TTYs use one animated line with elapsed time and the
latest subprocess line. Redirected output, `NO_COLOR`, `TERM=dumb`, JSON/YAML
output and consoles without Windows VT support use plain lines. `--verbose`
shows full subprocess output; otherwise failures show the captured tail.
Installation logs retain full subprocess output. Downloads show byte counts
and percentages when the server supplies a content length.

Panels use terminal cell widths rather than rune or byte counts. CJK, fullwidth
punctuation and emoji occupy two cells; middle dots, arrows and checks occupy
one. Borders occupy at most `min(columns - 2, 76)` cells. Terminals narrower
than 60 columns use unboxed lines without right alignment. The palette uses
cyan, bold and dim only. First-run notices follow the product heading, before
deployment steps. Stable versions show either an available upgrade command or
an up-to-date label; dev and prerelease versions do not suggest downgrades.

TTY output redraws a two-line region (step/time and the last subprocess line),
truncating to terminal cell width. Completed steps remain above it. Windows
without VT uses carriage-return-only refresh. Plain output has no heartbeat.
The CLI never hides the cursor and clears the active region on completion or
cancellation. Summary and status panels use thin borders at 60 columns or more;
narrow terminals and plain output use unboxed lines.

Install/upgrade show the product, resolved target version, deployment method,
region selection source, instance directory and port. The success panel links
to account, model and bot setup. Bare `lbctl` shows grouped common commands;
`lbctl --help` retains complete help. Status adds directory, startup and uptime
without changing machine output. Native uptime starts when the supervisor
writes its PID after launching Python; Docker uses the LangBot container's
start timestamp. Remote or unavailable runtime timing is shown as not reported.

`lbctl list` reads all registered local instances and displays name, directory,
port, runtime state and observed startup state. Missing records remain visible
as unknown; uninstalled records are marked not installed. JSON/YAML use stable
`instances` entries and do not require a saved API context.

Health waits display the latest Core log line (bounded file tail for native,
one Compose log line for Docker). After three minutes the CLI prints one hint
to inspect `lbctl logs` or use `lbctl restart`; machine output is unchanged.

## Validation

`go test ./internal/i18n ./internal/progress ./internal/command` covers locale
precedence, fallback, format arguments, machine-output stability, the notice,
and plain progress. The source scan rejects CJK string literals outside the
catalog (tests and comments are excluded).

`scripts/test-progress.py` exercises a delayed local HTTP fixture through a
real PTY and verifies animation, `NO_COLOR`, `TERM=dumb`, JSON and redirected
output. It does not contact an external API.

Run `scripts/test-install-wizard.py` and the isolated native E2E in both
`LANG=C` and `LANG=zh_CN.UTF-8`. Native CI covers both locales on Linux,
Windows and macOS. Existing Chinese snapshots explicitly select `--lang`;
locale tests exercise the automatic selection path separately.

## Landing installer synchronization

The repository installers distribute the CLI binary. The landing installers
also launch deployment; preserve those additional steps when synchronizing:

- Copy locale precedence and propagation through `LBCTL_LANG`.
- Copy bilingual numbered download/verification/install messages.
- Copy the bilingual product heading and website/docs/plugin links.
- Copy the PowerShell outer exception handler for clean stderr failures.
  It sets `$LASTEXITCODE = 1` and returns without terminating an interactive
  `irm ... | iex` shell; wrappers should propagate that code when needed.
- Preserve existing network-region detection, mirror selection and deployment
  arguments. These changes do not alter network policy.
