# Stash .strm fork — maintenance guide

This checkout is a fork of [stashapp/stash](https://github.com/stashapp/stash)
that adds native `.strm` file support. All custom work lives in a **single
commit** on the `strm-support` branch, on top of an upstream release.

## What the patch does

- `.strm` files (plain text containing one `http(s)://` URL) are treated as
  videos. `ffprobe`/`ffmpeg` run against the URL inside the file.
- The scanned scene keeps the `.strm` file as its on-disk identity.
- Direct playback **302-redirects** the player to the URL (range requests go
  straight to the origin — e.g. Altmount / NZBDAV). Transcoding feeds the URL
  to ffmpeg.

Touched files (see `git show strm-support`):
`pkg/strm/` (new), `internal/manager/manager_tasks.go`,
`internal/manager/running_streams.go`, `pkg/ffmpeg/ffprobe.go`,
`pkg/ffmpeg/stream_transcode.go`, `pkg/ffmpeg/stream_segmented.go`,
`pkg/file/video/scan.go`.

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

The script replays **only your .strm commit** onto the chosen ref (it detects
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
