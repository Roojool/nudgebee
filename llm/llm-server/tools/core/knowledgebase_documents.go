package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"nudgebee/llm/common"
	"nudgebee/llm/config"
	"nudgebee/llm/security"
	"strconv"
)

const (
	kbDocumentsDefaultLimit = 50
	kbDocumentsMaxLimit     = 200
	errKBDocumentNotFound   = "kb: document not found, the knowledge base may have been re-synced"
)

// KBDocument is one stored document of a knowledge base. Content is only
// populated when a single document is fetched.
type KBDocument struct {
	Id    string  `json:"id"`
	Title string  `json:"title"`
	Url   *string `json:"url"`
	// DocumentKey is the handle a Fact/SOP mark is stored against (see
	// DocumentKey). Empty when the document cannot be marked individually.
	DocumentKey string `json:"document_key"`
	// NoteCategory is the mark a user put on this document: "sop", "fact", or
	// empty when it carries none.
	NoteCategory string  `json:"note_category"`
	Content      *string `json:"content,omitempty"`
}

// KBDocumentsPage is one page of a knowledge base's documents. NextOffset is
// an opaque cursor; nil means there are no more documents.
type KBDocumentsPage struct {
	Items      []KBDocument `json:"items"`
	NextOffset *string      `json:"next_offset"`
}

type ragKBDocument struct {
	Id       string  `json:"id"`
	Title    *string `json:"title"`
	Url      *string `json:"url"`
	Content  *string `json:"content"`
	SourceId *string `json:"source_id"`
}

// ragDocumentKey is DocumentKey for a document rag-server listed or served.
func ragDocumentKey(collection string, doc ragKBDocument) string {
	var sourceID string
	if doc.SourceId != nil {
		sourceID = *doc.SourceId
	}
	return DocumentKey(collection, sourceID, doc.Url)
}

// kbDocumentsCollection authorizes read access and resolves the KB's name and
// rag-server collection.
func kbDocumentsCollection(sc *security.RequestContext, accountId, kbId string) (name, collection string, err error) {
	if accountId == "" || kbId == "" {
		return "", "", errors.New(errKBAccountIDRequired)
	}
	if !sc.GetSecurityContext().HasAccountAccess(accountId, security.SecurityAccessTypeRead) {
		return "", "", errors.New(errKBUnauthorized)
	}
	dbms, err := common.GetDatabaseManager(common.Metastore)
	if err != nil {
		return "", "", fmt.Errorf("kb: failed to get database manager: %w", err)
	}
	var kbMeta struct {
		Name          string  `db:"name"`
		KBType        string  `db:"kb_type"`
		IntegrationID *string `db:"integration_id"`
	}
	err = dbms.Db.Get(&kbMeta, "SELECT name, kb_type, integration_id FROM llm_knowledgebases WHERE id = $1 AND account_id = $2", kbId, accountId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errors.New(errKBNotFound)
		}
		return "", "", fmt.Errorf("kb: failed to fetch knowledgebase: %w", err)
	}
	return kbMeta.Name, kbCollectionName(kbMeta.KBType, kbMeta.IntegrationID, kbId), nil
}

// documentTitle falls back to the KB name for stored documents that carry no
// title (manual notes and uploaded files).
func documentTitle(doc ragKBDocument, kbName string) string {
	if doc.Title != nil && *doc.Title != "" {
		return *doc.Title
	}
	return kbName
}

// getRAG issues an authenticated GET to rag-server and decodes {"data": ...}
// into out. It reports found=false on 404.
func getRAG(path string, out any) (found bool, err error) {
	ragServerURL := config.Config.RAGServerUrl
	if ragServerURL == "" {
		return false, errors.New("kb: search index service not configured")
	}
	req, err := http.NewRequest(http.MethodGet, ragServerURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("kb: failed to prepare search index request: %w", err)
	}
	addRAGAuth(req)
	resp, err := ragClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("kb: failed to reach search index: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("kb: failed to close response body", "error", closeErr)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return false, fmt.Errorf("kb: failed to read search index response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("kb: search index request failed (status %d): %s", resp.StatusCode, string(body))
	}
	envelope := struct {
		Data any `json:"data"`
	}{Data: out}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false, fmt.Errorf("kb: failed to parse search index response: %w", err)
	}
	return true, nil
}

