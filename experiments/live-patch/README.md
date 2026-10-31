# Live patch, without the source

Experiment only. Nothing here is a Chasen command. The question was whether OS-package and base-image vulnerabilities in an image we already have can be fixed without the application's source, for a Rails app, a Node app, and a Go binary.

Short answer: **yes for OS packages that still have a fix, and no for everything else.** A future `chasen patch` should do that OS update on the machine that already builds images, then hand the result to the health-checked swap Chasen already has. It should say, in the same output, what it did not touch.

## Recommendation

Ship `chasen patch` only as a narrow OS update, and do not describe its result as a clean image.

Copa 0.15.0 and a plain `apt-get upgrade` / `apk upgrade` layer cleared the same fixable OS findings on every image where both ran. The app kept answering `/up` and `/` afterwards, including Rails with native gems and Node linked against musl. Rebase onto a newer base (`crane rebase`) also kept the apps answering, and it is the only one of the three that replaced the Ruby interpreter and the Node binary. It is also the one that can leave fixable OS findings behind, because it can only be as new as the base image someone has published.

Run it where `chasen deploy` already builds: the developer's machine or CI. The server pulls the new image and does the swap it does today. A new container is started, the proxy waits until `/up` returns 200, and the old container is removed only after traffic has moved. If the check fails, the old container is still the one serving. A private app has no proxy, so the old container is started again when the new one does not come up healthy. That rollback is the safety net. The patch step does not get its own.

Do not run BuildKit on the small server. The patch itself was cheap once the image was local (under 20 seconds, about 450 MB of extra process memory on the Rails image), but it needs the Docker socket, which is root, a BuildKit daemon, and outbound network to the distro mirrors and to Copa's tooling image. A server that is already running apps should not grow that.

The alert after a patch has to be specific. "0 high CVEs" was the wrong sentence several times in this run.

| What the scan shows | What to say |
|---|---|
| OS package, fix available, distro still supported | Patchable here. Copa and `apt`/`apk` both did it. |
| OS package, no fixed version (`affected`, `fix_deferred`) | No package to install. Debian left 57 high/critical findings in that state. |
| `linux-libc-dev` | Header package, not the kernel that is running. On the Ruby image it was 1260 of 1385 high/critical findings. Upgrading it does not patch the host. |
| Official Node binary, or Ruby's own interpreter | Not a distro package. `apt`/`apk`/Copa left Node at v20.11.1 and Ruby at 3.2.4. Rebase, or rebuild the image. |
| OpenSSL inside official Node (`3.0.13+quic`) | Bundled in the Node binary. The Debian `libssl3` update did not change it. |
| OpenSSL that Ruby's `openssl.so` loads | This one is Debian `libssl3`. Copa and `apt` moved the loaded library from 3.0.13 to 3.0.22. Ruby's own version string stayed 3.0.13, so the banner is the wrong thing to check. |
| npm packages, Ruby gems | Untouched by an OS patch. The Express tree stayed at 30 high/critical. Rails 7.1.3 and its default gems stayed vulnerable except where rebase replaced gems that live in the Ruby image itself. |
| Go standard library, or a module compiled into the binary | Rebuild the binary. All three approaches left Go 1.21.13 and `golang.org/x/net` v0.17.0 exactly as they were. |
| `scratch` | No packages. Copa exited 0, printed that no update was found, and produced no image. |
| Distroless static | Copa saw `ca-certificates` and `tzdata` and then failed: their maintainer scripts need `debconf` and `openssl`, which the image does not have. |
| End-of-life distro | Alpine 3.19 still served packages, and the six high findings Trivy tracks went to zero. Trivy also said the release is unsupported and its data may be incomplete. The alert is "rebuild on a supported base", not "clean". |

## Results

Tools, pinned: Trivy 0.76.1 (vulnerability DB updated 2026-10-31 07:07 UTC), Copa 0.15.0, crane 0.22.1, Docker 29.1.3 with Buildx 0.30.1. Sizes are what `docker images` reported. Times are the patch step only, with the image and the Trivy DB already on disk. "Healthy" means the container stayed up, `GET /up` returned 200, and `GET /` returned 200 with a body from the app.

