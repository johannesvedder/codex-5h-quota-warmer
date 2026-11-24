# Codex Five-Hour Quota Warmer

`codex-5h-quota-warmer` is a Go dynamic-library plugin for CLIProxyAPI. It sends one non-streaming OpenAI-compatible message through every currently active Codex credential at configured daily times. Each request uses the exact credential ID returned by `host.auth.list`.

The plugin only selects credentials where all of the following are true:

- `provider` is `codex`;
- `disabled` is `false`;
- `id` is present.

The plugin reports `status` and `unavailable` in the selection log but does not use them to skip an enabled credential. CPA evaluates cooldown and availability for the configured warm-up model when the request is sent.

## Management UI

The plugin registers a browser resource at:

```text
/v0/resource/plugins/codex-5h-quota-warmer/status
```

The page provides a manual warm-up button, editable daily schedule rows, the next scheduled execution time for every enabled row, the latest credential-level results, and a bounded persistent log. Enter the Management key to load these data; the page does not persist it. Before the first run, the latest execution time is shown as unavailable.

The resource page carries no management data. Enter a CPA Management key in the page before it requests protected APIs. The key is held only in the current page. Legacy browser-storage entries are removed when this page is opened.

The page writes settings through CLIProxyAPI's standard plugin configuration endpoint. This preserves the configured `enabled` and `priority` values while updating the plugin-owned settings.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-5h-quota-warmer:
      enabled: true
      priority: 1
      model: "gpt-5.6-luna"
      prompt: "Reply with exactly: quota window activated"
      timezone: "Asia/Singapore"
      max_log_entries: 200
      state_file: "plugins/state/codex-5h-quota-warmer.json"
      schedules:
        - id: "morning-05"
          at: "05:00"
          enabled: true
        - id: "morning-10"
          at: "10:00"
          enabled: true
        - id: "afternoon-15"
          at: "15:00"
          enabled: true
```

Each enabled schedule executes once daily in the configured IANA timezone. The plugin waits for the next daily time after startup and does not perform a startup warm-up. Duplicate enabled schedule times are rejected.

The plugin writes the latest run, per-credential results, and up to `max_log_entries` log entries to `state_file` using a temporary file and replacement (atomic on Unix). The file is created with owner-only permissions. The default path uses the operating system's user configuration directory. For Docker, set `state_file` to a path on a persistent volume, such as a writable path under the mounted plugin directory shown above. The Management key, credential tokens, raw upstream errors, credential IDs and labels are not stored there. Saved results use per-run credential numbers; live authenticated results can still display labels. State files must be JSON files directly within `plugins/state` or the default plugin configuration directory. Existing custom paths outside these directories must be migrated by the deployment operator. History from releases before persistent state was added remains available only in the host logs.

Use the Management UI to add, edit, enable, or delete rows. The same operations are possible through CLIProxyAPI's standard endpoint:

```text
GET /v0/management/plugins/codex-5h-quota-warmer/config
PUT /v0/management/plugins/codex-5h-quota-warmer/config
```

## Plugin Management API

All routes below require the CPA Management key.

```text
GET  /v0/management/plugins/codex-5h-quota-warmer/status
POST /v0/management/plugins/codex-5h-quota-warmer/run
GET  /v0/management/plugins/codex-5h-quota-warmer/logs
```

`POST /run` starts one manual pass. A simultaneous automatic or manual pass returns HTTP 409. Manual starts are limited to one per minute (HTTP 429). Configuration limits are 48 schedule rows, a 4096-byte prompt, a 256-byte model name and a 64-byte schedule ID. A run rejects more than 1000 eligible credentials. The bounded persistent log records configuration updates, each planned and triggered schedule, run starts, credential selection counts and skip reasons, each credential result, and run completion. The plugin sends schedule, selection, result, failure, and completion events to the host logger through `host.log` with allowlisted diagnostic fields only; credential IDs, labels and raw upstream errors are omitted. Selection logs include disabled and missing-ID skip counts, plus the unavailable and inactive counts among credentials sent to CPA. A run with zero eligible credentials produces a warning and completion log with `credential_count: 0`. A storage failure appears in the status response and the host log.

## Build and Install

From this repository on Linux, build the shared library:

```bash
cd go
go build -buildmode=c-shared -o ../codex-5h-quota-warmer.so .
```

Install `codex-5h-quota-warmer.so` into the proxy server's `plugins/linux/$(go env GOARCH)/` directory, then enable the plugin using the configuration above and restart the proxy server. Use `.dylib` on macOS and `.dll` on Windows. The dynamic-library basename must remain `codex-5h-quota-warmer` so it matches `plugins.configs.codex-5h-quota-warmer`.

GitHub Releases provide platform ZIP files named `codex-5h-quota-warmer_<version>_<goos>_<goarch>.zip` and a `checksums.txt` file. Each ZIP contains the dynamic library at its root. Tagged releases are built for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

## Security and deployment

This is a **native in-process plugin**, not a sandboxed service. It has the same operating-system permissions as CLIProxyAPI. Build from a reviewed, pinned commit and do not automatically update native plugins. Run the host as a non-root user; protect the management and resource paths using network access control and HTTPS, and protect the plugin binaries and state directories from modification by untrusted users. The operator controls the working directory and trusted state-directory roots; those roots and their parents must not be writable by untrusted users. Keep any required writable mounts minimal. The host remains responsible for authentication, upstream transport, token refresh and model-execution timeouts. The plugin does not independently cancel a blocked host callback. Release checksums are not source-to-binary attestations. Test the plugin in a disposable CLIProxyAPI instance before production use.

Old state files may contain credential identifiers until successfully rewritten. Old browser tabs and old server logs may still contain legacy data; close old tabs, review log retention and rotate the management key if it may have been exposed.
