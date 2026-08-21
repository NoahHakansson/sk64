package project

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/NoahHakansson/sk64/internal/k8s"
)

// KindNamespace identifies a suggested project namespace.
const KindNamespace = k8s.KindNamespace

// RenderMode records how a suggestion's source file was interpreted.
type RenderMode string

const (
	// ModeManifest marks suggestions parsed directly from Kubernetes YAML.
	ModeManifest RenderMode = "manifest"
	// ModeRendered marks suggestions parsed from rendered tool output.
	ModeRendered RenderMode = "rendered"
	// ModeLiteral marks suggestions extracted without rendering.
	ModeLiteral RenderMode = "literal"
	// ModeCI marks suggestions extracted from CI configuration.
	ModeCI RenderMode = "ci"
)

// Suggestion is one confirmable scan finding.
type Suggestion struct {
	Kind       string
	Name       string
	Namespace  string
	NamePrefix bool
	File       string
	Line       int
	Mode       RenderMode
	Detail     string
}

// Provenance returns file or file:line for display.
func (s Suggestion) Provenance() string {
	if s.Line == 0 {
		return s.File
	}
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

// ModeLabel returns the bracketed mode tag, such as [rendered: kustomize].
func (s Suggestion) ModeLabel() string {
	label := string(s.Mode)
	if s.Detail != "" {
		label += ": " + s.Detail
	}
	return "[" + label + "]"
}

// DisplayName returns the name, with a -* suffix for generated-name prefixes.
func (s Suggestion) DisplayName() string {
	if s.NamePrefix {
		return s.Name + "-*"
	}
	return s.Name
}

const (
	// DefaultMaxDepth is the default scanner directory depth limit.
	DefaultMaxDepth = 12
	// DefaultMaxFiles is the default scanner file count limit.
	DefaultMaxFiles      = 20000
	maxScanFileBytes     = 4 << 20
	scanSkipPrefix       = "file skipped: "
	defaultNoteSeparator = " — "
)

var (
	errScanFileOutsideRoot = errors.New("symlink target outside repository")
	errScanFileDangling    = errors.New("dangling symlink")
	errScanFileNotRegular  = errors.New("not a regular file")
	errScanFileTooLarge    = fmt.Errorf("larger than %d MiB", maxScanFileBytes>>20)
	errScanFileChanged     = errors.New("path changed during scan")
)

var scanSkipReasons = []error{
	errScanFileOutsideRoot,
	errScanFileDangling,
	errScanFileNotRegular,
	errScanFileTooLarge,
	errScanFileChanged,
}

// ScanOptions configures one repository scan.
type ScanOptions struct {
	Root     string
	MaxDepth int
	MaxFiles int
	// DefaultNamespace fills Namespace on namespaced suggestions whose source
	// sets no metadata.namespace during deduplication. Cluster-scoped Namespace
	// suggestions keep an empty Namespace.
	DefaultNamespace string
	// NoteSeparator joins scanner-generated note clauses.
	NoteSeparator string
}

// ScanResult is a completed scan.
type ScanResult struct {
	Suggestions  []Suggestion
	ContextHints []string
	Notes        []string
	Incomplete   bool
}

type scanFiles struct {
	manifests     []string
	kustomizeDirs []string
	chartDirs     []string
	ciFiles       []string
	notes         []string
	incomplete    bool
}

func (opts ScanOptions) withDefaults() ScanOptions {
	if opts.MaxDepth == 0 {
		opts.MaxDepth = DefaultMaxDepth
	}
	if opts.MaxFiles == 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.NoteSeparator == "" {
		opts.NoteSeparator = defaultNoteSeparator
	}
	return opts
}

// Scan walks a repository and extracts confirmable project suggestions.
func Scan(ctx context.Context, opts ScanOptions) (ScanResult, error) {
	opts = opts.withDefaults()
	info, err := os.Stat(opts.Root)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan repository %q: %w", opts.Root, err)
	}
	if !info.IsDir() {
		return ScanResult{}, fmt.Errorf("scan repository %q: root is not a directory", opts.Root)
	}
	resolvedRoot, err := filepath.EvalSymlinks(opts.Root)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan repository %q: %w", opts.Root, err)
	}
	opts.Root = resolvedRoot
	files, err := walkScanFiles(ctx, opts)
	if err != nil {
		return ScanResult{}, err
	}

	runner := execToolRunner{timeout: defaultRenderTimeout}
	result := ScanResult{Notes: append([]string(nil), files.notes...), Incomplete: files.incomplete}
	var ciSuggestions []Suggestion
	var contextHints []string
	var valuesHints []string
	for _, relPath := range files.ciFiles {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, fmt.Errorf("scan repository: %w", err)
		}
		data, err := readScanFile(opts.Root, relPath)
		if err != nil {
			result.Notes = appendScanFileNote(result.Notes, err)
			result.Incomplete = true
			continue
		}
		hints := extractCI(relPath, data)
		ciSuggestions = append(ciSuggestions, hints.namespaces...)
		contextHints = append(contextHints, hints.contexts...)
		result.Notes = append(result.Notes, hints.contextNotes...)
		valuesHints = append(valuesHints, hints.valuesFiles...)
	}

	for _, relPath := range files.manifests {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, fmt.Errorf("scan repository: %w", err)
		}
		data, err := readScanFile(opts.Root, relPath)
		if err != nil {
			result.Notes = appendScanFileNote(result.Notes, err)
			result.Incomplete = true
			continue
		}
		result.Suggestions = append(result.Suggestions, extractManifest(relPath, data)...)
	}

	var kustomizeArgs []string
	if len(files.kustomizeDirs) > 0 {
		if tool, ok := findTool("kustomize"); ok {
			kustomizeArgs = kustomizeArgv(tool, "")
		} else if tool, ok := findTool("kubectl"); ok {
			kustomizeArgs = kustomizeArgv("", tool)
		}
	}
	for _, dirRel := range files.kustomizeDirs {
		suggestions, notes, incomplete := extractKustomize(ctx, opts.Root, dirRel, kustomizeArgs, opts.NoteSeparator, runner)
		result.Suggestions = append(result.Suggestions, suggestions...)
		result.Notes = append(result.Notes, notes...)
		result.Incomplete = result.Incomplete || incomplete
		if err := ctx.Err(); err != nil {
			return ScanResult{}, fmt.Errorf("scan repository: %w", err)
		}
	}

	helmPath := ""
	if len(files.chartDirs) > 0 {
		if tool, ok := findTool("helm"); ok {
			helmPath = tool
		}
	}
	for _, dirRel := range files.chartDirs {
		suggestions, notes, incomplete := extractHelm(ctx, opts.Root, dirRel, helmPath, valuesHints, opts.NoteSeparator, runner)
		result.Suggestions = append(result.Suggestions, suggestions...)
		result.Notes = append(result.Notes, notes...)
		result.Incomplete = result.Incomplete || incomplete
		if err := ctx.Err(); err != nil {
			return ScanResult{}, fmt.Errorf("scan repository: %w", err)
		}
	}
	result.Suggestions = append(result.Suggestions, ciSuggestions...)
	result.Suggestions = dedupeAndSort(result.Suggestions, opts.DefaultNamespace)
	result.ContextHints = dedupeStrings(contextHints)
	result.Notes = summarizeScanNotes(result.Notes)
	return result, nil
}