OS counts are high and critical findings. "Fixable" means Trivy had a fixed package version. App-dependency counts are the same severities.

| App | Approach | OS high/critical (fixable) | Runtime | App dependencies | Healthy | Size | Time |
|---|---|---|---|---|---|---|---|
| Rails 7.1.3 on `ruby:3.2.4-slim-bookworm` | original | 1385 (1141), of which 1260 are `linux-libc-dev` | Ruby 3.2.4, loaded OpenSSL 3.0.13 | 16 gems | yes | 393 MB | |
| | Copa | 244 (0), of which 184 are `linux-libc-dev` | Ruby 3.2.4, loaded OpenSSL 3.0.22 | 16 gems | yes | 567 MB | 16.7 s |
| | `apt-get upgrade` | 244 (0), same header split | same as Copa | 16 gems | yes | 526 MB | 13.8 s |
| | rebase onto `ruby:3.2-slim-bookworm` | 89 (31), header package gone | Ruby 3.2.11, loaded OpenSSL 3.0.18 | 13 gems | yes | 345 MB | 0.6 s |
| Node on `node:20.11.1-bookworm-slim` | original | 92 (35) | Node v20.11.1, bundled OpenSSL 3.0.13+quic | 30 npm | yes | 310 MB | |
| | Copa | 57 (0) | unchanged | 30 npm | yes | 412 MB | 11.9 s |
| | `apt-get upgrade` | 57 (0) | unchanged | 30 npm | yes | 386 MB | 9.3 s |
| | rebase onto `node:20-bookworm-slim` | 74 (17) | Node v20.20.2, bundled OpenSSL 3.0.19 | 30 npm | yes | 307 MB | 1.1 s |
| Node on `node:20.11.1-alpine3.19` (past support) | original | 6 (6). Trivy: detection may be incomplete | Node v20.11.1, bundled OpenSSL 3.0.13+quic | 30 npm | yes | 211 MB | |
| | Copa | 0. Same Trivy warning, still Alpine 3.19.1 | unchanged | 30 npm | yes | 225 MB | 3.5 s |
| | `apk upgrade` | 0. musl and libssl moved within 3.19 | unchanged, process still started | 30 npm | yes | 226 MB | 2.0 s |
| | rebase onto `node:20-alpine` (Alpine 3.23.4) | 4 (4) | Node v20.20.2, bundled OpenSSL 3.0.19 | 30 npm | yes | 210 MB | 1.1 s |
| Go static binary on `alpine:3.22.0` | original | 21 (21) | Go 1.21.13 stdlib, 27 | `golang.org/x/net` v0.17.0, 8 | yes | 23.4 MB | |
| | Copa | 0 | unchanged | unchanged | yes | 39.3 MB | 2.4 s |
| | `apk upgrade` | 0 | unchanged | unchanged | yes | 40.8 MB | 1.8 s |
| | rebase onto `alpine:3.22` (3.22.7) | 0 | unchanged | unchanged | yes | 23.4 MB | 0.6 s |
| Go static binary on `debian:bookworm-20240110-slim` | original | 98 (41) | same Go binary | same module | yes | 125 MB | |
| | Copa | 57 (0) | unchanged | unchanged | yes | 229 MB | 11.8 s |
| | `apt-get upgrade` | 57 (0) | unchanged | unchanged | yes | 202 MB | 8.9 s |
| | rebase onto `debian:bookworm-slim` (12.15) | 57 (0) | unchanged | unchanged | yes | 124 MB | 0.8 s |
| Go on `gcr.io/distroless/static-debian12:nonroot` | original | 0 high/critical (4 unknown, on ca-certificates and tzdata) | same Go binary | same module | yes | 16.8 MB | |
| | Copa | no image. `dpkg` postinst failed for ca-certificates and tzdata | | | | | 11.9 s, then failure |
| Go on `scratch` | original | none | same Go binary | same module | yes | 10.7 MB | |
| | Copa | no image. Exit 0, "no package updates found" | | | | | 0.2 s |

