package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
)

const (
	historicalCacheMagic      = "MTBH2"
	historicalPriceScale      = 10_000
	historicalCacheBufferSize = 1 << 20
)

type historicalJobCacheReader struct {
	file      *os.File
	gzip      *gzip.Reader
	reader    *bufio.Reader
	startUnix int64
	symbols   []string
	remaining uint64
}

func historicalCachePath(job historicalFetchJob, feed string) string {
	hasher := sha256.New()
	hasher.Write([]byte(historicalCacheVersion))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(feed))))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.start.Format(time.RFC3339Nano)))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.end.Format(time.RFC3339Nano)))
	for _, symbol := range job.symbols {
		hasher.Write([]byte("|"))
		hasher.Write([]byte(strings.ToUpper(strings.TrimSpace(symbol))))
	}
	key := hex.EncodeToString(hasher.Sum(nil))
	return filepath.Join(historicalCacheRoot, historicalCacheVersion, key[:2], key+".bars.gz")
}

func historicalJobCacheExists(job historicalFetchJob, feed string) bool {
	_, err := os.Stat(historicalCachePath(job, feed))
	return err == nil
}

func openHistoricalJobCacheReader(job historicalFetchJob, feed string) (*historicalJobCacheReader, error) {
	path := historicalCachePath(job, feed)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	reader := &historicalJobCacheReader{
		file:   file,
		gzip:   gzipReader,
		reader: bufio.NewReaderSize(gzipReader, historicalCacheBufferSize),
	}
	if err := reader.readHeader(); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

func (r *historicalJobCacheReader) Close() error {
	var closeErr error
	if r.gzip != nil {
		closeErr = r.gzip.Close()
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (r *historicalJobCacheReader) Next() (backtest.InputBar, bool, error) {
	if r.remaining == 0 {
		return backtest.InputBar{}, false, nil
	}
	offsetSeconds, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	symbolIndex, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	if symbolIndex >= uint64(len(r.symbols)) {
		return backtest.InputBar{}, false, fmt.Errorf("historical cache symbol index %d out of range", symbolIndex)
	}

	openPrice, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	highPrice, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	lowPrice, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	closePrice, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	prevClose, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	volume, err := readUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}

	r.remaining--
	return backtest.InputBar{
		Timestamp: time.Unix(r.startUnix+int64(offsetSeconds), 0),
		Symbol:    r.symbols[symbolIndex],
		Open:      decodeHistoricalPrice(openPrice),
		High:      decodeHistoricalPrice(highPrice),
		Low:       decodeHistoricalPrice(lowPrice),
		Close:     decodeHistoricalPrice(closePrice),
		Volume:    int64(volume),
		PrevClose: decodeHistoricalPrice(prevClose),
	}, true, nil
}

func (r *historicalJobCacheReader) readHeader() error {
	magic := make([]byte, len(historicalCacheMagic))
	if _, err := io.ReadFull(r.reader, magic); err != nil {
		return err
	}
	if string(magic) != historicalCacheMagic {
		return fmt.Errorf("unsupported historical cache format %q", string(magic))
	}

	startUnix, err := readVarint(r.reader)
	if err != nil {
		return err
	}
	if _, err := readVarint(r.reader); err != nil {
		return err
	}
	symbolCount, err := readUvarint(r.reader)
	if err != nil {
		return err
	}
	symbols := make([]string, 0, symbolCount)
	for index := uint64(0); index < symbolCount; index++ {
		symbol, err := readString(r.reader)
		if err != nil {
			return err
		}
		symbols = append(symbols, symbol)
	}
	recordCount, err := readUvarint(r.reader)
	if err != nil {
		return err
	}
	r.startUnix = startUnix
	r.symbols = symbols
	r.remaining = recordCount
	return nil
}

func saveHistoricalJobCache(job historicalFetchJob, feed string, result historicalFetchResult) error {
	path := historicalCachePath(job, feed)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	bars := append([]backtest.InputBar(nil), result.bars...)
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})

	tempPath := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	success := false
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(gzipWriter, historicalCacheBufferSize)

	if err := writeStringBytes(buffered, historicalCacheMagic); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeVarint(buffered, job.start.Unix()); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeVarint(buffered, job.end.Unix()); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeUvarint(buffered, uint64(len(job.symbols))); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	for _, symbol := range job.symbols {
		if err := writeString(buffered, strings.ToUpper(strings.TrimSpace(symbol))); err != nil {
			_ = gzipWriter.Close()
			return err
		}
	}
	if err := writeUvarint(buffered, uint64(len(bars))); err != nil {
		_ = gzipWriter.Close()
		return err
	}

	symbolIndex := make(map[string]uint64, len(job.symbols))
	for index, symbol := range job.symbols {
		symbolIndex[strings.ToUpper(strings.TrimSpace(symbol))] = uint64(index)
	}
	jobStartUnix := job.start.Unix()
	for _, item := range bars {
		index, ok := symbolIndex[strings.ToUpper(strings.TrimSpace(item.Symbol))]
		if !ok {
			_ = gzipWriter.Close()
			return fmt.Errorf("historical cache symbol %q missing from job symbol table", item.Symbol)
		}
		offsetSeconds := item.Timestamp.Unix() - jobStartUnix
		if offsetSeconds < 0 {
			_ = gzipWriter.Close()
			return fmt.Errorf("historical cache bar timestamp %s is before job start %s", item.Timestamp.Format(time.RFC3339), job.start.Format(time.RFC3339))
		}
		if err := writeUvarint(buffered, uint64(offsetSeconds)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, index); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Open)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.High)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Low)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Close)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.PrevClose)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, uint64(item.Volume)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
	}

	if err := buffered.Flush(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(tempPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr == nil {
			if retryErr := os.Rename(tempPath, path); retryErr == nil {
				success = true
				return nil
			}
		}
		return err
	}
	success = true
	return nil
}

func encodeHistoricalPrice(value float64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(math.Round(value * historicalPriceScale))
}

func decodeHistoricalPrice(value uint64) float64 {
	if value == 0 {
		return 0
	}
	return float64(value) / historicalPriceScale
}

func writeVarint(writer io.Writer, value int64) error {
	var buffer [binary.MaxVarintLen64]byte
	size := binary.PutVarint(buffer[:], value)
	_, err := writer.Write(buffer[:size])
	return err
}

func writeUvarint(writer io.Writer, value uint64) error {
	var buffer [binary.MaxVarintLen64]byte
	size := binary.PutUvarint(buffer[:], value)
	_, err := writer.Write(buffer[:size])
	return err
}

func readVarint(reader io.ByteReader) (int64, error) {
	return binary.ReadVarint(reader)
}

func readUvarint(reader io.ByteReader) (uint64, error) {
	return binary.ReadUvarint(reader)
}

func writeString(writer io.Writer, value string) error {
	if err := writeUvarint(writer, uint64(len(value))); err != nil {
		return err
	}
	_, err := io.WriteString(writer, value)
	return err
}

func writeStringBytes(writer io.Writer, value string) error {
	_, err := io.WriteString(writer, value)
	return err
}

func readString(reader *bufio.Reader) (string, error) {
	size, err := readUvarint(reader)
	if err != nil {
		return "", err
	}
	buffer := make([]byte, size)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", err
	}
	return string(buffer), nil
}