func walkScanFiles(ctx context.Context, opts ScanOptions) (scanFiles, error) {
	matcher := newIgnoreMatcher()
	var files scanFiles
	fileCount := 0
	err := filepath.WalkDir(opts.Root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if filePath == opts.Root {
				return walkErr
			}
			files.incomplete = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(opts.Root, filePath)
		if err != nil {
			return nil
		}
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if entry.Name() == ".git" && rel != "" {
				return fs.SkipDir
			}
			if rel != "" && (pathDepth(rel) > opts.MaxDepth || matcher.ignored(rel, true)) {
				return fs.SkipDir
			}
			if data, err := safeReadFile(opts.Root, filepath.Join(filePath, ".gitignore")); err == nil {
				matcher.addFile(rel, data)
			} else {
				files.notes = appendScanFileNote(files.notes, err)
				if !errors.Is(err, fs.ErrNotExist) {
					files.incomplete = true
				}
			}
			return nil
		}
		fileCount++
		if !matcher.ignored(rel, false) {
			classifyScanFile(rel, &files)
		}
		if fileCount >= opts.MaxFiles {
			files.notes = append(files.notes, fmt.Sprintf("file cap reached (%d)%sscan truncated", opts.MaxFiles, opts.NoteSeparator))
			files.incomplete = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return scanFiles{}, fmt.Errorf("scan repository: %w", err)
		}
		return scanFiles{}, fmt.Errorf("walk repository %q: %w", opts.Root, err)
	}
	// Each kustomization marker appends its directory, so one directory may appear more than once.
	files.kustomizeDirs = dedupeStrings(files.kustomizeDirs)
	files.manifests = dropChartTemplateManifests(files.manifests, files.chartDirs)
	return files, nil
}

func dropChartTemplateManifests(manifests, chartDirs []string) []string {
	for _, chartDir := range chartDirs {
		templatePrefix := filepath.ToSlash(filepath.Join(chartDir, "templates")) + "/"
		kept := manifests[:0]
		for _, rel := range manifests {
			if !strings.HasPrefix(rel, templatePrefix) {
				kept = append(kept, rel)
			}
		}
		manifests = kept
	}
	return manifests
}

