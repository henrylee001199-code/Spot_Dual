package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.binance.com"
	defaultOutDir  = "data/binance"
)

type kline struct {
	OpenTime  int64
	Open      string
	High      string
	Low       string
	Close     string
	Volume    string
	CloseTime int64
}

type tickLine struct {
	Time      string `json:"time"`
	Timestamp int64  `json:"timestamp"`
	Symbol    string `json:"symbol"`
	Interval  string `json:"interval"`
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Price     string `json:"price"`
	Volume    string `json:"volume"`
}

type dateWriter struct {
	root        string
	currentDate string
	currentFile *os.File
}

func newDateWriter(root string) (*dateWriter, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &dateWriter{root: root}, nil
}

func (w *dateWriter) write(date string, line []byte) error {
	if err := w.rotate(date); err != nil {
		return err
	}
	if _, err := w.currentFile.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (w *dateWriter) rotate(date string) error {
	if date == w.currentDate && w.currentFile != nil {
		return nil
	}
	if w.currentFile != nil {
		if err := w.currentFile.Sync(); err != nil {
			_ = w.currentFile.Close()
			w.currentFile = nil
			return err
		}
		if err := w.currentFile.Close(); err != nil {
			w.currentFile = nil
			return err
		}
		w.currentFile = nil
	}
	path := filepath.Join(w.root, date+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w.currentFile = f
	w.currentDate = date
	return nil
}

func (w *dateWriter) close() error {
	if w == nil || w.currentFile == nil {
		return nil
	}
	if err := w.currentFile.Sync(); err != nil {
		_ = w.currentFile.Close()
		w.currentFile = nil
		return err
	}
	err := w.currentFile.Close()
	w.currentFile = nil
	return err
}

func main() {
	var (
		baseURL  string
		symbol   string
		interval string
		months   int
		startRaw string
		endRaw   string
		outDir   string
		timeout  int
	)

	flag.StringVar(&baseURL, "base-url", defaultBaseURL, "exchange REST base url")
	flag.StringVar(&symbol, "symbol", "BTCUSDT", "symbol, e.g. BTCUSDT")
	flag.StringVar(&interval, "interval", "1m", "kline interval, e.g. 1m/5m/15m/1h")
	flag.IntVar(&months, "months", 6, "how many months to fetch back from now")
	flag.StringVar(&startRaw, "start", "", "start time (YYYY-MM-DD or RFC3339, UTC)")
	flag.StringVar(&endRaw, "end", "", "end time (YYYY-MM-DD or RFC3339, UTC), inclusive for date")
	flag.StringVar(&outDir, "out-dir", defaultOutDir, "output root dir")
	flag.IntVar(&timeout, "timeout-sec", 20, "http timeout seconds")
	flag.Parse()

	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	interval = strings.TrimSpace(interval)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if symbol == "" || interval == "" || baseURL == "" {
		fatal("base-url/symbol/interval are required")
	}
	start, end, err := resolveWindow(months, startRaw, endRaw)
	if err != nil {
		fatal(err.Error())
	}

	targetDir := filepath.Join(outDir, symbol, interval)
	writer, err := newDateWriter(targetDir)
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if closeErr := writer.close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close writer failed: %v\n", closeErr)
		}
	}()

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}

	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	total := 0
	requests := 0

	fmt.Printf("fetching symbol=%s interval=%s from=%s to=%s\n", symbol, interval, start.Format(time.RFC3339), end.Add(-time.Millisecond).Format(time.RFC3339))

	for startMs < endMs {
		batch, err := fetchKlines(client, baseURL, symbol, interval, startMs, endMs-1, 1000)
		if err != nil {
			fatal(err.Error())
		}
		if len(batch) == 0 {
			break
		}
		requests++
		for _, k := range batch {
			if k.OpenTime >= endMs {
				continue
			}
			ts := time.UnixMilli(k.OpenTime).UTC()
			date := ts.Format("2006-01-02")
			line := tickLine{
				Time:      ts.Format(time.RFC3339),
				Timestamp: k.OpenTime,
				Symbol:    symbol,
				Interval:  interval,
				Open:      k.Open,
				High:      k.High,
				Low:       k.Low,
				Close:     k.Close,
				Price:     k.Close,
				Volume:    k.Volume,
			}
			encoded, err := json.Marshal(line)
			if err != nil {
				fatal(err.Error())
			}
			if err := writer.write(date, encoded); err != nil {
				fatal(err.Error())
			}
			total++
			startMs = k.OpenTime + 1
		}
		if requests%20 == 0 {
			fmt.Printf("progress: requests=%d records=%d last=%s\n", requests, total, time.UnixMilli(startMs).UTC().Format(time.RFC3339))
		}
		time.Sleep(120 * time.Millisecond)
	}

	fmt.Printf("done: records=%d requests=%d output=%s\n", total, requests, targetDir)
}

func fetchKlines(client *http.Client, baseURL, symbol, interval string, startMs, endMs int64, limit int) ([]kline, error) {
	endpoint := baseURL + "/api/v3/klines"
	values := url.Values{}
	values.Set("symbol", symbol)
	values.Set("interval", interval)
	values.Set("startTime", strconv.FormatInt(startMs, 10))
	values.Set("endTime", strconv.FormatInt(endMs, 10))
	values.Set("limit", strconv.Itoa(limit))

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest(http.MethodGet, endpoint+"?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return parseKlines(body)
	}
	if lastErr == nil {
		lastErr = errors.New("fetch klines failed")
	}
	return nil, lastErr
}

func parseKlines(body []byte) ([]kline, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]kline, 0, len(rows))
	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		openTime, err := parseInt64(row[0])
		if err != nil {
			continue
		}
		closeTime, err := parseInt64(row[6])
		if err != nil {
			closeTime = 0
		}
		out = append(out, kline{
			OpenTime:  openTime,
			Open:      parseStr(row[1]),
			High:      parseStr(row[2]),
			Low:       parseStr(row[3]),
			Close:     parseStr(row[4]),
			Volume:    parseStr(row[5]),
			CloseTime: closeTime,
		})
	}
	return out, nil
}

func parseInt64(raw json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	}
	return 0, errors.New("invalid int64")
}

func parseStr(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err == nil {
		return n.String()
	}
	return strings.TrimSpace(string(raw))
}

func resolveWindow(months int, startRaw, endRaw string) (time.Time, time.Time, error) {
	startRaw = strings.TrimSpace(startRaw)
	endRaw = strings.TrimSpace(endRaw)
	if startRaw == "" && endRaw == "" {
		if months < 1 {
			return time.Time{}, time.Time{}, errors.New("months must be >= 1")
		}
		end := time.Now().UTC()
		start := end.AddDate(0, -months, 0)
		return start, end, nil
	}
	if startRaw == "" || endRaw == "" {
		return time.Time{}, time.Time{}, errors.New("start and end must be provided together")
	}
	start, startDateOnly, err := parseRangeTime(startRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start: %w", err)
	}
	end, endDateOnly, err := parseRangeTime(endRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end: %w", err)
	}
	if startDateOnly {
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	}
	if endDateOnly {
		end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("end must be after start")
	}
	return start.UTC(), end.UTC(), nil
}

func parseRangeTime(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, errors.New("empty")
	}
	if len(raw) == len("2006-01-02") {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, false, err
		}
		return t, true, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), false, nil
		}
	}
	return time.Time{}, false, errors.New("unsupported time format")
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
