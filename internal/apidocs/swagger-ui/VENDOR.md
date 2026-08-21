# Vendored Swagger UI

These files are redistributed verbatim from Swagger UI. They are compiled into
the `gobbonet` binary by `go:embed` so `/docs` works with no internet
connection, which is the normal case for this app.

| | |
|---|---|
| Project | https://swagger.io/tools/swagger-ui/ |
| Version | **4.15.5** |
| License | Apache-2.0 (`LICENSE`, `NOTICE`) |
| Source | https://codeload.github.com/swagger-api/swagger-ui/tar.gz/refs/tags/v4.15.5 |
| Tarball integrity | `sha384-pOwNNBDnfD+zlf8LtM3994vvCyIaaFpiyRhCRs3FI2GJ/ESZcfZ2YbJ8E4ECf+R7` |

Only two of the eight dist files are here. The `.map` files add 2.3 MB and are
only useful when debugging Swagger UI itself; `swagger-ui-standalone-preset.js`
drives the URL bar and topbar, which `/docs` does not show.

`sha256` of what is committed:

```
fd76294e33356ab3fd111ddaeeb10d3f79de8ae1a4d34dbf777f5eef224648d9  swagger-ui-bundle.js
e883f234c6ef0b7dbb6d473fb45a00b85e98d58282f9dd1cc70bcc57ef12ef6a  swagger-ui.css
cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30  LICENSE
0d20d1adef18aee3f40dd258172155521ce702ac445cb5f7b7d60ed32dad2fb2  NOTICE
```

## Why 4.15.5 and not 5.x

This copy came off a local checkout of MediaWiki, which vendors the same files
under `resources/lib/swagger-ui/` and records the source URL and tarball
integrity above in `resources/lib/foreign-resources.yaml`. The machine that
assembled this commit had no network access, so 4.15.5 is what was verifiably
on hand rather than what is newest.

That choice has one consequence worth knowing: **Swagger UI 4.x does not render
OpenAPI 3.1**, so `openapi.json` is written to 3.0.3. Nothing in this API needs
a 3.1 feature — there are no webhooks and no schema keywords outside the 3.0
subset — so this costs nothing today. It would matter if the spec ever grew one.

## Upgrading

```sh
npm pack swagger-ui-dist@<version>
tar -xzf swagger-ui-dist-<version>.tgz
cp package/swagger-ui-bundle.js package/swagger-ui.css internal/apidocs/swagger-ui/
cp package/LICENSE package/NOTICE internal/apidocs/swagger-ui/
```

Then update the table above, re-run `sha256sum`, and run
`go test ./internal/server/ -run OpenAPI`. Moving to 5.x also unlocks writing
`openapi.json` as 3.1 if a reason to do so appears.
