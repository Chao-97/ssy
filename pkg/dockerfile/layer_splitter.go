package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/replicate/cog/pkg/dockerignore"
)

const (
	DefaultMaxLayerSizeGB = 50
	MaxLayersAWSECR       = 1000 // AWS ECR layer limit
	GBToBytes             = 1024 * 1024 * 1024
)

// FileBatch represents a batch of files that will be copied in a single COPY command
type FileBatch struct {
	Files      []string
	TotalSize  int64
	LayerIndex int
}

// LayerSplitter handles splitting files into multiple Docker layers based on size constraints
type LayerSplitter struct {
	maxLayerSizeBytes int64
	maxLayers         int
	sourceDir         string
	excludePaths      []string          // Paths to exclude from splitting (like .dockerignore patterns)
	layerFirst        bool              // If true, prioritize layering over whole directory copying
	ignoreMatcher     *ignore.GitIgnore // Docker ignore pattern matcher
}

// NewLayerSplitter creates a new LayerSplitter instance
func NewLayerSplitter(maxLayerSizeGB int64, sourceDir string) *LayerSplitter {
	if maxLayerSizeGB <= 0 {
		maxLayerSizeGB = DefaultMaxLayerSizeGB
	}

	// Create dockerignore matcher
	ignoreMatcher, err := dockerignore.CreateMatcher(sourceDir)
	if err != nil {
		// If we can't read .dockerignore, proceed without it
		// This is a non-fatal error
		ignoreMatcher = nil
	}

	return &LayerSplitter{
		maxLayerSizeBytes: maxLayerSizeGB * GBToBytes,
		maxLayers:         MaxLayersAWSECR,
		sourceDir:         sourceDir,
		excludePaths:      make([]string, 0),
		layerFirst:        true, // Default to layer-first strategy
		ignoreMatcher:     ignoreMatcher,
	}
}

// SetLayerFirst enables or disables layer-first strategy
func (ls *LayerSplitter) SetLayerFirst(enabled bool) {
	ls.layerFirst = enabled
}

// SetExcludePaths sets paths that should be excluded from layer splitting
func (ls *LayerSplitter) SetExcludePaths(paths []string) {
	ls.excludePaths = paths
}

// FileInfo represents a file with its path and size
type FileInfo struct {
	Path string
	Size int64
}

// ScanFiles scans the source directory and returns a list of files with their sizes
func (ls *LayerSplitter) ScanFiles() ([]FileInfo, error) {
	var files []FileInfo

	err := dockerignore.Walk(ls.sourceDir, ls.ignoreMatcher, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Get relative path from source directory
		relPath, err := filepath.Rel(ls.sourceDir, path)
		if err != nil {
			return err
		}

		// Skip excluded paths (additional manual exclusions)
		if ls.isExcluded(relPath) {
			return nil
		}

		files = append(files, FileInfo{
			Path: relPath,
			Size: info.Size(),
		})

		return nil
	})

	return files, err
}

// isExcluded checks if a file path should be excluded based on exclude patterns
func (ls *LayerSplitter) isExcluded(path string) bool {
	for _, excludePath := range ls.excludePaths {
		// Simple pattern matching - can be enhanced with glob patterns if needed
		if strings.HasPrefix(path, excludePath) {
			return true
		}
		if matched, _ := filepath.Match(excludePath, path); matched {
			return true
		}
	}
	return false
}

