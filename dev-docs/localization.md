## Language and output

lbctl supports English (`en`) and Simplified Chinese (`zh-Hans`). Selection order:

1. Global `--lang` flag.
2. `LBCTL_LANG`, then `LANGBOT_LANG`.
3. `LC_ALL`, `LC_MESSAGES`, then `LANG`.
4. Windows preferred UI languages, falling back to the default UI language;
   macOS `AppleLanguages` when no environment override exists.
5. English. Unsupported languages and `C`/`POSIX` select English.

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

Progress is on stderr. TTYs use one animated line with elapsed time and the
latest subprocess line. Redirected output, `NO_COLOR`, `TERM=dumb`, JSON/YAML
output and consoles without Windows VT support use plain lines. `--verbose`
shows full subprocess output; otherwise failures show the captured tail.
Installation logs retain full subprocess output. Downloads show byte counts
and percentages when the server supplies a content length.

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
- Copy the PowerShell outer exception handler for clean stderr failures.
  It sets `$LASTEXITCODE = 1` and returns without terminating an interactive
  `irm ... | iex` shell; wrappers should propagate that code when needed.
- Preserve existing network-region detection, mirror selection and deployment
  arguments. These changes do not alter network policy.
