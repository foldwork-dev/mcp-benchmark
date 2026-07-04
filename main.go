// Command mcp-benchmark scans a local directory and prints a formatted
// table showing per-file and aggregate token savings achieved by the
// mcp-injector AST compression engine.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/foldwork-dev/mcp-benchmark/compress"
)

// Version is the benchmark CLI version, overridden at build time via -ldflags.
var Version = "dev"

// supportedExtensions is the set of file extensions the benchmark scans.
var supportedExtensions = map[string]bool{
	".go": true, ".java": true, ".py": true, ".pyw": true,
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".rs": true, ".cs": true, ".cpp": true, ".cc": true,
	".c": true, ".h": true, ".hpp": true,
}

// defaultPricePerMillion is the Claude Sonnet input token price in USD.
const defaultPricePerMillion = 3.00

// flatFallbackReductionPct is the conservative savings estimate used for
// extensions not explicitly handled by the AST scanner.
const flatFallbackReductionPct = 0.40

// bytesPerToken is the heuristic: tokens ≈ bytes / 3.5
const bytesPerToken = 3.5

// FileResult holds per-file benchmark data.
type FileResult struct {
	Path              string  `json:"path"`
	RawLines          int     `json:"raw_lines"`
	RawBytes          int     `json:"raw_bytes"`
	RawTokens         int     `json:"raw_tokens"`
	CompressedBytes   int     `json:"compressed_bytes"`
	CompressedTokens  int     `json:"compressed_tokens"`
	SavingsPct        float64 `json:"savings_pct"`
	CostSavedPerRun   float64 `json:"cost_saved_per_run_usd"`
	Estimated         bool    `json:"estimated"` // true when fallback 40% was used
}

// BenchmarkResult holds the aggregate result.
type BenchmarkResult struct {
	Directory         string        `json:"directory"`
	Tier              int           `json:"compression_tier"`
	PricePerMillion   float64       `json:"price_per_million_usd"`
	TotalFiles        int           `json:"total_files"`
	TotalRawTokens    int           `json:"total_raw_tokens"`
	TotalCompTokens   int           `json:"total_compressed_tokens"`
	TotalSavingsPct   float64       `json:"total_savings_pct"`
	TotalCostSaved    float64       `json:"total_cost_saved_per_run_usd"`
	Files             []FileResult  `json:"files"`
	GeneratedAt       string        `json:"generated_at"`
}

var (
	flagJSON       = flag.Bool("json", false, "Emit JSON instead of formatted table")
	flagMinSavings = flag.Float64("min-savings", 0, "Skip files below this savings %")
	flagTier       = flag.Int("tier", 3, "Compression tier (2=fold, 3=fold+strip)")
	flagPrice      = flag.Float64("price-per-million", defaultPricePerMillion,
		"Token price in USD per 1M tokens")
	flagVersion    = flag.Bool("version", false, "Print version and exit")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mcp-benchmark [flags] <directory>\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *flagTier < 1 || *flagTier > 3 {
		fmt.Fprintf(os.Stderr, "Error: --tier must be 1, 2, or 3 (got %d)\n", *flagTier)
		os.Exit(1)
	}

	if *flagVersion {
		v := Version
		if v != "dev" && !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		fmt.Printf("mcp-benchmark %s\n", v)
		os.Exit(0)
	}

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	dir, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		log.Fatalf("resolve path: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		log.Fatalf("%q is not a valid directory", dir)
	}

	result, err := runBenchmark(dir, *flagTier, *flagPrice)
	if err != nil {
		log.Fatalf("benchmark: %v", err)
	}

	if len(result.Files) == 0 {
		fmt.Printf("No supported files found in %s\n", dir)
		fmt.Printf("Supported extensions: .go .java .py .ts .tsx .js .jsx .rs .proto .vue .svelte .astro\n")
		os.Exit(0)
	}

	if *flagMinSavings > 0 {
		filtered := result.Files[:0]
		for _, f := range result.Files {
			if f.SavingsPct >= *flagMinSavings {
				filtered = append(filtered, f)
			}
		}
		result.Files = filtered
	}

	if *flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatalf("json encode: %v", err)
		}
	} else {
		printTable(result)
	}

	appendLog(result)
}