// SplitIntoLayers splits files into batches that fit within both size and count limits
func (ls *LayerSplitter) SplitIntoLayers(files []FileInfo) ([]FileBatch, error) {
	// First check for files that exceed the size limit
	for _, file := range files {
		if file.Size > ls.maxLayerSizeBytes {
			return nil, fmt.Errorf("file %s (%d bytes) exceeds the maximum layer size limit (%d bytes). Consider splitting or compressing this file",
				file.Path, file.Size, ls.maxLayerSizeBytes)
		}
	}

	// Build directory structure and analyze sizes
	dirStructure := ls.buildDirectoryStructure(files)

	// Generate copy operations based on directory analysis
	copyOps, err := ls.generateCopyOperations(dirStructure)
	if err != nil {
		return nil, fmt.Errorf("failed to generate copy operations: %w", err)
	}

	// Check layer count limit
	if len(copyOps) > ls.maxLayers {
		return nil, fmt.Errorf("generated %d layers, which exceeds AWS ECR limit of %d layers. Consider increasing max_layer_size_gb or reducing file count",
			len(copyOps), ls.maxLayers)
	}

	// Convert copy operations to batches
	batches := make([]FileBatch, len(copyOps))
	for i, op := range copyOps {
		batches[i] = FileBatch{
			Files:      op.Files,
			TotalSize:  op.TotalSize,
			LayerIndex: i,
		}
	}

	return batches, nil
}

// DirectoryNode represents a node in the directory tree
type DirectoryNode struct {
	Path         string
	Files        []FileInfo
	Subdirs      map[string]*DirectoryNode
	TotalSize    int64
	CanCopyWhole bool // Whether this directory can be copied as a unit
}

// CopyOperation represents a single copy operation for a layer
type CopyOperation struct {
	Type      string   // "file", "directory", or "files"
	Source    string   // Source path
	Files     []string // List of files (for Type="files")
	TotalSize int64
}

// buildDirectoryStructure builds a tree structure of directories and files
func (ls *LayerSplitter) buildDirectoryStructure(files []FileInfo) *DirectoryNode {
	root := &DirectoryNode{
		Path:      "",
		Files:     make([]FileInfo, 0),
		Subdirs:   make(map[string]*DirectoryNode),
		TotalSize: 0,
	}

	for _, file := range files {
		ls.addFileToTree(root, file)
	}

	// Calculate total sizes and determine copy strategies
	ls.calculateSizesAndStrategies(root)

	return root
}

// addFileToTree adds a file to the directory tree
func (ls *LayerSplitter) addFileToTree(node *DirectoryNode, file FileInfo) {
	parts := strings.Split(filepath.Clean(file.Path), string(filepath.Separator))

	if len(parts) == 1 {
		// File is in current directory
		node.Files = append(node.Files, file)
		node.TotalSize += file.Size
		return
	}

	// File is in a subdirectory
	dirName := parts[0]
	if node.Subdirs[dirName] == nil {
		node.Subdirs[dirName] = &DirectoryNode{
			Path:      filepath.Join(node.Path, dirName),
			Files:     make([]FileInfo, 0),
			Subdirs:   make(map[string]*DirectoryNode),
			TotalSize: 0,
		}
	}

	// Create new file info with relative path from subdirectory
	remainingPath := filepath.Join(parts[1:]...)
	subFile := FileInfo{
		Path: remainingPath,
		Size: file.Size,
	}

	ls.addFileToTree(node.Subdirs[dirName], subFile)
	node.TotalSize += file.Size
}

// calculateSizesAndStrategies determines the copy strategy for each directory
func (ls *LayerSplitter) calculateSizesAndStrategies(node *DirectoryNode) {
	// First, process all subdirectories
	for _, subdir := range node.Subdirs {
		ls.calculateSizesAndStrategies(subdir)
	}

	// Determine if this directory can be copied as a whole
	canFitInOneLayer := node.TotalSize <= ls.maxLayerSizeBytes

	if ls.layerFirst {
		// Layer-first strategy: only copy whole directory if it's small and has no subdirectories to split
		// This encourages better layering and caching
		node.CanCopyWhole = canFitInOneLayer && ls.shouldCopyWholeInLayerFirstMode(node)
	} else {
		// Size-first strategy: copy whole directory if it fits in one layer
		node.CanCopyWhole = canFitInOneLayer
	}
}

