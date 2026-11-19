# Changelog

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
