# fm-cli scripting contract (v0.3)

This is the machine-readable surface the `ninepointlabs.fastmail` Omarchy plugin
drives. It deliberately mirrors the shape of the HEY CLI so the plugin, which is
adapted from 37signals' `37signals.hey` plugin, needs minimal changes. The TUI
(`fm-cli` with no arguments, or `fm-cli tui`) is unchanged.

## Envelope

Every command given `--json` prints exactly one JSON object.

Success, on stdout, exit 0:

```json
{"ok": true, "data": <payload>, "summary": "human one-liner"}
```

Failure, on stderr, nothing on stdout, exit 1:

```json
{"ok": false, "error": "message", "code": "auth|usage|network|remote|busy", "hint": "optional"}
```

`code: "auth"` means signed out or the credential was rejected. `code: "busy"`
means another `fm-cli watch` already holds the per-user watch lock. An unknown
subcommand, flag, or `--events` value fails with `code: "usage"` and the text
`unknown command "x"` / `unknown flag "--x"` / `unknown event "x"`, so a plugin
can detect an old CLI.

## Version

```
fm-cli --version
fm-cli version
```

Prints one line: `fm-cli version 0.3.0`.

## Auth

```
fm-cli auth status --json
```

Always succeeds (exit 0), even when signed out:

```json
{"ok": true, "data": {
  "authenticated": true,
  "auth_type": "oauth",          // "oauth" | "token" | "none"
  "username": "tim@example.com", // JMAP session username, "" when signed out
  "expires_at": "2026-09-09T20:00:00Z", // access-token expiry, null for API tokens
  "refresh_available": true,     // an OAuth refresh token is stored
  "storage": "keyring",          // "keyring" | "env"
  "base_url": "https://api.fastmail.com"
}, "summary": "Signed in as tim@example.com"}
```

```
fm-cli auth login [--token] [--no-browser] [--silent-success]
fm-cli setup [--silent-success]
fm-cli auth logout
fm-cli auth token
```

`auth login` runs the OAuth authorization-code flow: registers a public client
with Fastmail on first use (dynamic client registration), opens the browser to
the consent page, and catches the redirect on a loopback port. `--no-browser`
prints the URL instead of opening it. `--token` skips OAuth and reads a Fastmail
API token from stdin (or prompts when stdin is a terminal). `setup` is the
plugin's guided path: it signs in only when `auth status` says signed out, and
with `--silent-success` prints nothing when already signed in. `auth token`
prints a current bearer token, refreshing first if needed, for curl and scripts.

Fastmail requires an RFC 8707 `resource` indicator on the authorize and token
requests; fm-cli sends the JMAP session URL. Without it the authorize endpoint
bounces with `invalid_target`.

## Accounts

```
fm-cli account list --json
```

```json
{"ok": true, "data": [
  {"id": "u12345678", "name": "tim@example.com", "email": "tim@example.com", "active": true}
], "summary": "1 mail account"}
```

Personal accounts with the mail capability. `active` marks the primary mail
account. `--account <id>` on other commands selects one; `--account all` is
accepted and means every listed account.

## Boxes

```
fm-cli box list --json
fm-cli box view <inbox|all|role|mailbox-id> [--limit N] [--account ID] [--exclude FOLDER]... --json
fm-cli inbox [--limit N] [--account ID] --json      # alias for box view inbox
```

`box view inbox` (or any real folder):

```json
{"ok": true, "data": {
  "id": "P-F",
  "kind": "inbox",                 // JMAP role, "" for plain folders
  "name": "Inbox",
  "account_id": "u12345678",
  "app_url": "https://app.fastmail.com/mail/Inbox?u=12345678",
  "unread_count": 3,               // Mailbox.unreadEmails
  "total_count": 162,              // Mailbox.totalEmails
  "folders": [ <folder>, ... ],    // see below
  "postings": [ <posting>, ... ]   // newest first, one per thread, at most --limit (default 50, max 200)
}, "summary": "3 unread in Inbox"}
```

### The virtual `all` box

Fastmail rules file mail into folders, so the Inbox is a partial view.
`box view all` is the attention view the plugin uses: the newest Inbox
threads (seen or not) **plus every unseen thread from every other included
folder**, merged, newest first, one posting per thread, capped at `--limit`.

```json
{"ok": true, "data": {
  "id": "all",
  "kind": "all",
  "name": "All folders",
  "account_id": "u12345678",
  "app_url": "https://app.fastmail.com/mail/Inbox?u=12345678",
  "unread_count": 49,              // unseen emails across every included folder
  "total_count": 162,              // Inbox totalEmails
  "folders": [
    {"id": "P-F",  "kind": "inbox", "name": "Inbox",   "unread_count": 3,  "total_count": 162, "app_url": "..."},
    {"id": "Pibo", "kind": "",      "name": "FInance", "unread_count": 44, "total_count": 241, "app_url": "..."}
  ],
  "postings": [ <posting>, ... ]
}, "summary": "49 unread across 2 folders"}
```

`folders` lists, in mailbox sort order, the Inbox and every other included
folder that currently has unseen mail. It is what a folder-chip strip renders.
`folders` is present on every `box view` response (for a real folder it holds
just that folder).

Excluded from `all` by role: `junk`, `trash`, `drafts`, `sent`, `snoozed`,
`scheduled`, `archive`. `--exclude` (repeatable; a folder name, path such as
`Other Services/Gmail`, role, or id) excludes more; a parent folder excludes
its children. A thread's seen state is computed across included folders only,
so one unread message in Finance is one unread badge.

