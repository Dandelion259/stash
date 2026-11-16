# Stash fork: what we change

Written 2026-11-16, revised the same day as the work went on. Each change says what was built and what was checked.

## Decision

We keep Stash and patch our Go fork (`strm-support` branch). We do not rebuild
Stash in Rust.

Four changes, in this order:

1. Make the Images page fast.
2. NzbStream registers new releases in Stash at import. No scan.
3. NzbStream calls Stash directly. The watcher goes away.
4. Identify at grab time: a release grabbed for a known StashDB scene keeps
   that identity.

## Status (2026-11-16, evening)

All four changes are built, checked on a scratch copy of the library and live.

| What | State |
|---|---|
| Images page | Live. 6.1 s → about 60 ms on the server. Android app v0.24.1 opens Images the same way. |
| `registerRelease` in Stash | Live in image `stash-strm:register`. |
| NzbStream sends finished imports to Stash | Live in NzbStream 0.5.0, for the categories `porn` and `images`. |
| Identify and ThePornDB markers inside Stash | Live. |
| Grab-time StashDB id | Live in stash-streamer, NzbStream and Stash. Checked on the scratch copy; no real grab has gone through it yet. |
| `nzbstream-stash-watcher.service` | Stopped and disabled. The script is still in `~/p-stack/scripts`. |

Live check after the switch: one existing release was re-imported. NzbStream
registered it in Stash 1.7 s after the import finished, Stash identified it
against both stash-boxes, and the scene came out unchanged.

NzbStream went from 0.4.5 to 0.5.0 on this server, which also brought in 0.4.6
to 0.4.9 (released on GitHub on 2026-11-08, not deployed here before). Seeks
measured the same before and after. The 0.4.5 image is kept as
`nzbstream:0.4.5-before-0.5.0`.

To go back: put `stash-strm:images` back in `~/p-stack/docker-compose.yml`, set
`enabled = false` under `[stash]` in `~/nzbstream/config/nzbstream.toml`, and
`sudo systemctl enable --now nzbstream-stash-watcher`.

## Where we were before (measured 2026-11-16)

| What | Today |
|---|---|
| New video release: Stash scan | 14 ms to 3.0 s per release, usually about 0.2 s (9 scans in the log) |
| New video release: import finished → identified, markers in | 3 to 4 s |
| Cold seek, time to first byte | 51 ms through Stash, 52 ms straight from NzbStream (8 scenes) |
| Cold seek, 4 MiB delivered | 144 ms through Stash, 95 ms straight from NzbStream |
| Page of 40 scenes (server) | 107 ms |
| Page of 40 galleries / performers (server) | 22 ms / 21 ms |
| Pictures of one gallery (server) | 4 ms |
| Images page, all pictures (server) | 6.1 s for the list, plus 2.1 s + 2.1 s for the totals line |
| Scenes without a StashDB id | 16,582 of 48,265 |

The very first images query of the session took 103 s. I saw this once and did
not look into the cause.

Library size: 48,265 scenes, 14,225 galleries, 3,737,607 pictures, 5,552
performers. The database is 2.15 GB.

Re-measure with `~/p-stack/scripts/stash_playback_baseline.py` (read-only, takes
about a minute).

## Change 1: make the Images page fast

### Why it is slow

The Images page asks for all 3.7 million pictures, sorted by path, on every
page load. Four separate costs:

| Part | Time | Cause |
|---|---|---|
| Sort by path (the UI default) | about 5.0 s | Sorts on folder path glued to file name, with natural-order comparison. No index can serve that, so all rows are joined and sorted. |
| Count | 1.0 s | Stash runs `COUNT(DISTINCT id)` over a subquery. A plain `COUNT(*)` on the same table takes 88 ms. |
| Sort by date added | 1.2 to 1.4 s | `images.created_at` has no index: full scan plus sort. Sorting by id takes 3 ms. |
| Totals line (megapixels, file size) | 2.1 s each | Sums over every picture and its file, sent as a second query (`FindImagesMetadata`). |

Code: `pkg/sqlite/image.go`, `setImageSortAndPagination` (sort) and
`queryGroupedFields` (count and totals).

### Steps

1. **Count shortcut.** When the images query has no filter and no join, count
   with `SELECT COUNT(*) FROM images`.
   Expected: 1.0 s → about 0.1 s.
2. **Index on `images.created_at`.** Created at startup with
   `CREATE INDEX IF NOT EXISTS`, not as a numbered migration, so our schema
   version stays the same as upstream's and rebases stay clean.
   Expected: 1.4 s → a few ms. To confirm with `EXPLAIN QUERY PLAN` that Stash's
   real query uses it.