// ListKBDocuments returns one page of a knowledge base's stored documents
// (title and link only). A KB whose collection was never built returns an
// empty page.
func ListKBDocuments(sc *security.RequestContext, accountId, kbId string, limit int, offset string) (*KBDocumentsPage, error) {
	kbName, collection, err := kbDocumentsCollection(sc, accountId, kbId)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = kbDocumentsDefaultLimit
	}
	limit = min(limit, kbDocumentsMaxLimit)

	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if offset != "" {
		query.Set("offset", offset)
	}
	var ragPage struct {
		Items      []ragKBDocument `json:"items"`
		NextOffset *string         `json:"next_offset"`
	}
	found, err := getRAG(fmt.Sprintf("/collections/%s/documents?%s", url.PathEscape(collection), query.Encode()), &ragPage)
	if err != nil {
		return nil, err
	}
	page := &KBDocumentsPage{Items: []KBDocument{}}
	if !found {
		return page, nil
	}
	keys := make([]string, 0, len(ragPage.Items))
	for _, doc := range ragPage.Items {
		key := ragDocumentKey(collection, doc)
		page.Items = append(page.Items, KBDocument{Id: doc.Id, Title: documentTitle(doc, kbName), Url: doc.Url, DocumentKey: key})
		if key != "" {
			keys = append(keys, key)
		}
	}
	// One lookup for the page, not one per document. A failure costs the marks,
	// not the listing: the documents are still worth showing without them.
	categories, err := kbDocumentCategories(sc.GetContext(), accountId, keys)
	if err != nil {
		sc.GetLogger().Warn("kb: unable to read document categories", "error", err, "kb_id", kbId)
		categories = map[string]string{}
	}
	for i := range page.Items {
		page.Items[i].NoteCategory = categories[page.Items[i].DocumentKey]
	}
	page.NextOffset = ragPage.NextOffset
	return page, nil
}

