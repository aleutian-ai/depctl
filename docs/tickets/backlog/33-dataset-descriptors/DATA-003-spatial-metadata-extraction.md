# DATA-003: Spatial metadata extraction

**Epic:** Dataset descriptors
**Status:** backlog
**Depends on:** DATA-001 (descriptor convention)
**Estimated size:** medium

## Goal
When GDAL is available on the machine running `hack/fetch-geodata`, record basic spatial facts (CRS, layer names, geometry types, feature counts, field names) into `dataset.json` — enough for a downstream tool or an LLM prompt to know "9,129 California tract polygons, NAD83, fields GEOID/NAME/ALAND/AWATER, stored here" without opening the shapefile.

## Non-goals
- No GDAL dependency added to `depctl` itself or to `go.mod` — this shells out to `ogrinfo`/`gdalinfo` (or uses a Go GDAL binding) only from the `hack/fetch-geodata` dev tool, and only when found on `$PATH`.
- No support for every OGR-readable format — shapefiles and GeoPackages (what TIGER/NOAA ENC actually use) are enough; skip silently for anything else.
- Not a hard dependency: if GDAL isn't installed, `dataset.json` is written without the `spatial` section and the fetch still succeeds.

## Simplicity constraints
- Shell out to `ogrinfo -json`, don't hand-parse shapefile binary formats.
- One `spatial` object per dataset, not per layer file — most TIGER downloads are single-layer.

## Design
After extraction, if `exec.LookPath("ogrinfo")` succeeds, run `ogrinfo -json -al -so <extracted-dir-or-file>` and fold the relevant fields into `dataset.json`:

```json
{
  "spatial": {
    "crs": "EPSG:4269",
    "layers": [
      {
        "name": "tl_2025_06_tract",
        "geometry_type": "MultiPolygon",
        "feature_count": 9129,
        "fields": ["GEOID", "NAME", "ALAND", "AWATER"]
      }
    ]
  }
}
```

## Inputs / Outputs
- Input: the extracted dataset directory (or file) DATA-001 already locates.
- Output: `spatial` section added to that dataset's `dataset.json`, absent when GDAL isn't available or the format isn't OGR-readable.

## Failure behavior
`ogrinfo` failing or being absent is never a fetch failure — log and continue without the `spatial` section.

## Tests
- Given a small fixture shapefile and `ogrinfo` present (or a stubbed `ogrinfo` executable in `$PATH` for CI), `dataset.json` gets a populated `spatial` section.
- Given `ogrinfo` absent, `dataset.json` is written without `spatial` and the fetch doesn't fail.

## Acceptance criteria
- [ ] `spatial` section populated when GDAL is present and the format is OGR-readable.
- [ ] No fetch failure or panic when GDAL is absent.
- [ ] `hack/test-linux.sh`'s Alpine container (no GDAL by default) still passes without the `spatial` section.