// shouldCopyWholeInLayerFirstMode determines if a directory should be copied whole in layer-first mode
func (ls *LayerSplitter) shouldCopyWholeInLayerFirstMode(node *DirectoryNode) bool {
	// Define threshold: min(2GB, max_layer_size)
	thresholdBytes := int64(2 * GBToBytes) // 2GB
	if ls.maxLayerSizeBytes < thresholdBytes {
		thresholdBytes = ls.maxLayerSizeBytes
	}

	// If directory is smaller than threshold, copy it whole to avoid over-fragmentation
	if node.TotalSize < thresholdBytes {
		return true
	}

	// If directory has many small files but no subdirs, copy it whole to avoid too many layers
	if len(node.Subdirs) == 0 && len(node.Files) > 20 {
		return true
	}

	// For larger directories, prefer to split for better caching
	return false
}

// generateCopyOperations generates the optimal copy operations
func (ls *LayerSplitter) generateCopyOperations(root *DirectoryNode) ([]CopyOperation, error) {
	var operations []CopyOperation

	// Process root files first
	if len(root.Files) > 0 {
		ops, err := ls.generateFileOperations(root.Files, "")
		if err != nil {
			return nil, err
		}
		operations = append(operations, ops...)
	}

	// Process subdirectories
	for dirName, subdir := range root.Subdirs {
		ops, err := ls.generateDirectoryOperations(dirName, subdir)
		if err != nil {
			return nil, err
		}
		operations = append(operations, ops...)
	}

	return operations, nil
}

// generateDirectoryOperations generates copy operations for a directory
func (ls *LayerSplitter) generateDirectoryOperations(dirName string, node *DirectoryNode) ([]CopyOperation, error) {
	var operations []CopyOperation

	if node.CanCopyWhole {
		// Directory fits in one layer - copy the whole directory
		operation := CopyOperation{
			Type:      "directory",
			Source:    dirName,
			Files:     []string{dirName + "/"},
			TotalSize: node.TotalSize,
		}
		operations = append(operations, operation)
	} else {
		// Directory is too large - need to split it

		// First, copy files in this directory
		if len(node.Files) > 0 {
			ops, err := ls.generateFileOperations(node.Files, dirName)
			if err != nil {
				return nil, err
			}
			operations = append(operations, ops...)
		}

		// Then, process subdirectories
		for subdirName, subdir := range node.Subdirs {
			fullSubdirPath := filepath.Join(dirName, subdirName)
			ops, err := ls.generateDirectoryOperations(fullSubdirPath, subdir)
			if err != nil {
				return nil, err
			}
			operations = append(operations, ops...)
		}
	}

	return operations, nil
}

// generateFileOperations generates copy operations for a list of files
func (ls *LayerSplitter) generateFileOperations(files []FileInfo, basePath string) ([]CopyOperation, error) {
	// Sort files by size (largest first) for better packing
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size > files[j].Size
	})

	if ls.layerFirst {
		// Layer-first strategy: create more layers for better caching
		return ls.generateLayerFirstOperations(files, basePath)
	} else {
		// Size-first strategy: pack files efficiently to minimize layers
		return ls.generateSizeFirstOperations(files, basePath)
	}
}

