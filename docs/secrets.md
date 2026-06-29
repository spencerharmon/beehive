# Secrets & GPG Workflow

Secrets are gpg-encrypted yaml committed into the beehive repo. The keyring
lives in `/etc/beehive/gnupg`, shared by CLI, frontend, and honeybees. Honeybees
keep key access on purpose: they provision infrastructure and write secrets
autonomously.

## Layout

- `SECRETS.yaml.gpg` — top-level, global secrets.
- `submodules/<name>/SECRETS.yaml.gpg` — per-submodule, deconflicts concurrent
  edits. Referenced by that submodule's `INFRASTRUCTURE.md`.

Each file is one gpg-encrypted **single** yaml document. Document separators
(`---`) are not allowed.

## CLI

```
beehive secret add    -f file.yaml     # create encrypted doc
beehive secret update -f file.yaml     # replace contents
beehive secret edit                    # decrypt to $EDITOR, re-encrypt on save
```

Plaintext never lands on disk; edit decrypts to a temp buffer and re-encrypts.

## Frontend

`/secrets` lists keys only (values stay encrypted). `POST /secrets` adds/updates
and commits `SECRETS.yaml.gpg`. The frontend key signs ROI changes; honeybee
keys cannot (pre-receive hook rejects honeybee-authored ROI diffs).

## Bring your own key

Drop your secret key into `/etc/beehive/gnupg` before install. The installer
detects an existing secret key and skips keygen. Cross-submodule secret
isolation is out of scope — for strict separation run separate beehive repos
with separate keyrings.
