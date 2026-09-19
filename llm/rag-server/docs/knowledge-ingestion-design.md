# Knowledge ingestion: ownership, figures, chunking, source visibility

Working notes for the ingestion redesign. What was verified, what was decided, what is
still open. Supersedes the image-only draft this file started as.

## Goals

1. Images in ingested documents — Confluence diagrams, PDF figures, dashboard screenshots
   — are invisible to retrieval. Make them findable.
2. One ingestion path, not four.
3. Users can see what data is indexed, **as the source was**, and what each load captured.

## Verified current state

| Source | Chunking | Images | Source kept |
|---|---|---|---|
| Text KB upload | one document **per line** (`knowledgebase_controller.py:83`) | n/a | yes (`llm_knowledgebases.data`) |
| Product-doc PDFs (S3) | `SemanticChunker` + sliding window (`loaders/documents.py:169`) | dropped | in S3 |
| User PDF KB | page slices + one doc per figure | vision-described | yes (base64 in `data`) |
| Confluence / ServiceNow / scraped | **none** — one Document per page (`scraper.py:366`) | alt/title/filename only | **no** |

- `process_documents` has no splitter; loaders chunk or they don't.
- Retrieval is `client.query_points` (`search/search_logic.py:53`) — a vector search, not a
  scroll. The documents listing is what scrolls (`collection.py`).
- The cross-encoder scores only the first `RAG_RERANKER_MAX_DOC_CHARS` (1000) characters of
  a document, so anything below the fold of a whole-page document cannot affect ranking.
- `_is_listed_point` collapses a chunked document to one listing row using `parent_id` +
  `parent_content`. With `parent_id` and no `parent_content` it lists **every** chunk.
  Nothing writes either field today.
- `handle_updated_documents` (`processing.py:799`) deletes any point not in the current
  run's id list — so stale versions are pruned, and there is no history anywhere.
- `rag_embedding_token_usage.batch_id` exists, is nullable, and nothing writes it.
- Load history is real and wired: `ai_get_kb_load_history` → that table filtered to
  `operation_type='batch_embedding'`. Counts only, no document list.
- Collections carry metadata (`module`, `account`, `tenant_id`, `source`) via
  `_build_collection_meta`, and filtering selects on that — **not** on name patterns.
- Integration collections are `{integration_id}_knowledge_base` (`scraper.py:373`), one per
  integration, while `llm_knowledgebases` holds one row per cloud account for it.
- rag-server already has per-account LLM resolution (`get_llm`) and its own cost table, so
  it does not need llm-server to make a model call.

## Decisions

### Ingestion ownership
llm-server owns the KB row and the trigger; it hands over
`{account_id, kb_id, data, format, filename, context_tags}` and receives
`{document_count, warnings}`. rag-server extracts, describes figures, chunks, indexes,
caches and reports gaps. **Implemented** for PDFs on `feat/rag-owns-image-ingestion`.

### Figures
- One vision call per unique image; the prompt and parse mirror `vision_adapter.go`.
- Descriptions cached in Qdrant keyed on `sha256(image bytes) + model + prompt version`, so
  a retrigger is free, a model or prompt change re-describes, and the model's wording can
  never drift between runs.
- An undescribed figure is stored but not indexed: a chunk that only says "there is a
  figure here" retrieves noise.
- Only an explicit unsupported-modality error means "no vision model" — never a timeout or
  a 429.

### Chunking
- Structure first, with a size cap: respect block boundaries, merge to the cap, split
  oversized blocks on line boundaries, never split a code block.
- **Semantic chunking is not the default.** The implementation splits on a `[.!?]` regex
  (markdown-blind), uses a 90th-percentile *relative* threshold so it always splits ~10% of
  joins, has no size cap, costs one embedding per sentence per sync, and moves every
  boundary when a page is edited. Reserved for blocks with no structure.
- `chunk_index` is stamped on every chunk and `_is_listed_point` lists only index 0 —
  instead of `parent_content`, which would store a full copy of the document on every one
  of its chunks.
- Transitional duplicates after the listing change self-heal: `handle_updated_documents`
  prunes the old whole-page point on the next successful sync.

### Source visibility
- **A document that was uploaded is served from where it already is.** llm-server stored
  the file in `llm_knowledgebases.data` before rag-server ever saw it, so "view source"
  for a PDF returns that file rather than a second copy. Only a scraped page needs the
  store below: once converted, it exists nowhere else.
