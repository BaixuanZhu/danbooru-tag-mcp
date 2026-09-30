# WD14 tagger vocabularies (pinned snapshots)

These CSVs are the `selected_tags.csv` vocabularies of the WD14 taggers that
captioned most LoRA training data. They are vendored as **immutable
snapshots**: the whole point is historical lookup (a tagger-era name that
current Danbooru has renamed away), so they are never refreshed in place.

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
- Re-pinning a snapshot is a deliberate act: replace the file, update the
  table above, and expect `TestDefault_PinnedSnapshots` to fail until the
  pinned counts are re-verified.

The data originates from Danbooru tag statistics compiled by SmilingWolf;
tag names and counts only, no creative content.
