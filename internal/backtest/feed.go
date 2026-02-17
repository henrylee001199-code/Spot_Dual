package backtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Tick struct {
	Time  time.Time
	Price decimal.Decimal
}

type Feed interface {
	Next() (Tick, error)
	Close() error
}

type JSONLFeed struct {
	paths   []string
	index   int
	file    *os.File
	scanner *bufio.Scanner
}

func NewJSONLFeed(path string) (*JSONLFeed, error) {
	paths, err := resolveJSONLPaths(path)
	if err != nil {
		return nil, err
	}
	feed := &JSONLFeed{paths: paths}
	if err := feed.openCurrent(); err != nil {
		return nil, err
	}
	return feed, nil
}

func (f *JSONLFeed) Close() error {
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	f.scanner = nil
	return err
}

func (f *JSONLFeed) Next() (Tick, error) {
	for {
		if f.scanner == nil {
			if err := f.openCurrent(); err != nil {
				return Tick{}, err
			}
		}
		if !f.scanner.Scan() {
			if err := f.scanner.Err(); err != nil {
				return Tick{}, err
			}
			_ = f.Close()
			f.index++
			if f.index >= len(f.paths) {
				return Tick{}, io.EOF
			}
			continue
		}
		line := strings.TrimSpace(f.scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			continue
		}

		var (
			ts       time.Time
			price    decimal.Decimal
			hasTime  bool
			hasPrice bool
		)
		if v, found := first(raw, "time", "timestamp", "ts", "t"); found {
			ts, hasTime = parseTimeValue(v)
		}
		if !hasTime {
			continue
		}
		if v, found := first(raw, "price", "close", "p"); found {
			price, hasPrice = parseDecimalValue(v)
		}
		if !hasPrice {
			continue
		}
		return Tick{Time: ts, Price: price}, nil
	}
}

func (f *JSONLFeed) openCurrent() error {
	if f.index >= len(f.paths) {
		return io.EOF
	}
	file, err := os.Open(f.paths[f.index])
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	f.file = file
	f.scanner = scanner
	return nil
}

func resolveJSONLPaths(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(path, name))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, errors.New("no jsonl files found in directory")
	}
	return paths, nil
}

func first(m map[string]interface{}, keys ...string) (interface{}, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

func parseTimeValue(v interface{}) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		return parseTimeString(t)
	case json.Number:
		if iv, err := t.Int64(); err == nil {
			return parseTimeNumber(iv), true
		}
		if fv, err := t.Float64(); err == nil {
			return parseTimeNumber(int64(fv)), true
		}
	case float64:
		return parseTimeNumber(int64(t)), true
	case int64:
		return parseTimeNumber(t), true
	case int:
		return parseTimeNumber(int64(t)), true
	case uint64:
		return parseTimeNumber(int64(t)), true
	case uint:
		return parseTimeNumber(int64(t)), true
	}
	return time.Time{}, false
}

func parseTimeString(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if allDigits(raw) {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return parseTimeNumber(v), true
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseTimeNumber(v int64) time.Time {
	if v >= 1_000_000_000_000 {
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}

func parseDecimalValue(v interface{}) (decimal.Decimal, bool) {
	switch t := v.(type) {
	case decimal.Decimal:
		return t, true
	case json.Number:
		dec, err := decimal.NewFromString(t.String())
		if err != nil {
			return decimal.Zero, false
		}
		return dec, true
	case string:
		if t == "" {
			return decimal.Zero, false
		}
		dec, err := decimal.NewFromString(strings.TrimSpace(t))
		if err != nil {
			return decimal.Zero, false
		}
		return dec, true
	case float64:
		return decimal.NewFromFloat(t), true
	case int64:
		return decimal.NewFromInt(t), true
	case int:
		return decimal.NewFromInt(int64(t)), true
	case uint64:
		return decimal.NewFromInt(int64(t)), true
	case uint:
		return decimal.NewFromInt(int64(t)), true
	}
	return decimal.Zero, false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var _ Feed = (*JSONLFeed)(nil)