- Scraped source stored as content-addressed points in a sibling Qdrant collection, one per
  parent collection, identified by collection metadata `module: "kb_source"` — filtering is
  by metadata, never by a name suffix.
- Created with `on_disk_payload=True`. Qdrant defaults it to false and this repo's on-disk
  work covers vectors and HNSW only, so payloads are RAM-resident today — that is the OOM
  path. It cannot be changed after creation.
- Dropped with its parent collection, so no source outlives a deleted KB. The figure cache
  is dropped at the same point, so no derived content does either.
- Retention: **10 versions**, pruned day-collapsed — keep the newest version per calendar
  day, then the newest 10 of those. Ten syncs of a macro-churning page in one day collapse
  to one; ten real edits across ten days all survive.
- Raw HTML is kept rather than the converted markdown. The UI sanitizes and renders by
  content type; Confluence-relative images and links are broken until there is an
  authenticated proxy — accepted for v1.

### Load runs
- One run id per load, written to `rag_embedding_token_usage.batch_id` and stamped on every
  point indexed in that run, so "what did this load capture" is a filtered scroll that
  paginates natively — no manifest blob.
- Failures need their own record: a page that failed produces no point.
- The UI shows 10 runs, matching the 10 retained versions, so a listed run's sources are
  normally still present. Older runs must degrade to "source unavailable", not a dead link.

### Cost accounting
Figure description is written to `rag_embedding_token_usage` under
`operation_type='figure_description'` — not `llm_conversation_token_usage`, which requires a
conversation and message id a load does not have. Load history filters on
`batch_embedding`, so these rows record cost without appearing as extra loads.

## Challenge outcomes

1. *Unified chunker first* → **REDESIGN**. Superseded once ingestion moved to rag-server.
2. *rag-server owns ingestion* → **PROCEED**. Mitigations: probe vision capability properly,
   write gap warnings in the same statement as the status flip, fix `get_llm` resolution.
   All three implemented.
3. *Source visibility* → **REVISE** ×2 (store shape, then collection keying) → **PROCEED**.
   Mitigations: time-aware pruning, UI degrades on pruned versions, figure cache dropped
   with the KB.

## Open

### Separate ticket: vision-model coverage and telling the user

Indexing images needs a vision-capable model, and nothing in the product says so. Tracked
apart from the ingestion work because it is a provider and product-messaging problem, not an
ingestion one.

- **Provider coverage.** Only `googleai` has been exercised end to end (gemini-2.5-flash).
  `generate_with_images` also exists for OpenAI, Azure, Anthropic and Bedrock, and none has
  run. Bedrock is the riskiest: it needs the Converse API, a different call shape from the
  `invoke_model` body that provider uses for text today.
- **No way to point figure description at a vision model.** Description follows the
  account's default LLM integration, and that is routinely text-only. The Go path had
  `llm_server_kb_vision_llm_source` to pin one vision-capable integration; that pin went
  with the Go code and has no equivalent here. An account with a vision-capable integration
  configured alongside a text-only default currently cannot use it.
- **The user only finds out afterwards.** A text-only account gets "Indexed with gaps: N
  figures were stored but not analysed because no vision-capable model is configured" —
  after the upload, on the knowledge base. Nothing at upload time says a PDF's figures need
  a vision model, or which model the account would use.
- **Capability is discovered by failing.** There is no pre-flight check: we attempt the call
  and read an explicit unsupported-modality error as "not capable". Fine for correctness,
  but it means the answer is only known once figures have been sent.
- **Disclosure.** Figures leave for the account's AI provider. That is stated at upload in
  the existing UI copy; it needs re-checking against wherever this lands.


- Deleting the now-dead Go PDF path (`extractKBPDF`, `describeKBFigures`,
  `buildKBPDFChunkTexts`, the vision adapter/interface, `/kb/extract_pdf`) — including
  `clearKBImages`, which references a table this branch removes.
- Measurement gate before any chunking flag is turned on: labelled queries over
  image-bearing and long pages, measured against the live service.
- Confluence figures: extending the vision path to attachments, which needs an authenticated
  fetch and is the one thing `pdf_figures` cannot do as written.