func runBenchmark(dir string, tier int, pricePerMillion float64) (*BenchmarkResult, error) {
	var files []FileResult
	ignorePatterns := loadGitignore(dir)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == ".mcp-injector" ||
				base == "vendor" || base == "target" || base == "__pycache__" ||
				base == ".gradle" || base == "build" || base == "dist" {
				return filepath.SkipDir
			}
			relPath, errRel := filepath.Rel(dir, path)
			if errRel == nil && relPath != "." {
				if matchesGitignore(ignorePatterns, relPath) {
					return filepath.SkipDir
				}
			}
			return nil
		}

		relPath, errRel := filepath.Rel(dir, path)
		if errRel == nil && relPath != "." {
			if matchesGitignore(ignorePatterns, relPath) {
				return nil
			}
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExtensions[ext] {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.Size() > 5*1024*1024 {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if len(src) == 0 {
			return nil
		}

		limit := 512
		if len(src) < limit {
			limit = len(src)
		}
		if isBinary(src[:limit]) {
			return nil
		}

		rawBytes := len(src)
		rawLines := strings.Count(string(src), "\n") + 1
		rawTokens := bytesToTokens(rawBytes)

		var compressedBytes int
		estimated := !compress.IsStructurallySupported(path)

		if estimated {
			compressedBytes = int(math.Round(float64(rawBytes) * (1 - flatFallbackReductionPct)))
		} else {
			compressed, err := compress.Compress(path, src, tier)
			if err != nil {
				estimated = true
				compressedBytes = int(math.Round(float64(rawBytes) * (1 - flatFallbackReductionPct)))
			} else {
				compressedBytes = len(compressed)
			}
		}

		compressedTokens := bytesToTokens(compressedBytes)
		savingsPct := 0.0
		if rawTokens > 0 {
			savingsPct = (1.0 - float64(compressedTokens)/float64(rawTokens)) * 100
		}

		tokensSaved := rawTokens - compressedTokens
		costSaved := float64(tokensSaved) / 1_000_000 * pricePerMillion

		files = append(files, FileResult{
			Path:             relPath,
			RawLines:         rawLines,
			RawBytes:         rawBytes,
			RawTokens:        rawTokens,
			CompressedBytes:  compressedBytes,
			CompressedTokens: compressedTokens,
			SavingsPct:       math.Round(savingsPct*10) / 10,
			CostSavedPerRun:  math.Round(costSaved*1000) / 1000,
			Estimated:        estimated,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].SavingsPct > files[j].SavingsPct
	})

	totalRaw, totalComp := 0, 0
	for _, f := range files {
		totalRaw += f.RawTokens
		totalComp += f.CompressedTokens
	}

	totalSavingsPct := 0.0
	if totalRaw > 0 {
		totalSavingsPct = (1.0 - float64(totalComp)/float64(totalRaw)) * 100
	}

	totalTokensSaved := totalRaw - totalComp
	totalCostSaved := float64(totalTokensSaved) / 1_000_000 * pricePerMillion

	return &BenchmarkResult{
		Directory:       dir,
		Tier:            tier,
		PricePerMillion: pricePerMillion,
		TotalFiles:      len(files),
		TotalRawTokens:  totalRaw,
		TotalCompTokens: totalComp,
		TotalSavingsPct: math.Round(totalSavingsPct*10) / 10,
		TotalCostSaved:  math.Round(totalCostSaved*1000) / 1000,
		Files:           files,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func printTable(r *BenchmarkResult) {
	const colFile = 42
	const colRaw = 12
	const colComp = 12
	const colSaved = 8
	const colCost = 12

	sep := strings.Repeat("─", colFile+colRaw+colComp+colSaved+colCost+4)

	fmt.Printf("\n════════════════════════════════════════════════════════════════════════════════\n")
	fmt.Printf("  mcp-benchmark — %s\n", filepath.Base(r.Directory))
	fmt.Printf("  Tier %d compression  |  $%.2f/1M tokens  |  %s\n",
		r.Tier, r.PricePerMillion, r.GeneratedAt)
	fmt.Printf("════════════════════════════════════════════════════════════════════════════════\n\n")

	fmt.Printf("%-*s  %*s  %*s  %*s  %*s\n",
		colFile, "FILE",
		colRaw, "RAW TOKENS",
		colComp, "COMPRESSED",
		colSaved, "SAVED",
		colCost, "COST SAVED*")
	fmt.Println(sep)

	for _, f := range r.Files {
		label := f.Path
		if f.Estimated {
			label += " [est]"
		}
		if utf8.RuneCountInString(label) > colFile {
			label = "..." + label[len(label)-(colFile-3):]
		}
		fmt.Printf("%-*s  %*s  %*s  %*s  %*s\n",
			colFile, label,
			colRaw, formatInt(f.RawTokens),
			colComp, formatInt(f.CompressedTokens),
			colSaved, fmt.Sprintf("%.1f%%", f.SavingsPct),
			colCost, fmt.Sprintf("$%.4f", f.CostSavedPerRun))
	}

	fmt.Println(sep)
	fmt.Printf("%-*s  %*s  %*s  %*s  %*s\n",
		colFile, fmt.Sprintf("TOTAL (%d files)", r.TotalFiles),
		colRaw, formatInt(r.TotalRawTokens),
		colComp, formatInt(r.TotalCompTokens),
		colSaved, fmt.Sprintf("%.1f%%", r.TotalSavingsPct),
		colCost, fmt.Sprintf("$%.4f", r.TotalCostSaved))
	fmt.Printf("\n  * Based on $%.2f / 1M input tokens\n\n", r.PricePerMillion)

	rawCostPerRun := float64(r.TotalRawTokens) / 1_000_000 * r.PricePerMillion
	compCostPerRun := float64(r.TotalCompTokens) / 1_000_000 * r.PricePerMillion
	savedPerRun := rawCostPerRun - compCostPerRun
	savedPerDay10x := savedPerRun * 10
	savedPerMonth := savedPerDay10x * 30

	fmt.Printf("  💡 Running this codebase through Claude 10×/day costs $%.2f/day raw.\n", rawCostPerRun*10)
	fmt.Printf("     With mcp-injector:  $%.2f/day.  You save $%.2f/day ($%.0f/month).\n\n",
		compCostPerRun*10, savedPerDay10x, savedPerMonth)
}

func appendLog(r *BenchmarkResult) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logDir := filepath.Join(home, ".mcp-injector")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	logPath := filepath.Join(logDir, "benchmark.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%d\t%d\t%.1f%%\n",
		r.GeneratedAt, r.Directory, r.TotalRawTokens, r.TotalCompTokens, r.TotalSavingsPct)
}

func bytesToTokens(b int) int {
	return int(math.Round(float64(b) / bytesPerToken))
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(ch))
	}
	return string(out)
}

func loadGitignore(dir string) []string {
	var patterns []string
	for _, filename := range []string{".gitignore", ".aiignore"} {
		path := filepath.Join(dir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // no file is fine
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}
	return patterns
}

func matchesGitignore(patterns []string, relPath string) bool {
	for _, pattern := range patterns {
		pat := strings.Trim(pattern, "/")
		base := filepath.Base(relPath)

		matched, _ := filepath.Match(pat, base)
		if matched {
			return true
		}
		matched, _ = filepath.Match(pat, relPath)
		if matched {
			return true
		}
		parts := strings.Split(relPath, string(filepath.Separator))
		for _, part := range parts {
			matched, _ = filepath.Match(pat, part)
			if matched {
				return true
			}
		}
	}
	return false
}

func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}