// generateLayerFirstOperations creates more layers prioritizing caching benefits
func (ls *LayerSplitter) generateLayerFirstOperations(files []FileInfo, basePath string) ([]CopyOperation, error) {
	var operations []CopyOperation

	// Target layer size for better caching (smaller than max)
	targetLayerSize := ls.maxLayerSizeBytes / 2 // Use 50% of max size as target
	if targetLayerSize < 1*GBToBytes {
		targetLayerSize = 1 * GBToBytes // Minimum 1GB per layer
	}

	var currentBatch []string
	var currentSize int64

	for _, file := range files {
		filePath := file.Path
		if basePath != "" {
			filePath = filepath.Join(basePath, file.Path)
		}

		// If adding this file would exceed the target size, start a new batch
		// Or if current batch reaches maximum size limit
		shouldStartNewBatch := (currentSize+file.Size > targetLayerSize && len(currentBatch) > 0) ||
			(currentSize+file.Size > ls.maxLayerSizeBytes)

		if shouldStartNewBatch {
			operations = append(operations, CopyOperation{
				Type:      "files",
				Files:     append([]string{}, currentBatch...),
				TotalSize: currentSize,
			})
			currentBatch = nil
			currentSize = 0
		}

		currentBatch = append(currentBatch, filePath)
		currentSize += file.Size
	}

	// Add the last batch if it has files
	if len(currentBatch) > 0 {
		operations = append(operations, CopyOperation{
			Type:      "files",
			Files:     currentBatch,
			TotalSize: currentSize,
		})
	}

	return operations, nil
}

// generateSizeFirstOperations packs files efficiently to minimize layer count
func (ls *LayerSplitter) generateSizeFirstOperations(files []FileInfo, basePath string) ([]CopyOperation, error) {
	var operations []CopyOperation

	var currentBatch []string
	var currentSize int64

	for _, file := range files {
		filePath := file.Path
		if basePath != "" {
			filePath = filepath.Join(basePath, file.Path)
		}

		// If adding this file would exceed the limit, start a new batch
		if currentSize+file.Size > ls.maxLayerSizeBytes && len(currentBatch) > 0 {
			operations = append(operations, CopyOperation{
				Type:      "files",
				Files:     append([]string{}, currentBatch...),
				TotalSize: currentSize,
			})
			currentBatch = nil
			currentSize = 0
		}

		currentBatch = append(currentBatch, filePath)
		currentSize += file.Size
	}

	// Add the last batch if it has files
	if len(currentBatch) > 0 {
		operations = append(operations, CopyOperation{
			Type:      "files",
			Files:     currentBatch,
			TotalSize: currentSize,
		})
	}

	return operations, nil
}

// GenerateCopyCommands generates Docker COPY commands for each batch
func (ls *LayerSplitter) GenerateCopyCommands(batches []FileBatch, targetDir string) []string {
	var commands []string

	for i, batch := range batches {
		if len(batch.Files) == 0 {
			continue
		}

		// Add comment to identify the layer
		commands = append(commands, fmt.Sprintf("# Layer %d: %d items (~%.2f GB)",
			i+1, len(batch.Files), float64(batch.TotalSize)/float64(GBToBytes)))

		// Generate copy commands for this batch
		for _, file := range batch.Files {
			if strings.HasSuffix(file, "/") {
				// Directory copy
				dirName := strings.TrimSuffix(file, "/")
				commands = append(commands, fmt.Sprintf("COPY %s %s/", dirName, filepath.Join(targetDir, dirName)))
			} else {
				// File copy
				commands = append(commands, fmt.Sprintf("COPY %s %s", file, filepath.Join(targetDir, file)))
			}
		}
	}

	return commands
}

// GetLayerSizeInfo returns information about the layer sizes
func (ls *LayerSplitter) GetLayerSizeInfo(batches []FileBatch) string {
	if len(batches) == 0 {
		return "No layers generated"
	}

	// Find max layer size and total size
	var maxLayerSize int64
	var totalSize int64
	totalItems := 0

	for _, batch := range batches {
		if batch.TotalSize > maxLayerSize {
			maxLayerSize = batch.TotalSize
		}
		totalSize += batch.TotalSize
		totalItems += len(batch.Files)
	}

	maxLayerSizeGB := float64(maxLayerSize) / float64(GBToBytes)
	totalSizeGB := float64(totalSize) / float64(GBToBytes)

	return fmt.Sprintf("Generated %d layers, %d items total (%.2f GB), largest layer: %.2f GB",
		len(batches), totalItems, totalSizeGB, maxLayerSizeGB)
}