// KBDocumentSource is a document as it was fetched, before conversion and chunking.
type KBDocumentSource struct {
	SourceId    string `json:"source_id"`
	Title       string `json:"title"`
	Url         string `json:"url"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
	ContentHash string `json:"content_hash"`
	FetchedAt   string `json:"fetched_at"`
}

// KBRunDocument is one document a load captured.
type KBRunDocument struct {
	SourceId    string `json:"source_id"`
	Title       string `json:"title"`
	Url         string `json:"url"`
	ContentHash string `json:"content_hash"`
	FetchedAt   string `json:"fetched_at"`
}

// KBRunDocumentsPage is one page of what a load captured.
type KBRunDocumentsPage struct {
	Items      []KBRunDocument `json:"items"`
	NextOffset *string         `json:"next_offset"`
}

// GetKBDocumentSource returns a document's source exactly as it was fetched.
//
// Addressed by content hash rather than by document, so a caller can ask for the version a
// particular load indexed. Old versions are pruned, so a hash from an older run legitimately
// returns not-found — callers render "source unavailable", not a broken link.
func GetKBDocumentSource(sc *security.RequestContext, accountId, kbId, contentHash string) (*KBDocumentSource, error) {
	// An uploaded document is its own source, and llm-server already holds it: it was
	// stored here before rag-server ever saw it. Serving the stored file is both the
	// truthful answer and the one that needs no second copy — unlike a scraped page,
	// which exists nowhere else once it has been converted.
	if uploaded, err := uploadedKBSource(sc, accountId, kbId); err != nil || uploaded != nil {
		return uploaded, err
	}
	if contentHash == "" {
		return nil, errors.New("kb: content_hash is required")
	}
	_, collection, err := kbDocumentsCollection(sc, accountId, kbId)
	if err != nil {
		return nil, err
	}
	var source KBDocumentSource
	found, err := getRAG(
		fmt.Sprintf("/collections/%s/source/%s", url.PathEscape(collection), url.PathEscape(contentHash)), &source)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New(errKBDocumentNotFound)
	}
	return &source, nil
}

// uploadedKBSource returns the stored file for a knowledge base that was uploaded as a
// document, or nil when the knowledge base has none — a scraped integration, whose source
// is served from rag-server instead.
//
// Content carries the bytes as llm_knowledgebases.data holds them: base64 for a PDF, the
// text itself otherwise. ContentType is what tells the two apart.
func uploadedKBSource(sc *security.RequestContext, accountId, kbId string) (*KBDocumentSource, error) {
	dbms, err := knowledgeDatabase(common.Metastore)
	if err != nil {
		return nil, fmt.Errorf("kb: failed to get database manager: %w", err)
	}
	var row struct {
		Name      string `db:"name"`
		Filename  string `db:"data_filename"`
		Format    string `db:"data_format"`
		Data      string `db:"data"`
		UpdatedAt string `db:"updated_at"`
	}
	err = dbms.Db.Get(&row, `SELECT name, COALESCE(data_filename, '') AS data_filename,
	                                COALESCE(data_format, '') AS data_format, COALESCE(data, '') AS data,
	                                COALESCE(updated_at::text, '') AS updated_at
	                         FROM llm_knowledgebases
	                         WHERE id = $1 AND account_id = $2 AND enabled`, kbId, accountId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New(errKBDocumentNotFound)
		}
		return nil, fmt.Errorf("kb: failed to read knowledge base: %w", err)
	}
	if row.Data == "" {
		return nil, nil
	}
	contentType := "text/plain"
	if row.Format == string(KBFormatPDF) {
		contentType = "application/pdf"
	}
	title := row.Filename
	if title == "" {
		title = row.Name
	}
	return &KBDocumentSource{
		SourceId:    kbId,
		Title:       title,
		Content:     row.Data,
		ContentType: contentType,
		ContentHash: "",
		FetchedAt:   row.UpdatedAt,
	}, nil
}

// ListKBRunDocuments returns what one load captured.
//
// Failures are not here: a document that failed to load produced no record. Those are on
// the load-history entry for the same run.
func ListKBRunDocuments(
	sc *security.RequestContext, accountId, kbId, runId string, limit int, offset string,
) (*KBRunDocumentsPage, error) {
	if runId == "" {
		return nil, errors.New("kb: run_id is required")
	}
	_, collection, err := kbDocumentsCollection(sc, accountId, kbId)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = kbDocumentsDefaultLimit
	}
	limit = min(limit, kbDocumentsMaxLimit)

	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if offset != "" {
		query.Set("offset", offset)
	}
	var ragPage struct {
		Documents  []KBRunDocument `json:"documents"`
		NextOffset *string         `json:"next_offset"`
	}
	found, err := getRAG(fmt.Sprintf("/collections/%s/runs/%s/documents?%s",
		url.PathEscape(collection), url.PathEscape(runId), query.Encode()), &ragPage)
	if err != nil {
		return nil, err
	}
	page := &KBRunDocumentsPage{Items: []KBRunDocument{}}
	if !found {
		return page, nil
	}
	page.Items = append(page.Items, ragPage.Documents...)
	page.NextOffset = ragPage.NextOffset
	return page, nil
}

// GetKBDocument returns one stored document of a knowledge base with its content.
func GetKBDocument(sc *security.RequestContext, accountId, kbId, documentId string) (*KBDocument, error) {
	if documentId == "" {
		return nil, errors.New("kb: document_id is required")
	}
	kbName, collection, err := kbDocumentsCollection(sc, accountId, kbId)
	if err != nil {
		return nil, err
	}
	var doc ragKBDocument
	found, err := getRAG(fmt.Sprintf("/collections/%s/documents/%s", url.PathEscape(collection), url.PathEscape(documentId)), &doc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New(errKBDocumentNotFound)
	}
	key := ragDocumentKey(collection, doc)
	var category string
	if key != "" {
		categories, err := kbDocumentCategories(sc.GetContext(), accountId, []string{key})
		if err != nil {
			sc.GetLogger().Warn("kb: unable to read document categories", "error", err, "kb_id", kbId)
		}
		category = categories[key]
	}
	return &KBDocument{
		Id:           doc.Id,
		Title:        documentTitle(doc, kbName),
		Url:          doc.Url,
		DocumentKey:  key,
		NoteCategory: category,
		Content:      doc.Content,
	}, nil
}