### Posting

A posting is one thread as seen from a box:

```json
{
  "id": "AB5kIMdwqots",            // thread id — the identity the plugin uses for seen/open
  "thread_id": "AB5kIMdwqots",
  "email_id": "StmlKEsTVdXB",      // newest email of the thread in the box
  "account_id": "u12345678",
  "box_id": "Pibo",                // the folder this posting is reported from
  "box_kind": "",                  // its role, "inbox" for the Inbox
  "box_name": "FInance",
  "name": "Subject line",
  "summary": "First ~200 chars of the newest message",
  "active_at": "2026-09-09T13:40:20Z",   // receivedAt of the newest email in the box
  "seen": false,                   // true when no email of the thread in the box lacks $seen
  "unseen_count": 2,
  "visible_entry_count": 3,        // emails of the thread in the box
  "flagged": false,
  "app_url": "https://app.fastmail.com/mail/FInance/AB5kIMdwqots.StmlKEsTVdXB?u=12345678",
  "alternative_sender_name": "",
  "creator": {
    "name": "The HEY Team",        // From display name, falls back to the address
    "email_address": "support@hey.com",
    "initials": "TH"
  }
}
```

For the `all` box, a posting's `box_*` is the folder its newest email sits
in (the Inbox wins when a thread spans folders), and `seen`,
`unseen_count`, `visible_entry_count` are computed across included folders.

## Seen

```
fm-cli seen <id>... [--account ID] --json
fm-cli unseen <id>... [--account ID] --json
```

Each id may be a thread id (every email of the thread is patched) or an email
id. Response: `{"ok": true, "data": {"ids": ["..."], "seen": true}}`.

## Watch

```
fm-cli watch [--box inbox]... [--events added,updated,deleted,new,resync] [--account all]
```

Runs until interrupted. Prints one JSON object per line on stdout, flushed
immediately. stderr carries diagnostics only. Lines:

```json
{"change": "ready"}          // cursors set and the push connection live; also after every reconnect's catch-up
{"change": "disconnected"}   // push connection dropped; a reconnect with catch-up follows
{"change": "resync"}         // the server could not list changes one at a time; re-read the box
{"change": "added",   "new": true,  "box": {"id": "P-F", "kind": "inbox", "name": "Inbox"}, "posting": <posting>}
{"change": "updated", "new": false, "box": {...}, "posting": <posting>}
{"change": "deleted", "new": false, "box": {...}, "posting": {"id": "<thread id>", "email_id": "<email id>", "thread_id": "..."}}
```

Semantics match `hey watch`: a line is a wake-up, not a delta; the plugin
re-reads the box on any line. `new` is true only for an email created since the
watch's cursor that is unseen and in the box, so the backlog the first read
carries is never `new`. `--events new` selects only those, alone or with the
others; `resync` may be left out. Without `--box`, every mailbox is followed
and each line names the box the email is in (`box.kind` carries the role, so
a plugin can ignore junk, trash, drafts, sent, snoozed and scheduled). Every
listed account is watched. Watch postings carry `box_*` like box reads.

Implementation: JMAP EventSource (`types=Email,Mailbox`, `closeafter=no`,
`ping=60`) as the wake-up, then `Email/changes` from the stored state, then
`Email/get` for created and updated ids. A `cannotCalculateChanges` error emits
`resync` and resets the cursor to the current state. The connection reconnects
with backoff (1 s doubling to 60 s, reset only after a connection that lived
30 s); each reconnect emits `disconnected`, catches up, then `ready`.

**One watch per sign-in.** Fastmail keeps a single EventSource per
authorization: opening a second one makes the server send `event: close` to the
first. `fm-cli watch` therefore takes a per-user lock
(`$XDG_RUNTIME_DIR/fm-cli/watch.lock`) and a second invocation fails with
`code: "busy"` instead of fighting. Fastmail pings at least every 30 s; the
watch treats two missed pings as a dead connection.

**Line buffering.** Anything that pipes the watch through another process
(`head -c`, say) must keep that process line-buffered (`stdbuf -oL head -c …`),
or lines arrive in 4 KB batches.

## Open in TUI

```
fm-cli tui [--thread <thread id>] [--email <email id>] [--remote]
```

`fm-cli tui` opens the TUI on the main menu. With `--thread` it opens the
reader on the newest email of that thread (or the one named by `--email`,
which also works alone). `--remote` hands the target to a TUI that is already
running, over `$XDG_RUNTIME_DIR/fm-cli/tui.sock`, and exits 0; with no TUI
listening it exits 1 without output on stdout. Plugins open a thread in two
steps, mirroring the HEY plugin:

```
fm-cli tui --thread <id> --email <id> --remote   # a running TUI jumps there
omarchy-launch-or-focus-tui --app-id=com.ninepointlabs.fm-cli fm-cli tui --thread <id> --email <id>
```

The second command raises the running TUI's window, or starts a new TUI on
that thread when none is running.

## Web links

`app_url` values use the web app's own form,
`https://app.fastmail.com/mail/<Mailbox>/<threadId>.<emailId>?u=<account>`, where
`<account>` is the JMAP account id without its leading `u`. They open the
message in a browser or in `omarchy-launch-webapp`. (The Fastmail desktop app
cannot be deep-linked to a message: its `fastmail:` handler only serves sign-in
callbacks, so the plugin does not offer it.)

## Install

```
omarchy-mise-install github:ninepointlabs/fm-cli fm-cli
```

Releases ship `fm-cli_<version>_linux_<arch>.tar.gz` archives containing the
`fm-cli` binary.
