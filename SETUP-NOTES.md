# SETUP NOTES (macOS, Apple Silicon)

Practical, hard-won notes for building and running SpotiFLAC from source on an
Apple Silicon Mac with the vendored slskd (Soulseek) integration. These cover
things that are easy to get wrong and aren't obvious from the README.

## 1. The app MUST be built as native arm64 (not x86_64 / Rosetta)

This is the single most important gotcha. If the Go toolchain is x86_64, the
whole app compiles for Intel and runs under Rosetta — and **WKWebView media
playback under Rosetta crashes the renderer** (audio playback black-screens the
app). The project's `build-macos.sh` even refuses to build when it detects a
Rosetta Go (`check_architecture_alignment`).

On many Apple Silicon machines Homebrew lives at **`/usr/local` (the Intel
prefix, running under Rosetta)**, so `brew install go` gives an **x86_64** Go.
Check with:

```bash
go env GOHOSTARCH   # must print arm64, NOT amd64
file $(which go)    # must say arm64
```

If it's `amd64`, install a native arm64 Go side-by-side (no need to touch your
Intel Homebrew):

```bash
curl -fsSL -o /tmp/go-arm64.tar.gz https://go.dev/dl/go1.26.2.darwin-arm64.tar.gz
rm -rf "$HOME/go-arm64" && mkdir -p "$HOME/go-arm64"
tar -xzf /tmp/go-arm64.tar.gz -C "$HOME/go-arm64" --strip-components=1
"$HOME/go-arm64/bin/go" version   # -> go ... darwin/arm64
```

Then always build with that Go first on PATH:

```bash
PATH="$HOME/go-arm64/bin:$PATH" GOROOT="$HOME/go-arm64" ./build-macos.sh
```

Output: `build/bin/SpotiFLAC.app` (verify `file build/bin/SpotiFLAC.app/Contents/MacOS/SpotiFLAC` says **arm64**).

Node/pnpm just need to be arm64-capable (they usually already are); only Go's
arch determines the app binary's arch.

## 2. .NET 10 for the vendored slskd

The checked-in `frontend/third_party/slskd` targets **net10.0** and the app
launches it at runtime (prefers a prebuilt apphost, else `dotnet run`).

```bash
brew install dotnet            # 10.0.x SDK
git submodule update --init --recursive   # if slskd isn't checked out
```

slskd is a separate HTTP server process, so it's fine for it to be **x86_64 /
Rosetta** (only the SpotiFLAC app itself must be arm64). Pre-build it once so
the app launches the fast apphost instead of `dotnet run`:

```bash
cd frontend/third_party/slskd
dotnet build src/slskd/slskd.csproj -c Release
# produces src/slskd/bin/Release/net10.0/slskd (apphost the app auto-detects)
```

### Runtime-discovery fix (Homebrew .NET)

Homebrew installs .NET to `/usr/local/opt/dotnet/libexec`, but the slskd apphost
probes `/usr/local/share/dotnet/x64`. Without a bridge it dies instantly with
"app launch failed" whenever it's started without `DOTNET_ROOT` (e.g. from a
Finder-launched `.app`). Fix once:

```bash
mkdir -p /usr/local/share/dotnet
ln -sfn /usr/local/opt/dotnet/libexec /usr/local/share/dotnet/x64
```

(`/usr/local/share` is writable without sudo under Intel Homebrew.)

## 3. slskd / Soulseek configuration — two different directories

This trips people up: there are **two** slskd-related dirs.

| Dir | What's there | Set by |
|-----|--------------|--------|
| `~/.spotiflac/slskd/` | `api-key.txt`, `downloads/` (staging), `incomplete/`, managed log | SpotiFLAC (the app manages this) |
| `~/Library/Application Support/slskd/` | **`slskd.yml`** (the real config: Soulseek creds, shares), `data/`, `logs/` | slskd itself (its default app dir) |

The app passes `APP_DIR` to slskd, but slskd ignores it and uses its own default
app directory — so **Soulseek credentials and shares go in
`~/Library/Application Support/slskd/slskd.yml`**, not in `~/.spotiflac/slskd`.

Minimal `slskd.yml` additions:

```yaml
soulseek:
  username: <your-soulseek-username>   # Soulseek auto-registers new accounts on first login
  password: <your-soulseek-password>

# Optional but recommended: share a folder so peers that gate on sharing don't
# reject you. This makes the folder browsable/downloadable by other Soulseek users.
shares:
  directories:
    - /Users/<you>/Music
```

slskd watches its config, but **adding credentials to an already-running
instance doesn't auto-connect** — it needs a restart (or `PUT
/api/v0/server` with the API key). On a fresh launch with valid creds in the
config it connects automatically. After editing `shares`, trigger a rescan:
`PUT /api/v0/shares`.

## 4. SpotiFLAC settings that must match slskd

In SpotiFLAC's `~/.spotiflac/config.json` (or the in-app Settings → slskd):

- `slskdURL` = `http://127.0.0.1:5030`
- `slskdAPIKey` = **the exact contents of `~/.spotiflac/slskd/api-key.txt`**
  (a stale/old key here is the usual cause of `slskd API returned status 401`).
- `slskdDownloadsDir` = `~/.spotiflac/slskd/downloads` — i.e. slskd's **staging**
  dir (where slskd writes completed transfers), NOT your final music folder.
  SpotiFLAC finds the file there and copies the tagged result to your main
  download folder (`downloadPath`). Pointing this at your music folder causes
  "slskd completed the transfer but the file was not found under ...".

The app auto-fills these from the managed instance **only when the field is
empty**, so an old value won't be overwritten — fix it by hand if needed.

## 5. Running / relaunching

```bash
# Dev mode (hot reload), still needs the arm64 Go on PATH:
PATH="$HOME/go-arm64/bin:$PATH" GOROOT="$HOME/go-arm64" \
  go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 dev

# Or run the built app bundle (it launches slskd itself):
open -n build/bin/SpotiFLAC.app
```

Gotcha: `open build/bin/SpotiFLAC.app` (without `-n`) just **focuses an already
running instance** instead of launching the freshly built binary. After a
rebuild, fully quit the old app (or `pkill -f SpotiFLAC.app/Contents/MacOS`)
and use `open -n` to guarantee you're running the new build.

First launch of slskd scans the shared folder, so it can take ~30–60s before
the API on `:5030` responds and it shows `Connected, LoggedIn`.