3. **Default order.** Make "newest first" the saved default for the Images
   page (a UI setting, no code). Pictures inside a gallery keep their file-name
   order; that is a different, fast query. See open question 1.
4. **Totals line.** Do not request `FindImagesMetadata` for an unfiltered list.
   Filtered lists keep it. Decide after steps 1 to 3 whether it still matters.

### How we check it

- Each step first runs against a copy of the database in a scratch Stash
  container, never the live one.
- Same query before and after must return the same ids and the same count.
- Target: the images row of the baseline script under 300 ms.

## Change 2: NzbStream registers releases in Stash at import

### Today

NzbStream finishes an import → the watcher hears it → asks Stash to scan the
release folder on the mount → Stash walks the folder, reads 128 KB per file for
the hash, runs ffprobe on each video, reads each zip's table of contents.

### After

NzbStream sends Stash one message per release that lists its files. Stash
creates the scenes and galleries from that list. No scan job, no walk over the
mount.

### Stash side (fork), as built

- **New mutation `registerRelease`.** Input: the release folder as Stash sees
  it (`/mnt/nzbstream/<category>/<release>`) with its modified time, and every
  file and folder below it: path, size, modified time, and the oshash of videos
  and zips.
- **It reuses the stock scan code.** The mutation runs Stash's own scanner over
  the list instead of over the mount (`pkg/file/listed_fs.go`). Scene, gallery
  and image creation, rename detection and plugin hooks are the stock code.
- **Size and modified time are stored exactly as the mount reports them**, so a
  normal scan later sees every registered file as unchanged.
- **It runs at once**, next to whatever job is running, and answers with the
  scene and gallery ids when it is done. Stash's own "start a job now" function
  turned out to be unused upstream and broken (it left finished jobs in the
  queue, which blocked every later job), so the fork has its own
  (`pkg/job/run.go`).

Two things differ from the first version of this plan:

- **Stash still reads each zip's table of contents itself.** NzbStream does not
  send the list of pictures. The names and times have to be exactly what Stash's
  own zip reader produces (it guesses the character set of old zips), or the
  pictures could not be opened later.
- **Stash still runs ffprobe on each video, through the mount.** Not over HTTP:
  the mount path needs no extra setting in Stash and gives the same values as a
  scan by construction.

So what a registered release saves is the walk over the folder and the 128 KB
hash read per video or zip, and it no longer waits behind other jobs.

### NzbStream side, as built

- **oshash at import** for videos and zips (the size plus 64 KiB from each end).
- **The file list** comes from the catalogue, with the modified times the mount
  reports.

### Checked on the scratch copy (2026-11-16)

Four releases (one video, a six-video pack, a zip gallery of 397 pictures, a
folder of 40 loose pictures), each scanned on one copy and registered on
another, with `~/p-stack/scripts/stash_register_check.py`:

- The file, folder, fingerprint, scene, gallery and image rows are the same.
- A normal scan afterwards changes nothing. Registering twice changes nothing.

Registering was not faster than scanning in these runs: 0.7 to 3.6 s against
0.2 to 2.0 s per release.

## Change 3: NzbStream calls Stash directly

### Today

A Python service (`nzbstream-stash-watcher`) listens to NzbStream's event
stream. NzbStream does not replay missed events, so on every reconnect the
watcher walks the download history to catch up. The watcher does four jobs:

1. asks Stash to scan,
2. batches gallery scans until Stash is idle,
3. runs Identify once per stash-box with your saved options,
4. imports ThePornDB markers after the ThePornDB pass.

### After, as built

- **`[stash]` section in NzbStream's config:** URL, API key, on/off, the
  categories that are Stash libraries.
- **Outbox in NzbStream's catalogue.** Each finished import adds a row. A
  sender works through the rows and deletes one when Stash has answered. If
  Stash is down or refuses, the row stays and is tried again (after 10 s,
  doubling up to 10 minutes). This survives a restart of either side, so no
  catch-up walk is needed. `GET /api/stash/outbox` shows what is waiting.
- **Jobs 1 and 2 disappear** with change 2.
- **Job 3 is one job inside Stash.** `registerRelease` with `identify: true`
  queues it: one Identify pass per stash-box with your saved options.
  ThePornDB is asked last, as the watcher did, so where both know a scene its
  values are the ones kept.
- **Job 4 runs in that same job**, right after the ThePornDB pass, with the
  watcher's rules (markers within 15 s of each other become the earliest one, a
  marker at 0 s is left out, the name is the primary tag).
- **`/api/events` stays**, with a new `registered` event after each success.

One thing is better than before: a scene that only StashDB knows is now marked
organized too. The watcher could only set that in its last pass (ThePornDB).

### Checked on the scratch copy (2026-11-16)

