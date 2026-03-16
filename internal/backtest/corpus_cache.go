package backtest

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/strategy"
)

const trainingCorpusCacheVersion = "v1"

var trainingCorpusCacheRoot = filepath.Join(".cache", "backtest", "training-corpus", trainingCorpusCacheVersion)

type trainingCorpusCacheEntry struct {
	Version string
	Key     string
	SavedAt time.Time
	Corpus  trainingCorpusCachePayload
}

type trainingCorpusCachePayload struct {
	CandidateTimestamps []time.Time
	Rows                []trainingCorpusCacheRow
}

type trainingCorpusCacheRow struct {
	CandidateAt time.Time
	AvailableAt time.Time
	Sample      strategy.TrainingSample
}

func trainingCorpusCacheKey(cfg config.TradingConfig, runCfg RunConfig, records []record, symbolIndices map[string][]int) string {
	hasher := sha256.New()
	start := time.Time{}
	end := time.Time{}
	if len(records) > 0 {
		start = records[0].bar.Timestamp.UTC()
		end = records[len(records)-1].bar.Timestamp.UTC()
	}
	fmt.Fprintf(
		hasher,
		"%s|cfg=%#v|run=%s|%s|%s|%s|%s|%s|records=%d|symbols=%d|lookahead=%d",
		trainingCorpusCacheVersion,
		cfg,
		runCfg.Start.UTC().Format(time.RFC3339),
		runCfg.TrainStart.UTC().Format(time.RFC3339),
		runCfg.TrainEnd.UTC().Format(time.RFC3339),
		runCfg.End.UTC().Format(time.RFC3339),
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
		len(records),
		len(symbolIndices),
		runCfg.LabelLookaheadBars,
	)
	return hex.EncodeToString(hasher.Sum(nil))
}

func loadTrainingCorpusCache(cacheKey string) (trainingCorpus, bool, error) {
	path := trainingCorpusCachePath(cacheKey)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return trainingCorpus{}, false, nil
		}
		return trainingCorpus{}, false, err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return trainingCorpus{}, false, err
	}
	defer gzipReader.Close()

	var entry trainingCorpusCacheEntry
	if err := gob.NewDecoder(gzipReader).Decode(&entry); err != nil {
		return trainingCorpus{}, false, err
	}
	if entry.Version != trainingCorpusCacheVersion || entry.Key != cacheKey {
		return trainingCorpus{}, false, nil
	}
	return trainingCorpus{
		candidateTimestamps: entry.Corpus.CandidateTimestamps,
		rows:                expandTrainingCorpusRows(entry.Corpus.Rows),
	}, true, nil
}

func saveTrainingCorpusCache(cacheKey string, corpus trainingCorpus) error {
	path := trainingCorpusCachePath(cacheKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	entry := trainingCorpusCacheEntry{
		Version: trainingCorpusCacheVersion,
		Key:     cacheKey,
		SavedAt: time.Now().UTC(),
		Corpus: trainingCorpusCachePayload{
			CandidateTimestamps: corpus.candidateTimestamps,
			Rows:                compactTrainingCorpusRows(corpus.rows),
		},
	}
	return gob.NewEncoder(gzipWriter).Encode(entry)
}

func trainingCorpusCachePath(cacheKey string) string {
	prefix := cacheKey
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(trainingCorpusCacheRoot, prefix, cacheKey+".gob.gz")
}

func compactTrainingCorpusRows(rows []trainingRow) []trainingCorpusCacheRow {
	compact := make([]trainingCorpusCacheRow, 0, len(rows))
	for _, row := range rows {
		compact = append(compact, trainingCorpusCacheRow{
			CandidateAt: row.candidateAt,
			AvailableAt: row.availableAt,
			Sample:      row.sample,
		})
	}
	return compact
}

func expandTrainingCorpusRows(rows []trainingCorpusCacheRow) []trainingRow {
	expanded := make([]trainingRow, 0, len(rows))
	for _, row := range rows {
		expanded = append(expanded, trainingRow{
			candidateAt: row.CandidateAt,
			availableAt: row.AvailableAt,
			sample:      row.Sample,
		})
	}
	return expanded
}