func pathDepth(rel string) int { return strings.Count(rel, "/") }

func classifyScanFile(rel string, files *scanFiles) {
	dir, name := filepath.ToSlash(filepath.Dir(rel)), filepath.Base(rel)
	if dir == "." {
		dir = ""
	}
	switch name {
	case "kustomization.yaml", "kustomization.yml", "Kustomization":
		files.kustomizeDirs = append(files.kustomizeDirs, dir)
		return
	case "Chart.yaml":
		files.chartDirs = append(files.chartDirs, dir)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".yaml" && ext != ".yml" {
		return
	}
	if rel == ".gitlab-ci.yml" || (strings.HasPrefix(rel, ".github/workflows/") && pathDepth(strings.TrimPrefix(rel, ".github/workflows/")) == 0) {
		files.ciFiles = append(files.ciFiles, rel)
		return
	}
	files.manifests = append(files.manifests, rel)
}

func readScanFile(root, relPath string) ([]byte, error) {
	return safeReadFile(root, filepath.Join(root, filepath.FromSlash(relPath)))
}

// safeReadFile reads a regular file no larger than maxScanFileBytes, refusing
// paths whose symlinks resolve outside root. root must already be
// symlink-resolved. Containment is checked against that resolution, so it holds
// for symlinks committed to the repository, not against a tree being mutated
// concurrently: a directory component swapped for a symlink between the check
// and the open would not be caught (O_NOFOLLOW only covers the final component).
func safeReadFile(root, filePath string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if info, lstatErr := os.Lstat(filePath); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errScanFileDangling
			}
		}
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errScanFileOutsideRoot
	}
	file, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- the resolved path is contained in the resolved scan root.
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errScanFileChanged
		}
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errScanFileNotRegular
	}
	if info.Size() > maxScanFileBytes {
		return nil, errScanFileTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, maxScanFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScanFileBytes {
		return nil, errScanFileTooLarge
	}
	return data, nil
}

func appendScanFileNote(notes []string, err error) []string {
	if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return notes
	}
	for _, reason := range scanSkipReasons {
		if errors.Is(err, reason) {
			return append(notes, scanSkipPrefix+reason.Error())
		}
	}
	return append(notes, scanSkipPrefix+"unreadable")
}

func dedupeAndSort(suggestions []Suggestion, defaultNamespace string) []Suggestion {
	type key struct {
		kind, namespace, name string
		prefix                bool
	}
	seen := make(map[key]struct{})
	result := make([]Suggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if suggestion.Kind == "" || suggestion.Name == "" {
			continue
		}
		itemKey := key{suggestion.Kind, resolvedNamespace(suggestion, defaultNamespace), suggestion.Name, suggestion.NamePrefix}
		suggestion.Namespace = itemKey.namespace
		if _, ok := seen[itemKey]; ok {
			continue
		}
		seen[itemKey] = struct{}{}
		result = append(result, suggestion)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if kindRank(left.Kind) != kindRank(right.Kind) {
			return kindRank(left.Kind) < kindRank(right.Kind)
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Namespace < right.Namespace
	})
	return result
}

func resolvedNamespace(suggestion Suggestion, defaultNamespace string) string {
	if suggestion.Kind == KindNamespace || suggestion.Namespace != "" {
		return suggestion.Namespace
	}
	return defaultNamespace
}

var kindOrder = map[string]int{
	KindNamespace:       0,
	k8s.KindDeployment:  1,
	k8s.KindStatefulSet: 2,
	k8s.KindDaemonSet:   3,
	k8s.KindJob:         4,
	k8s.KindCronJob:     5,
	k8s.KindSecret:      6,
	k8s.KindConfigMap:   7,
}

func kindRank(kind string) int {
	if rank, ok := kindOrder[kind]; ok {
		return rank
	}
	return len(kindOrder)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func summarizeScanNotes(notes []string) []string {
	var ordinary []string
	counts := make(map[string]int)
	var reasons []string
	for _, note := range notes {
		if !strings.HasPrefix(note, scanSkipPrefix) {
			ordinary = append(ordinary, note)
			continue
		}
		reason := strings.TrimPrefix(note, scanSkipPrefix)
		if counts[reason] == 0 {
			reasons = append(reasons, reason)
		}
		counts[reason]++
	}
	result := dedupeStrings(ordinary)
	for _, reason := range reasons {
		count := counts[reason]
		noun := "file"
		if count != 1 {
			noun = "files"
		}
		result = append(result, fmt.Sprintf("%d %s skipped: %s", count, noun, reason))
	}
	return result
}
