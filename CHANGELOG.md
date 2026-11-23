# Changelog

## [0.12.1](https://github.com/marshal-org/factorymeter/compare/v0.12.0...v0.12.1) (2026-11-23)


### Bug Fixes

* **measure:** count a change verified at its first passing round ([9daf8f6](https://github.com/marshal-org/factorymeter/commit/9daf8f619fed5ea03d5f61362f34d8748b61c9df))

## [0.12.0](https://github.com/marshal-org/factorymeter/compare/v0.11.0...v0.12.0) (2026-11-23)


### Features

* **measure:** read tickets through a Source ([#40](https://github.com/marshal-org/factorymeter/issues/40)) ([ab2f8d7](https://github.com/marshal-org/factorymeter/commit/ab2f8d77dc88ebee6f53a25eab383cd41ddf1b5a))

## [0.11.0](https://github.com/marshal-org/factorymeter/compare/v0.10.0...v0.11.0) (2026-11-23)


### Features

* **measure:** read records through a Source interface ([#38](https://github.com/marshal-org/factorymeter/issues/38)) ([8abbd29](https://github.com/marshal-org/factorymeter/commit/8abbd2986d4da9cf3bf7e957f537eca8dd4539c8))

## [0.10.0](https://github.com/marshal-org/factorymeter/compare/v0.9.0...v0.10.0) (2026-11-22)


### Features

* **spec:** a ticket in triage or the backlog is not waiting for the line ([2b4acdf](https://github.com/marshal-org/factorymeter/commit/2b4acdfdcee102c86ece5431e1e2ded43e818f3e))

## [0.9.0](https://github.com/marshal-org/factorymeter/compare/v0.8.0...v0.9.0) (2026-11-22)


### Features

* **serve:** drill into the items behind a number, pick dates, search repositories; fix years of writing time ([7f9ef59](https://github.com/marshal-org/factorymeter/commit/7f9ef59183d06f0f483b4322bf4175c585324f84))

## [0.8.0](https://github.com/marshal-org/factorymeter/compare/v0.7.0...v0.8.0) (2026-11-22)


### Features

* **page:** say PRs in every chart, not changes or items ([b8032c8](https://github.com/marshal-org/factorymeter/commit/b8032c849d700469871a8a28c36bac3b32dfb650))
* **serve:** tick several repositories in the page's repository menu ([4223e92](https://github.com/marshal-org/factorymeter/commit/4223e920c26264d0cf9a1da9346e31544daa3b00))

## [0.7.0](https://github.com/marshal-org/factorymeter/compare/v0.6.0...v0.7.0) (2026-11-22)


### Features

* pick repositories of an owner, serve opens the browser, clearer labels ([d7051c8](https://github.com/marshal-org/factorymeter/commit/d7051c8bf7b3c36d9720b8e486c90315edab8b51))


### Bug Fixes

* **collector:** read each commit's author as a field, cutting the cost of a pull-request page from 26 points to 1 ([fa57c82](https://github.com/marshal-org/factorymeter/commit/fa57c825295b15bc372c62b446967e0ba4d9bddb))

## [0.6.0](https://github.com/marshal-org/factorymeter/compare/v0.5.0...v0.6.0) (2026-11-22)


### Features

* **config:** keep the config in ~/.config/factorymeter by default ([#27](https://github.com/marshal-org/factorymeter/issues/27)) ([4a0ba0c](https://github.com/marshal-org/factorymeter/commit/4a0ba0cad5e979dc990c6a6c7b86bf9879acbf3c))


### Bug Fixes

* **init:** stop on Ctrl-C during a question; a second Ctrl-C always quits ([5cef808](https://github.com/marshal-org/factorymeter/commit/5cef8084a6613e63798d20d70f35a9e2539915f9))

## [0.5.0](https://github.com/marshal-org/factorymeter/compare/v0.4.0...v0.5.0) (2026-11-20)


### Features

* **cli:** factorymeter init asks a few questions and writes the config ([034885a](https://github.com/marshal-org/factorymeter/commit/034885adbd4f00b978ba238c325f361318ab1ae7))
* **cli:** factorymeter upgrade replaces the binary in place ([034885a](https://github.com/marshal-org/factorymeter/commit/034885adbd4f00b978ba238c325f361318ab1ae7))
* **cli:** store tickets lists each ticket with its pull requests ([034885a](https://github.com/marshal-org/factorymeter/commit/034885adbd4f00b978ba238c325f361318ab1ae7))
* **config:** read settings from a factorymeter.yml ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))
* install in one line, upgrade in place, and an init wizard ([034885a](https://github.com/marshal-org/factorymeter/commit/034885adbd4f00b978ba238c325f361318ab1ae7))
* **report:** list each ticket system with the keys it searches for ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))
* **site:** a one-line install script ([034885a](https://github.com/marshal-org/factorymeter/commit/034885adbd4f00b978ba238c325f361318ab1ae7))


### Bug Fixes

* **jira:** refuse a site name without a domain and read single errors ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))
* **linear:** take the last done time from the state history ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))
* **spec:** a key followed by a letter is not a ticket key ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))
* **spec:** a pull request opened after a ticket closed does not start it ([d1f339a](https://github.com/marshal-org/factorymeter/commit/d1f339a40c96ac944f1cf187f0852bd3fa95e8ec))

## [0.4.0](https://github.com/marshal-org/factorymeter/compare/v0.3.0...v0.4.0) (2026-11-19)


### Features

* **report:** link the line to Lights Out, and leave the first generation out of the help ([0ef67fe](https://github.com/marshal-org/factorymeter/commit/0ef67fe25bff36af5be836a800d13212f5a7b6a8))


### Bug Fixes

* **ci:** start no run on a release pull request that also moves the version pins ([13f4d05](https://github.com/marshal-org/factorymeter/commit/13f4d052f9976168e2ddec791b33dc85afb13911))
* **site:** let the pack job read the Pages settings ([30f27ba](https://github.com/marshal-org/factorymeter/commit/30f27ba5b1401085313ca7fd2eed4e869691c391))
* **site:** upload and publish the site in two jobs ([2291abd](https://github.com/marshal-org/factorymeter/commit/2291abdabbe78cf9e04593fec86f5080e4c8b21f))

## [0.3.0](https://github.com/marshal-org/factorymeter/compare/v0.2.0...v0.3.0) (2026-11-19)


### Features

* **page:** name the next station in line, tie where to invest to each station, and use the Software Factory colors ([421b70a](https://github.com/marshal-org/factorymeter/commit/421b70aa1f24e1f4f46abc7ac1fbd454b0d3b3ae))
* **page:** split the page into pages, grade each station, and open a station below the line ([b3fe2a8](https://github.com/marshal-org/factorymeter/commit/b3fe2a8a2ed7d77e98f0526889705e49bfd5004f))


### Bug Fixes

* **ci:** start no CI run on the release pull request ([1a25442](https://github.com/marshal-org/factorymeter/commit/1a25442501fe0ad788bdf8445f0e014300b9dc2b))
* **cli:** check the target and the flags before using them ([dad5a28](https://github.com/marshal-org/factorymeter/commit/dad5a2818a6856c3e156bedda97ab98c5eb39a87))

## [0.2.0](https://github.com/marshal-org/factorymeter/compare/v0.1.0...v0.2.0) (2026-11-19)


### Features

* **sync:** show what a sync will do, how far it is and how long is left ([f7370a1](https://github.com/marshal-org/factorymeter/commit/f7370a1485b7b1ee10b1681775ec9a35091b3a53))

## 0.1.0 (2026-11-19)


### ⚠ BREAKING CHANGES

* **report:** `factorymeter report` reads the local store and needs a `factorymeter sync` first. The command that reads GitHub directly and writes the HTML page is now `factorymeter snapshot`.

### Features

* **action:** keep the store between runs ([2d10faa](https://github.com/marshal-org/factorymeter/commit/2d10faac7e8e12e66f459a0fa792ece848687de7))
* **action:** run factorymeter as a GitHub Action ([78b7de4](https://github.com/marshal-org/factorymeter/commit/78b7de4f0935068801d63819e66fd4bf73ad183d))
* **contract:** line model v1, metric registry, generated reference and JSON Schema ([e44d007](https://github.com/marshal-org/factorymeter/commit/e44d007650dedf000b36ce8b948eae216d0f67eb))
* **jira:** read the tickets of a Jira Cloud site ([db0eb34](https://github.com/marshal-org/factorymeter/commit/db0eb34848631bea1854032deb512b1b7df98695))
* **linear:** read Linear tickets and measure the spec station from them ([691a1da](https://github.com/marshal-org/factorymeter/commit/691a1dabc6c574aa5b24dc51a90762fc77550b82))
* measure a delivery line from GitHub history ([c9c6281](https://github.com/marshal-org/factorymeter/commit/c9c62813d7e05eb39903006584ba34ea9e4dbf75))
* name failing workflows, read merge queue runs, match deploys by commit ([b1d3c5f](https://github.com/marshal-org/factorymeter/commit/b1d3c5f7ea2ced4609458d903a53e8927807f464))
* **report:** recommendations, sized against the line's own best four weeks ([7b276b5](https://github.com/marshal-org/factorymeter/commit/7b276b54b70489fb340be3e1a09a112cef307952))
* **report:** report the line by station from the store, with weekly history ([af12543](https://github.com/marshal-org/factorymeter/commit/af1254301b40da9b9c8ac7bd05b7b80363113054))
* **serve:** the report as a page, served from the store or written to one file ([d7cffba](https://github.com/marshal-org/factorymeter/commit/d7cffba59baf6e1c6c2892a745dfbba8314c87a7))
* **sync:** local Parquet store and incremental sync from GitHub ([f89a397](https://github.com/marshal-org/factorymeter/commit/f89a39736e2a613f22679a6407eb7e7c4e54f5dd))
* **tickets:** read GitHub issues and measure the spec station ([46cc745](https://github.com/marshal-org/factorymeter/commit/46cc74521577b7c34067393a3b11b38bd564c71f))
* **trend:** show how every number of the report changes over time ([c505c64](https://github.com/marshal-org/factorymeter/commit/c505c64b294b8b379e35fd9e78396bcacec2b8d4))


### Bug Fixes

* **metrics:** name no constraint from fewer than 5 merged pull requests ([368b17f](https://github.com/marshal-org/factorymeter/commit/368b17fe3c661fa2f53e501d38cec7962234252a))
* **release:** start at 0.1.0 ([b0fe586](https://github.com/marshal-org/factorymeter/commit/b0fe5864efc3e3b1f65ce9d8695b67020f505a77))
* **report:** the release station is measured from its first production ([7b276b5](https://github.com/marshal-org/factorymeter/commit/7b276b54b70489fb340be3e1a09a112cef307952))
* **sync:** an interrupted repository reads as stopped, not failed ([1b50666](https://github.com/marshal-org/factorymeter/commit/1b50666e16c9f2ffe28760e5d685a66295090c65))
