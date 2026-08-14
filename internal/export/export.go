// Package export writes crawl results to external file formats.
package export

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/NailLaraqui/webcrawler/internal/crawler"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

var csvHeader = []string{"url", "depth", "status", "links_found", "error"}

// statusOf classifies a Result the same way main.go's stdout output
// does, so the CSV and the terminal display always agree with each
// other instead of drifting into two slightly different notions of
// "what happened to this URL".
func statusOf(r crawler.Result) (status, errMsg string) {
	switch {
	case errors.Is(r.Err, robots.ErrDisallowed):
		return "skipped_robots", ""
	case r.Err != nil:
		return "failed", r.Err.Error()
	default:
		return "ok", ""
	}
}

// WriteCSV writes results to w in CSV form, one row per Result, in the
// order given. The caller controls ordering — this package doesn't sort.
func WriteCSV(w io.Writer, results []crawler.Result) error {
	cw := csv.NewWriter(w)

	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, r := range results {
		status, errMsg := statusOf(r)
		row := []string{
			r.URL,
			strconv.Itoa(r.Depth),
			status,
			strconv.Itoa(r.LinksFound),
			errMsg,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	cw.Flush()
	return cw.Error() // Flush only records I/O errors internally; surface them here
}

// WriteCSVFile creates (or truncates) path and writes results to it as
// CSV. The file is always closed, even if writing fails partway through;
// a close error is reported only if there wasn't already a write error to
// report instead (the write error is almost always the more useful one).
func WriteCSVFile(path string, results []crawler.Result) (err error) {
	f, createErr := os.Create(path)
	if createErr != nil {
		return createErr
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	return WriteCSV(f, results)
}

// JSONResult represents the structured JSON output for a single crawled page.
// Using explicit struct tags ensures clean field names in the output.
type JSONResult struct {
	URL        string `json:"url"`
	Depth      int    `json:"depth"`
	LinksFound int    `json:"links_found"`
	Error      string `json:"error,omitempty"` // Format the error to string (or omits the field if nil)
}

// WriteJSON writes the crawling results formatted as JSON to the provided writer.
func WriteJSON(w io.Writer, results []crawler.Result) error {
	jsonResults := make([]JSONResult, len(results))
	for i, r := range results {
		errStr := ""
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		jsonResults[i] = JSONResult{
			URL:        r.URL,
			Depth:      r.Depth,
			LinksFound: r.LinksFound,
			Error:      errStr,
		}
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(jsonResults); err != nil {
		return fmt.Errorf("failed to encode results to JSON: %w", err)
	}

	return nil
}

// WriteJSONFile creates or overwrites a JSON file at the specified path and writes results to it
func WriteJSONFile(filePath string, results []crawler.Result) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create JSON file %s: %w", filePath, err)
	}
	defer file.Close()

	return WriteJSON(file, results)
}
