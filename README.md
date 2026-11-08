# EmoteHarvest

Downloads every custom emoji and sticker from a Discord server, straight from Discord's own CDN. No browser, no third-party scraper, no hosted service.

```
output/
└── Arch Linux Community/
    ├── emojis/
    │   ├── penguin.webp
    │   └── party_blob.gif
    ├── stickers/
    │   ├── angry_cat_stare.png
    │   ├── wave.json
    │   └── dance.gif
    ├── guild.json
    └── errors.log          only when something failed
```

## Quick start

1. **Get the binary.** Build it with `go build -o emoteharvest .` (Go 1.26.5+), or grab a release if one is published.
2. **Get a token.** See [Getting a token](#getting-a-token). Takes about a minute, once.
3. **Run it.** Double-click the binary, or run `./emoteharvest`. It asks for the server and the token, then downloads.

One self-contained binary for Windows, Linux, and macOS — no runtime and no external tools to install. Windows appends `.exe` to the build output; every example below works the same either way.

## Getting a token

Discord only exposes a server's emoji list to authenticated callers — there is no public endpoint. A token from a throwaway application is enough; the bot never connects, never comes online, and is never hosted anywhere.

1. Open <https://discord.com/developers/applications>
2. **New Application** → give it any name → **Create**
3. Switch to **Bot** → **Reset Token** → **Copy**
4. Paste it somewhere the tool can read it:
   - enter it when the program asks, or
   - save it in a `.env` file next to the binary: `DISCORD_TOKEN=your-token-here`, or
   - set the `DISCORD_TOKEN` environment variable
5. **Private servers only:** **OAuth2 → URL Generator** → check `bot` → open the generated URL and add the bot to the server. Public servers work without this.

> **Keep the token private.** It grants full control of the application. Never commit `.env`, never paste it into chat or an issue. Revoke it any time with **Reset Token** on the same page.

## Usage

```console
$ emoteharvest                               # interactive prompt
$ emoteharvest archlinux                     # invite code
$ emoteharvest https://discord.gg/archlinux  # invite link
$ emoteharvest 1145481614856556706           # server ID
$ emoteharvest -force -workers 10 archlinux  # replace existing files
```

Flags may appear before or after the server, so `emoteharvest archlinux -force` works too. Everything after a bare `--` is treated as positional.

| Flag | Default | Meaning |
|---|---|---|
| `-token` | — | Bot token. A pasted `Bot ` prefix is stripped automatically |
| `-out` | `output` | Root output directory |
| `-workers` | `5` | Concurrent downloads |
| `-refresh` | off | Ignore the cached `guild.json` and ask Discord again |
| `-force` | off | Re-download files that already exist |
| `-quiet` | off | No progress bar |
| `-version` | — | Print version, commit, and build time, then exit |

**Token, first match wins:**

1. `-token <token>`
2. second positional argument: `emoteharvest archlinux <token>`
3. `DISCORD_TOKEN` environment variable
4. `DISCORD_TOKEN` in `.env` in the working directory

Exit codes: `0` success, `1` failure, `2` bad flags.

### Server ID

Invite links expire and can be revoked; the numeric server ID cannot. Discord hides it until you turn on Developer Mode, in the desktop app:

1. **User Settings** (the gear) → **Advanced** → enable **Developer Mode**
2. Right-click the server in the sidebar → **Copy Server ID**
3. Pass that number to the tool: `emoteharvest 1145481614856556706`

## Re-runs

The folder is named after the server and is safe to re-run:

- Files already on disk with a non-zero size are skipped.
- `guild.json` caches the emoji and sticker list, matched by server ID, so renaming the folder does not break the cache. Use `-refresh` after the server changes.
- `-force` re-downloads everything. It does **not** delete an older file in a different format, so a folder can end up with both `name.png` and `name.gif`.
- Interrupted downloads never leave a half-written file — data goes to a temp file and is renamed into place.

## Notes

- **User tokens** are detected and supported: they are sent bare rather than with a `Bot ` prefix. Using one is a self-bot, which violates Discord's Terms of Service — the tool will say so once and continue.
- **Private servers** need the bot added to them; the public preview endpoint is used first, and the member-only endpoints are the fallback.
- **Animated stickers** are APNG and are saved as `.png`. Browsers and Discord animate them; Windows Photos and Explorer show only the first frame.
- Rate limits are handled reactively (`429` with `Retry-After`, three attempts on `5xx`); there is no proactive throttle.
- This project is not affiliated with Discord.

## Troubleshooting

| Message | Cause | Fix |
|---|---|---|
| `missing bot token` | no token found anywhere | see [Getting a token](#getting-a-token) |
| `discord rejected the token (HTTP 401)...` | token is wrong, revoked, or from a deleted application | **Reset Token** and paste the new one |
| `guild preview unavailable and emoji listing failed (is the bot in the server?)` | private server, bot not a member | invite the bot, or run against a public server |
| `invite "x" not found` | expired or mistyped invite | use the [server ID](#server-id) instead |
| `invalid invite or guild ID "x"` | input was neither a Snowflake nor an invite code | copy the invite again or use the ID |
| `rate limited by Discord` | too many requests in a short window | wait a minute, lower `-workers` |
| Console window closes before you can read it | that run was started with arguments, e.g. from a shortcut | run it from a terminal, or launch it with no arguments to get the pause |

## License

[MIT](LICENSE)
