# Stash .strm fork — maintenance guide

This checkout is a fork of [stashapp/stash](https://github.com/stashapp/stash)
for a library that is streamed from Usenet through the nzbstream mount. The
custom work is a handful of commits on the `strm-support` branch (pushed as
`origin/strm-support-develop`), on top of upstream `develop`:

| Commit | What it does |
|---|---|
| Add .strm file support | `.strm` files (one `http(s)://` URL) are videos: ffprobe/ffmpeg run against the URL, playback is proxied to it |
| Lazy zip galleries | A picture inside a zip is registered from the zip's table of contents; it is read when first shown |
| Identify: batch stash-box lookups / no slow work in the write transaction | Identify asks a stash-box for 40 scenes at once and identifies 4 scenes side by side |
| Gallery scan: empty fingerprint list | Fix for archives inside zips being added to every gallery |
| Image thumbnails | Made in parallel, cheaper size, optional originals |
| Images list | Plain count and an index on the date added: the whole Images page opens in 60 ms with 3.7 million pictures |
| Docker build cache | A rebuild takes about 70 s instead of 9 minutes |
| registerRelease | nzbstream adds a finished import from a list of its files; nothing walks the folder |
| registerRelease: identify | The release's scenes are identified per stash-box, a grab's StashDB id is taken, ThePornDB's markers are added |

`FORK-PLAN.md` has the reasons and what was measured for the last four.
`git log upstream/develop..strm-support` lists the commits.

## Remotes

- `upstream` → official `stashapp/stash`. Pull releases from here.
- `origin` → (optional) your own GitHub fork, if you want a backup/push target.

## Updating to a new Stash version

One command does fetch + rebase + Docker build. **Target a release tag**, not
`develop` — release tags are immutable and are what users actually run:

```bash
./update-strm-fork.sh v0.31.1      # rebase onto release tag, build image
./update-strm-fork.sh              # rebase onto upstream/develop (not recommended)
./update-strm-fork.sh v0.31.1 norebuild   # rebase only, don't build
```

Find the newest tag with:
`git fetch upstream --tags && git tag -l 'v*' --sort=-version:refname | head`

The script replays **only your commits** onto the chosen ref (it detects
your commits by author email and uses `git rebase --onto`). This matters because
upstream release tags and `develop` diverge — a naive `git rebase <tag>` would
try to replay unrelated upstream commits.

The build uses Stash's own multi-stage Dockerfile, so it compiles the frontend
and backend inside Docker — **you only need Docker installed**, not Go or Node.
Result: an image tagged `stash-strm:<version>`.

### If the rebase conflicts

Most releases rebase cleanly because the patch touches small, stable functions.
When upstream edits the same lines, git stops and tells you which file. Then:

```bash
# edit the conflicted file, keeping both your change and upstream's
git add <file>
git rebase --continue
./update-strm-fork.sh <ref>        # re-run to build
```

`git rerere` is enabled, so once you resolve a given conflict, git replays that
resolution automatically on future rebases.

## Running the built image

Match the volumes to your existing stash container (config dir + media):

```bash
docker run -d --name stash-strm -p 9999:9999 \
  -v /home/ubuntu/p-stack/config/stash:/root/.stash \
  -v /path/to/your/media:/data \
  stash-strm:<version>
```

## Using .strm files

Create a plain-text file ending in `.strm` in your library, containing the URL:

```
http://altmount:8080/path/to/movie.mkv
```

(`#`-prefixed comment lines are ignored; the first real line is the URL.)
Then scan the library as usual.

## Known limitations

- File size shows 0 if the origin doesn't report `Content-Length` to ffprobe
  (cosmetic only).
- oshash/MD5 fingerprint the `.strm` text, not the remote video — fine for
  identity, but two `.strm` files pointing at the same video aren't detected as
  duplicates.