Copa was run with its default, OS packages only, from the Trivy JSON. It reported patching every OS finding that had a fix and skipping the rest (Debian: 136 patched, 230 skipped, which matches Trivy's fixed and unfixed counts). The upgrade layer was not more thorough. Where the two differ is size: both add a layer, so the image grows, and Copa's layer was the larger of the two on these images. Rebase replaces the base layers instead of stacking a copy, so the image stayed the same size or shrank.

## What actually moved

**OS packages on a supported distro.** Alpine 3.22 went from 21 high/critical to 0 by all three methods. Debian Bookworm went from 41 fixable to 0 fixable. The findings that remained on Debian have no fixed package (util-linux and its libraries, ncurses, gzip, and others, marked `affected` or `fix_deferred`).

**The library a process is linked against, when it is a distro library.** Ruby's `openssl.so` is linked to Debian `libssl.so.3`. After Copa and after `apt-get upgrade`, `dpkg` and `OpenSSL::OPENSSL_LIBRARY_VERSION` both said 3.0.22. `OpenSSL::OPENSSL_VERSION` still said 3.0.13, because that string is from the headers Ruby was compiled with.

**Not the official Node binary.** `ldd` on `/usr/local/bin/node` shows libstdc++ and libc, not libssl. `process.versions.openssl` stayed `3.0.13+quic` after Copa and after `apt`. Rebase onto `node:20` replaced the binary: v20.20.2, OpenSSL 3.0.19. Trivy 0.76.1 never reported the Node runtime as a package. Its "Node.js" target is the npm tree (`tar`, `brace-expansion`, `lodash`, `path-to-regexp`, and the rest of Express 4.17.1).

**Not the Ruby interpreter, except by rebase.** Copa and `apt` left Ruby 3.2.4 in place. Rebase onto the floating `ruby:3.2-slim-bookworm` tag (built 2026-04-07) moved it to 3.2.11. Native gems built on 3.2.4 still loaded, and Rails answered. That is one data point, not a promise: the gems were nokogiri's precompiled build plus extensions compiled on this old image (`bigdecimal`, `date`, `nio4r`). A rebase across a minor version was not tried.

Rebase was behind apt on the shared libraries. The Ruby image from April 2026 carries OpenSSL 3.0.18 and still has 31 fixable OS findings. The Node image from 2026-04-22 is Debian 12.13 and still has 17. Apt and Copa, talking to today's Bookworm security archive, got the fixable count to zero and loaded OpenSSL 3.0.22. So rebase updates the runtime, and a package upgrade updates the distro past the last published language image.

**Not gems that were installed by Bundler.** Rails 7.1.3, activestorage, actionpack, activesupport, erb, and net-imap stayed vulnerable. Rebase dropped three findings (`rexml`, `uri`, `zlib`) because those default gems live in the Ruby image and the newer image has newer copies. net-imap moved from 0.3.4.1 to 0.3.9 and was still reported.

**Not anything compiled into the Go binary.** The binary is static (`CGO_ENABLED=0`, Go 1.21.13, built with `-buildvcs=false`). Trivy reads the build info: 26 high and 1 critical in the standard library, 8 high in `golang.org/x/net` v0.17.0. The same counts came back after every approach. The `/` body still said `go go1.21.13`.

**Scratch and distroless.** There is nothing to upgrade. Distroless static has a `status.d` directory, so Copa tried, and the package scripts failed closed. That is a better failure than a half-applied image. Scratch has no database; Copa said the image was already up to date and did not write a tag. The Go findings were still in the original image.

**An end-of-life Alpine.** `node:20.11.1-alpine3.19` is Alpine 3.19.1. The v3.19 indexes are still on `dl-cdn.alpinelinux.org`, so `apk` and Copa installed the last builds of that branch (musl, libssl 3.1.8, busybox). Node still started. Trivy kept the warning that 3.19 is unsupported and that its report may be short. A zero in that column is not a clean image. Rebase onto current `node:20-alpine` left Alpine 3.19 entirely (the new base is 3.23.4) and was the first time the Node version changed. That new base still has 4 fixable high findings of its own.

## Host, and how Chasen would run it

What the patch needed on this machine:

- The Docker socket. Copa talks to the daemon's BuildKit (`--loader docker`). The socket is root.
- Outbound network. Apt and apk fetch the distro archive. Copa also pulls `ghcr.io/project-copacetic/copacetic/debian:12-slim` (and the Alpine equivalent) as a tooling image. Trivy pulls its vulnerability DB.
- Disk for a new layer. On these images Copa added roughly 16 MB (Alpine Go) to 174 MB (Rails). The upgrade layer was in the same range. Rebase did not add a layer.
- Little CPU once the bytes were local. The slow work was the first pull of the base images, not the patch. Warm patch times were 1 to 17 seconds.
- Memory. During the Rails re-patch the sum of RSS for dockerd, containerd, and the patch processes peaked around 450 MB. That is the warm case. A cold BuildKit plus a large apt download on a 1 GB server that is already serving traffic can still push it over.

`crane rebase` does not read the Docker daemon. The image was pushed to a throwaway `registry:2` on 127.0.0.1:5000, rebased with `--platform linux/amd64`, and pulled back. The old and new bases were the public tags. Images were built with `--provenance=false` so each one is a single manifest. BuildKit's default attestation wraps the image in an index, and rebase wants one image's layers.

A `chasen patch` that fits the rest of the tool:

1. Scan with a pinned Trivy, on the machine that builds. Keep the DB time in the output.
2. Refuse, with the sentences in the table above, when the image is scratch or distroless, when the distro is past support, or when the only findings are a Go binary, a gem, or an npm package.
3. Apply OS updates with a pinned Copa. The plain upgrade layer matched its CVE result and is the fallback when Copa cannot parse the image. Do not upgrade packages the report did not name if Copa is available.
4. Start the patched image and require `/up` and one real request, the same bar as a deploy. Copa does not do this.
5. Tag the result as a new image. `chasen deploy` of that tag is the rollout: the server pulls, the engine health-checks, and the previous image is the rollback. Chasen already keeps the newest images of an app for that.
6. Print the remainder in the same command. Fixable OS count before and after, the runtime version before and after (`node -v`, `ruby -v`, `OpenSSL::OPENSSL_LIBRARY_VERSION`, `runtime.Version`), and the dependency findings that were not touched. A Ruby banner that still says 3.0.13 while the loaded library is 3.0.22 has to be explained there, or the next scan looks like the patch did nothing.

Rebase is a separate, riskier step, not the default of `chasen patch`. Use it when the finding is the runtime that was copied into the image, and only onto a newer image of the same minor version. It needs the original base layers. It can move Node and Ruby, and here the apps survived, and it can also be months behind the distro archive.

## How this was built

The apps are under `apps/`. They are deliberately old.

- Rails 7.1.3, Webrick, on `ruby:3.2.4-slim-bookworm` (image created 2024-06-25). Gems were compiled in a throwaway stage pointed at `snapshot.debian.org` for 2024-06-26, so the final image's packages are the ones in that Ruby image and not today's archive. The final stage does not run apt.
- Node, Express 4.17.1 and lodash 4.17.20, on `node:20.11.1-bookworm-slim` (2024-03-12) and on `node:20.11.1-alpine3.19`.
- One static Go binary, module `example.com/livepatch`, Go 1.21.13, `golang.org/x/net` v0.17.0, copied onto Alpine 3.22.0, Debian Bookworm 2024-01-10, current distroless static, and scratch.

`scripts/pins.env` records the tags and the manifest-list digests resolved on 2026-10-31. `scripts/run.sh` builds, scans, patches, and checks `/up`. `scripts/summarize.py` counts Trivy's JSON. High and critical only in the table; the JSON has the other severities. Raw Trivy reports are not committed.

Limits. One small app each, no database, no login. Nokogiri was the precompiled Linux gem, so this did not exercise a from-source libxml build. Copa's experimental library mode and its Go binary rebuild were not turned on: the binary was built without VCS metadata, and the point was to see what an OS patch can do with no source. Times exclude the first image pull and the Trivy DB download.
