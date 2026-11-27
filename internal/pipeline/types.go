// Package pipeline contains the payload types shared by the agent and the
// mcp-pipeline server. The chain itself is built by the model: there is no
// local orchestrator, so the model passes items from search into summarize
// and summary from summarize into save_to_file.
package pipeline

// Item is one document found by search. Text is the source material for
// summarize; Snippet is the short preview shown to the model.
type Item struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Text    string `json:"text,omitempty"`
}

// SearchResult is the payload returned by the search tool.
type SearchResult struct {
	Query     string `json:"query"`
	Root      string `json:"root,omitempty"`
	Count     int    `json:"count"`
	Scanned   int    `json:"scanned,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Items     []Item `json:"items"`
}

// SummaryResult is the payload returned by the summarize tool.
type SummaryResult struct {
	Count     int      `json:"count"`
	Summary   string   `json:"summary"`
	Sentences []string `json:"sentences,omitempty"`
	SourceIDs []string `json:"source_ids,omitempty"`
}

// SaveResult is the payload returned by the save_to_file tool.
type SaveResult struct {
	Saved  bool   `json:"saved"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Format string `json:"format"`
}
