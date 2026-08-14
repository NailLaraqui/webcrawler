package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NailLaraqui/webcrawler/internal/crawler"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

func TestWriteCSV_HeaderAndRows(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/", Depth: 0, LinksFound: 3},
		{URL: "https://example.com/a", Depth: 1, Err: errors.New("connection reset")},
		{URL: "https://example.com/b", Depth: 1, Err: robots.ErrDisallowed},
	}

	var buf bytes.Buffer
	if err := WriteCSV(&buf, results); err != nil {
		t.Fatalf("WriteCSV returned error: %v", err)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse generated CSV: %v", err)
	}
	if len(rows) != len(results)+1 { // +1 for the header row
		t.Fatalf("got %d rows, want %d (including header)", len(rows), len(results)+1)
	}

	wantHeader := []string{"url", "depth", "status", "links_found", "error"}
	if !equalRows(rows[0], wantHeader) {
		t.Errorf("header = %v, want %v", rows[0], wantHeader)
	}

	wantRows := [][]string{
		{"https://example.com/", "0", "ok", "3", ""},
		{"https://example.com/a", "1", "failed", "0", "connection reset"},
		{"https://example.com/b", "1", "skipped_robots", "0", ""},
	}
	for i, want := range wantRows {
		if got := rows[i+1]; !equalRows(got, want) {
			t.Errorf("row %d = %v, want %v", i, got, want)
		}
	}
}

func TestWriteCSV_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, nil); err != nil {
		t.Fatalf("WriteCSV returned error: %v", err)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse generated CSV: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want just the header row", len(rows))
	}
}

func TestWriteCSV_FieldsContainingCommasAndQuotesAreEscaped(t *testing.T) {
	// encoding/csv should quote-escape special characters automatically;
	// this test exists to prove the round trip actually works rather
	// than just trusting the stdlib blindly.
	results := []crawler.Result{
		{URL: "https://example.com/", Err: errors.New(`weird, error "with" quotes`)},
	}

	var buf bytes.Buffer
	if err := WriteCSV(&buf, results); err != nil {
		t.Fatalf("WriteCSV returned error: %v", err)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse generated CSV: %v", err)
	}
	got := rows[1][4] // error column
	want := `weird, error "with" quotes`
	if got != want {
		t.Errorf("error field round-tripped as %q, want %q", got, want)
	}
}

func TestWriteCSVFile_WritesToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.csv")

	results := []crawler.Result{
		{URL: "https://example.com/", Depth: 0, LinksFound: 2},
	}
	if err := WriteCSVFile(path, results); err != nil {
		t.Fatalf("WriteCSVFile returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse written CSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (header + 1 result)", len(rows))
	}
	if rows[1][0] != "https://example.com/" {
		t.Errorf("row url = %q, want %q", rows[1][0], "https://example.com/")
	}
}

func TestWriteCSVFile_InvalidPathReturnsError(t *testing.T) {
	// A directory that doesn't exist should fail cleanly, not panic.
	err := WriteCSVFile("/nonexistent-dir-xyz/results.csv", nil)
	if err == nil {
		t.Fatalf("expected an error writing to a nonexistent directory, got nil")
	}
}

func TestWriteJSON_StructureAndValues(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/", Depth: 0, LinksFound: 3},
		{URL: "https://example.com/a", Depth: 1, Err: errors.New("connection reset")},
		{URL: "https://example.com/b", Depth: 1, Err: robots.ErrDisallowed},
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, results); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	var got []JSONResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse generated JSON: %v", err)
	}

	if len(got) != len(results) {
		t.Fatalf("got %d items, want %d", len(got), len(results))
	}

	if !JSONAndCrawlResultEqual(&got[0], &results[0]) {
		t.Errorf("got[0] = %+v, want URL='https://example.com/', Depth=0, LinksFound=3, Error=''", got[0])
	}

	if !JSONAndCrawlResultEqual(&got[1], &results[1]) {
		t.Errorf("got[1] = %+v, want URL='https://example.com/a', Depth=1, Error='connection reset'", got[1])
	}

	if !JSONAndCrawlResultEqual(&got[2], &results[2]) {
		t.Errorf("got[2] = %+v, want URL='https://example.com/b', Depth=1, Error=%q", got[2], robots.ErrDisallowed.Error())
	}
}

func TestWriteJSON_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, nil); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	var got []JSONResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse generated JSON for empty results: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("got %d items, want 0", len(got))
	}
}

func TestWriteJSONFile_WritesToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")

	results := []crawler.Result{
		{URL: "https://example.com/", Depth: 0, LinksFound: 2},
	}
	if err := WriteJSONFile(path, results); err != nil {
		t.Fatalf("WriteJSONFile returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	var got []JSONResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to parse written JSON file: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if got[0].URL != "https://example.com/" {
		t.Errorf("got[0].URL = %q, want 'https://example.com/'", got[0].URL)
	}
}

func TestWriteJSONFile_InvalidPathReturnsError(t *testing.T) {
	err := WriteJSONFile("/nonexistent-dir-xyz/results.json", nil)
	if err == nil {
		t.Fatalf("expected an error writing to a nonexistent directory, got nil")
	}
}

func equalRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func JSONAndCrawlResultEqual(a *JSONResult, b *crawler.Result) bool {

	errBStr := ""
	if b.Err != nil {
		errBStr = b.Err.Error()
	}

	return a.URL == b.URL && a.Depth == b.Depth && a.LinksFound == b.LinksFound && a.Error == errBStr
}
