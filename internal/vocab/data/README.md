# WD14 tagger vocabularies (pinned snapshots)

These CSVs are the `selected_tags.csv` vocabularies of the WD14 taggers that
captioned most LoRA training data. They are vendored as **immutable
snapshots**: the whole point is historical lookup (a tagger-era name that
current Danbooru has renamed away), so they are never refreshed in place.

## Plugin directory

`data/` is the vocabulary registry. Each `*.csv` file is one vocabulary and
its id is the file name minus the extension (`wd-eva02-large-tagger-v3.csv`
-> `wd-eva02-large-tagger-v3`) — there are no vocabulary-specific code touch
points.

- **Add a vocabulary**: drop the CSV here and add a row to the table below.
  `vocab.Default()` picks it up automatically;
  `TestDefault_PluginDirectoryIsTheRegistry` verifies the load.
- **Remove a vocabulary**: delete the CSV, its table row, and any pinned
  expectations in `vocab_test.go` that name it.
- **Update a snapshot**: replace the file in place and update its row below;
  changed counts trip the pins in `vocab_test.go` until re-verified.

| File | Vocabulary (HF dataset) | Tag rows | SHA256 |
| --- | --- | --- | --- |
| `wd-v1-4-moat-tagger-v2.csv` | [SmilingWolf/wd-v1-4-moat-tagger-v2](https://huggingface.co/datasets/SmilingWolf/wd-v1-4-moat-tagger-v2) | 9,083 | `8c8750600db36233a1b274ac88bd46289e588b338218c2e4c62bbc9f2b516368` |
| `wd-eva02-large-tagger-v3.csv` | [SmilingWolf/wd-eva02-large-tagger-v3](https://huggingface.co/datasets/SmilingWolf/wd-eva02-large-tagger-v3) | 10,861 | `298633d94d0031d2081c0893f29c82eab7f0df00b08483ba8f29d1e979441217` |

- Snapshot date: 2026-09-30.
- Format: `tag_id,name,category,count`, header row, LF line endings, comma
  separated (tag names never contain commas).
- `category` uses Danbooru numbering (0=general, 1=artist, 3=copyright,
  4=character, 5=meta) plus **9 = the tagger's rating buckets**
  (`general`/`sensitive`/`questionable`/`explicit`), which are tagger output
  columns, not Danbooru tags.
- The HF dataset repos were reachable anonymously at snapshot time but have
  since become login-gated — one more reason the data lives here instead of
  being fetched at runtime.

The data originates from Danbooru tag statistics compiled by SmilingWolf;
tag names and counts only, no creative content.
