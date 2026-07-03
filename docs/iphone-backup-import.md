# import iphone-backup

Read when: importing chat history from an extracted iPhone WhatsApp backup into the local wacli store, re-running an import, or migrating imported media files into the standard wacli media layout.

`wacli import iphone-backup` (alias: `import ios-backup`) reads the WhatsApp databases from an **extracted, decrypted** iPhone backup folder and imports contacts, chats, groups (including participants), messages, starred flags, and media references into `wacli.db`. It never talks to WhatsApp servers — it is a purely local, offline import.

## 1. Get an extracted backup folder

The importer does not read raw iTunes/Finder backups. You need the WhatsApp app-group container extracted to a plain folder first. In the backup it lives under the domain `AppDomainGroup-group.net.whatsapp.WhatsApp.shared`.

### Extracting with pyiosbackup

Create a local iPhone backup with Finder first (encrypted backups work — you just need the password). Finder stores backups under `~/Library/Application Support/MobileSync/Backup/<UDID>`.

```bash
pip install pyiosbackup

# Read the backup password without echoing it (zsh: read -s; bash: read -rs)
read -s BACKUP_PASS

BACKUP_DIR="$HOME/Library/Application Support/MobileSync/Backup/<UDID>"

# Sanity check: does the backup open with this password?
pyiosbackup stats "$BACKUP_DIR" -p "$BACKUP_PASS"

# Option A (fast): extract only the WhatsApp shared container
pyiosbackup extract-domain-path "$BACKUP_DIR" \
  AppDomainGroup-group.net.whatsapp.WhatsApp.shared -p "$BACKUP_PASS"

# Option B (full): extract the whole backup into the original
# filesystem layout, one folder per domain
pyiosbackup unback "$BACKUP_DIR" "$BACKUP_PASS" --target /Volumes/Data/iphone-backup
```

With option B, the folder to hand to wacli is
`/Volumes/Data/iphone-backup/AppDomainGroup-group.net.whatsapp.WhatsApp.shared`.
(`pyiosbackup extract-all` also works; it extracts per-domain folders under `--target` as well. Some pyiosbackup commands can be invoked as `python -m pyiosbackup …` if the CLI entrypoint is not on your PATH.)

GUI extraction tools (iMazing, ibackuptool, …) work too — anything that yields the shared-container folder below.

### Expected layout

Point `--path` at the folder that contains at least:

```text
<backup-folder>/
├── ChatStorage.sqlite     # required — chats, messages, media metadata
├── ContactsV2.sqlite      # optional — contact names
└── Media/ or Message/     # optional — the actual media files
    └── Media/...
```

`ChatStorage.sqlite` is mandatory; the import aborts without it. `ContactsV2.sqlite` is picked up automatically when present. Media files are resolved from the paths stored in the backup, tried relative to the backup folder as well as under `Media/` and `Message/` prefixes — so both common extractor layouts work.

## 2. Run the import

```bash
wacli import iphone-backup --path ~/backups/whatsapp-iphone
```

What happens:

- **Contacts** from `ContactsV2.sqlite` (and group member names from the chat DB) are upserted into the contact store.
- **Chats and groups** are created or updated; group participants are imported with de-duplication that prefers admin/superadmin roles when a member appears more than once.
- **Messages** are upserted keyed by chat + message ID, so the import is **idempotent**: re-running it (or running it next to a live `wacli sync`) does not create duplicates, it only fills gaps.
- **Identities are canonicalized**: `@lid` chats and phone-number (`@s.whatsapp.net`) chats belonging to the same person are merged into the phone-number chat during import, so backup history and live-synced history land in the same chat.
- **Media**: for every media message, the referenced file is looked up in the backup folder and **copied** into the standard wacli media layout (the same place `wacli media download` would put it), and the message row is marked as downloaded with that local path. Missing media files are skipped silently — the message itself is still imported, and the media stays downloadable later where WhatsApp still serves it.
- **Status/broadcast threads** are skipped by default.

Flags:

```bash
--path <dir>                 # extracted backup folder (required)
--include-status             # also import WhatsApp status/broadcast threads
--migrate-media-paths-only   # see below; --path not required in this mode
--json                       # machine-readable result
```

The summary reports counts for contacts, chats, groups, participants, messages, starred, media messages, and skipped status chats/messages.

## 3. Typical workflow

```bash
# 1. Import the old history from the backup
wacli import iphone-backup --path ~/backups/whatsapp-iphone

# 2. Link the live account and sync current messages on top
wacli auth

# 3. Optional: merge any remaining @lid duplicates into phone-number chats
wacli chats consolidate-identities
```

Order does not strictly matter — both import and sync upsert by chat + message ID — but importing first means the initial sync merges into already-named chats. `wacli sync` also runs the LID consolidation automatically after syncing unless `--no-consolidate-lids` is set.

## Migrate media paths only

If imported media was stored under old/non-standard paths (e.g. from an earlier import), this rewrites all stored `local_path` entries to the standard wacli media layout, copying files where needed, without importing anything:

```bash
wacli import iphone-backup --migrate-media-paths-only
```

It reports how many paths were checked, migrated, and skipped because the source or target file is missing.