With `~/p-stack/scripts/stash_register_identify_check.py`, against the live
Stash, which got the same releases through the watcher:

- A single video: both stash ids, title, date, studio, performers, tags and
  organized as live.
- A six-video pack: the one scene the stash-boxes knew has its 18 ThePornDB
  markers exactly as live. One more scene is identified now that live is not;
  ThePornDB has learned it since.
- NzbStream's own tests cover the outbox: a refused release stays and is sent
  again, rows waiting at start are sent, a deleted release is dropped.

## Change 4: identify at grab time

### Today

When you grab a scene from StashDB, stash-streamer writes the StashDB id and
NzbStream's job id to `pending_jobs.json`. Nothing on the NzbStream path reads
that file. The new scene is identified by file hash only. If StashDB does not
know that file's hash, the scene stays unidentified although we knew which
scene it was.

16,582 of 48,265 scenes have no StashDB id. I did not measure how many of
those were grabbed for a known id.

### After, as built

- **stash-streamer sends the id with the grab**, as two extra parameters on
  NzbStream's add call, but only for a release its ranking is "sure" of.
- **NzbStream stores them** with the queue row and puts them in
  `registerRelease`.
- **Stash takes that scene directly.** For a release of one scene it looks the
  StashDB scene up by id and applies it through the normal Identify code with
  your saved options. ThePornDB is still matched by hash.
- **Grabs without an id** (Hydra, manual NZBs, uploads) work as before.

### Guard against a wrong grab

- Only a release the ranking is "sure" of carries the id.
- Stash compares the file's duration with the durations StashDB has for the
  scene (its own and those of the files submitted for it). If StashDB has any
  and none is within 15 s (or 1% for long scenes) of the file's, the id is not
  used and the hashes decide as before.
- A pack never gets an id applied.

### Checked on the scratch copy (2026-11-16)

A video registered with a hash no stash-box knows:

| Sent with the grab | Result |
|---|---|
| no id | not identified |
| the scene's StashDB id | identified, same title, studio and performers as live |
| the id of a scene 158 s longer | not identified, falls back to hashes |

## Order of work

| Step | What | Depends on |
|---|---|---|
| 1 | Images page (change 1) | nothing |
| 2 | `registerRelease` in the fork, called by a test script | nothing |
| 3 | oshash and zip lists in NzbStream, outbox, direct call | 2 |
| 4 | Identify passes queued by `registerRelease` | 3 |
| 5 | StashDB id travels with the grab and is applied | 3 |
| 6 | ThePornDB markers in the fork, watcher retired | 4 |

Every step is tried on a scratch Stash with a copy of the database before the
live one. Each fork step is its own commit so it rebases on its own.

## Rebase cost

Changes 2 to 4 are the largest patch on the fork so far. Most of it is new
files, which do not conflict. The parts that touch upstream code, and can
conflict on a rebase:

- `pkg/sqlite/image.go` (change 1),
- `pkg/file/video/scan.go` (change 2),
- `internal/manager/task_identify.go` and `internal/identify/` (changes 3
  and 4; we already carry two patches there),
- the GraphQL schema (one new mutation).

## Not building

| Idea | Why not |
|---|---|
| Stash's web player streams straight from NzbStream | First byte is the same either way. The Android app and the Android web UI (via `custom.js`) already play direct. |
| Path order across all 3.7 million pictures made fast | Needs a rewrite of that sort. "Newest first" avoids it. |
| Removing a release in NzbStream also removes it in Stash | Not asked for. Stash's Clean task does this today. |

The warm-up floods on 2026-11-13 came from a script reading many files over the
mount at once, not from scans. The fix for that is a cap inside NzbStream, which
is still open on NzbStream's own list.

## Measure before deciding

**Web UI feel in the browser.** The server answers a scenes page in 107 ms, so
if the UI feels slow the time is spent in the browser (8.2 MB of JavaScript, no
virtualised lists) or on thumbnails. This has not been profiled. One browser
profile of a scenes page and a gallery page comes first.

## Housekeeping found along the way

- **Session cookie in the log.** Fixed: the stash-streamer plugin wrote its raw
  input, including the Stash session cookie, to the Stash log on every call. The
  cookie is now left out of that line.
- **Old NZBDav watcher.** `nzbdav-stash-watcher.service` is running but handled
  no import in the last 7 days. See open question 2.
- **Fork README.** Fixed: `STRM-FORK-README.md` now lists the fork's commits.

## Open questions

1. **Images page order.** Answered: "newest first" is the default now.
2. **Old NZBDav watcher.** Stop and disable it, or is NZBDav still meant to feed
   Stash? Recommendation: leave it running until you confirm.
