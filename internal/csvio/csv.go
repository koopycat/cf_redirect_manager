// Package csvio implements the strict two-column redirect interchange format.
package csvio

import (
	"encoding/csv"
	"fmt"
	"io"

	"github.com/koopycat/cf-redirect/internal/domain"
)

var header = []string{"source", "target"}

// Read requires the exact source,target header and exactly two fields per row.
func Read(r io.Reader) ([]domain.Redirect, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = 2
	record, err := reader.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("CSV is empty; expected source,target header")
	}
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}
	if record[0] != header[0] || record[1] != header[1] {
		return nil, fmt.Errorf("CSV header must be exactly source,target")
	}

	var redirects []domain.Redirect
	seen := make(map[string]struct{})
	line := 1
	for {
		line++
		record, err = reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV row %d: %w", line, err)
		}
		redirect := domain.New(record[0], record[1])
		if err := redirect.Validate(); err != nil {
			return nil, fmt.Errorf("CSV row %d: %w", line, err)
		}
		if _, exists := seen[redirect.Source]; exists {
			return nil, fmt.Errorf("CSV row %d: duplicate source %q", line, redirect.Source)
		}
		seen[redirect.Source] = struct{}{}
		redirects = append(redirects, redirect)
	}
	return redirects, nil
}

func Write(w io.Writer, redirects []domain.Redirect) error {
	writer := csv.NewWriter(w)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}
	for _, redirect := range redirects {
		if err := writer.Write([]string{redirect.Source, redirect.Target}); err != nil {
			return fmt.Errorf("write CSV row for %q: %w", redirect.Source, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write CSV: %w", err)
	}
	return nil
}
