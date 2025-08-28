package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/replicate/cog/pkg/dockerignore"
	"github.com/replicate/cog/pkg/util/console"
)

const (
	DefaultMaxLayerSizeGB = 50
	MaxLayersAWSECR       = 1000 // AWS ECR layer limit
	GBToBytes             = 1024 * 1024 * 1024
	MaxDockerPathDepth    = 7 // Docker path depth limit to avoid "max depth exceeded" errors - reduced from 7 to be more conservative
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
	Path       string
	Size       int64
	IsSymlink  bool
	LinkTarget string // For symlinks, the target path
}

// SymlinkGroup represents a group of files that should be kept together due to symlink relationships
type SymlinkGroup struct {
	Files []string
	Size  int64
}

// ScanFiles scans the source directory and returns a list of files with their sizes
func (ls *LayerSplitter) ScanFiles() ([]FileInfo, error) {
	var files []FileInfo
	var symlinkWarnings []string

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

		// Check if this is a symbolic link
		isSymlink := info.Mode()&os.ModeSymlink != 0
		var linkTarget string

		if isSymlink {
			// Get the target of the symlink
			target, err := os.Readlink(path)
			if err == nil {
				linkTarget = target
				// Check if target is relative and might be affected by layer splitting
				if !filepath.IsAbs(target) {
					symlinkWarnings = append(symlinkWarnings, fmt.Sprintf("Symlink %s -> %s", relPath, target))
				}
			}
		}

		files = append(files, FileInfo{
			Path:       relPath,
			Size:       info.Size(),
			IsSymlink:  isSymlink,
			LinkTarget: linkTarget,
		})

		return nil
	})

	// Print warnings about symlinks if any were found
	if len(symlinkWarnings) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: Found %d symbolic links that may be affected by layer splitting:\n", len(symlinkWarnings))
		for _, warning := range symlinkWarnings {
			fmt.Fprintf(os.Stderr, "  %s\n", warning)
		}
		fmt.Fprintf(os.Stderr, "Consider using --layer-first=false or adjusting directory grouping to preserve link relationships.\n")
	}

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

// getPathDepth calculates the depth of a file path
func (ls *LayerSplitter) getPathDepth(path string) int {
	if path == "" || path == "." {
		return 0
	}
	// Count directory separators + 1 for the file itself
	return strings.Count(filepath.Clean(path), string(filepath.Separator)) + 1
}

// isPathTooDeep checks if a path would exceed Docker's depth limits
func (ls *LayerSplitter) isPathTooDeep(path string) bool {
	return ls.getPathDepth(path) > MaxDockerPathDepth
}

// getTargetPathDepth calculates the depth of a path when placed in the target directory
func (ls *LayerSplitter) getTargetPathDepth(sourcePath, targetDir string) int {
	// Target directory depth (e.g., "/src" = 2 levels)
	targetDepth := ls.getPathDepth(targetDir)
	// Source path depth
	sourceDepth := ls.getPathDepth(sourcePath)
	// Combined depth - add 1 for safety margin
	return targetDepth + sourceDepth + 1
}

// isTargetPathTooDeep checks if a source path would be too deep when placed in target directory
func (ls *LayerSplitter) isTargetPathTooDeep(sourcePath, targetDir string) bool {
	return ls.getTargetPathDepth(sourcePath, targetDir) > MaxDockerPathDepth
}

// flattenPath creates a flattened version of a path to avoid depth issues
func (ls *LayerSplitter) flattenPath(sourcePath string) string {
	// Replace directory separators with underscores to create a flat path
	flattened := strings.ReplaceAll(sourcePath, string(filepath.Separator), "_")
	// Remove any leading/trailing underscores
	flattened = strings.Trim(flattened, "_")
	return flattened
}

// shortenPath attempts to create a shorter path that won't exceed depth limits
func (ls *LayerSplitter) shortenPath(sourcePath, targetDir string) string {
	if !ls.isTargetPathTooDeep(sourcePath, targetDir) {
		return sourcePath // Already short enough
	}

	// Extract filename and try to place it in a shallower directory
	filename := filepath.Base(sourcePath)

	// Try progressively shorter paths
	parts := strings.Split(filepath.Clean(sourcePath), string(filepath.Separator))

	// Try keeping fewer directory levels
	for i := len(parts) - 2; i >= 0; i-- {
		shortPath := filepath.Join(parts[i:]...)
		if !ls.isTargetPathTooDeep(shortPath, targetDir) {
			return shortPath
		}
	}

	// As last resort, just use the filename
	if !ls.isTargetPathTooDeep(filename, targetDir) {
		return filename
	}

	// If even the filename is too deep, flatten the entire path
	flatPath := ls.flattenPath(sourcePath)
	if !ls.isTargetPathTooDeep(flatPath, targetDir) {
		return flatPath
	}

	return "" // Can't shorten enough
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

	// Analyze symlink relationships before building directory structure
	symlinkGroups := ls.analyzeSymlinkRelationships(files)
	if len(symlinkGroups) > 0 {
		// Log symlink analysis results
		for commonDir, relatedFiles := range symlinkGroups {
			console.Infof("Found symlink group in %s with %d related files", commonDir, len(relatedFiles))
		}
	}

	// Build directory structure and analyze sizes
	dirStructure := ls.buildDirectoryStructure(files)

	// Apply symlink grouping constraints to directory structure
	ls.applySymlinkConstraints(dirStructure, symlinkGroups)

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

// analyzeSymlinkRelationships analyzes symlink relationships and groups related files
func (ls *LayerSplitter) analyzeSymlinkRelationships(files []FileInfo) map[string][]string {
	// Map from directory to list of files that should be kept together
	relatedGroups := make(map[string][]string)

	for _, file := range files {
		if !file.IsSymlink {
			continue
		}

		// Only handle relative symlinks that could be broken by layer splitting
		if filepath.IsAbs(file.LinkTarget) {
			continue
		}

		// Resolve the symlink target relative to the file's directory
		fileDir := filepath.Dir(file.Path)
		targetPath := filepath.Clean(filepath.Join(fileDir, file.LinkTarget))

		// Check if target exists in our file list
		targetExists := false
		for _, f := range files {
			if f.Path == targetPath {
				targetExists = true
				break
			}
		}

		if targetExists {
			// Find common directory that should contain both files
			commonDir := ls.findCommonDirectory(file.Path, targetPath)
			if commonDir != "" {
				relatedGroups[commonDir] = append(relatedGroups[commonDir], file.Path, targetPath)
			}
		}
	}

	// Remove duplicates
	for dir, group := range relatedGroups {
		seen := make(map[string]bool)
		var unique []string
		for _, path := range group {
			if !seen[path] {
				seen[path] = true
				unique = append(unique, path)
			}
		}
		relatedGroups[dir] = unique
	}

	return relatedGroups
}

// findCommonDirectory finds the common directory path for two files
func (ls *LayerSplitter) findCommonDirectory(path1, path2 string) string {
	dir1 := filepath.Dir(path1)
	dir2 := filepath.Dir(path2)

	// Find common prefix
	parts1 := strings.Split(dir1, string(filepath.Separator))
	parts2 := strings.Split(dir2, string(filepath.Separator))

	var commonParts []string
	minLen := len(parts1)
	if len(parts2) < minLen {
		minLen = len(parts2)
	}

	for i := 0; i < minLen; i++ {
		if parts1[i] == parts2[i] {
			commonParts = append(commonParts, parts1[i])
		} else {
			break
		}
	}

	if len(commonParts) == 0 {
		return "" // No common directory
	}

	return filepath.Join(commonParts...)
}

// applySymlinkConstraints modifies the directory structure to ensure symlinked files stay together
func (ls *LayerSplitter) applySymlinkConstraints(root *DirectoryNode, symlinkGroups map[string][]string) {
	for commonDir, relatedFiles := range symlinkGroups {
		// Mark the common directory as requiring whole-directory copy
		ls.markDirectoryForWholeCopy(root, commonDir, relatedFiles)
	}
}

// markDirectoryForWholeCopy marks a directory to be copied as a whole to preserve symlink relationships
func (ls *LayerSplitter) markDirectoryForWholeCopy(root *DirectoryNode, targetDir string, relatedFiles []string) {
	// Find the directory node
	node := ls.findDirectoryNode(root, targetDir)
	if node != nil {
		// Force this directory to be copied as a whole
		node.CanCopyWhole = true
		console.Infof("Marking directory %s for whole copy to preserve symlink relationships", targetDir)
	}
}

// findDirectoryNode finds a directory node in the tree by path
func (ls *LayerSplitter) findDirectoryNode(root *DirectoryNode, targetPath string) *DirectoryNode {
	if root.Path == targetPath {
		return root
	}

	for _, child := range root.Subdirs {
		if result := ls.findDirectoryNode(child, targetPath); result != nil {
			return result
		}
	}

	return nil
}

// shouldCopyWholeInLayerFirstMode determines if a directory should be copied whole in layer-first mode
func (ls *LayerSplitter) shouldCopyWholeInLayerFirstMode(node *DirectoryNode) bool {
	// If this directory was marked for whole copy due to symlink constraints, respect that
	if node.CanCopyWhole {
		return true
	}

	// Check if any files in this directory would exceed depth limit with target /src
	for _, file := range node.Files {
		fullPath := filepath.Join(node.Path, file.Path)
		if ls.isTargetPathTooDeep(fullPath, "/src") {
			// Force whole directory copy to avoid deep paths
			return node.TotalSize <= ls.maxLayerSizeBytes
		}
	}

	// Check subdirectories for depth issues
	for subdirName := range node.Subdirs {
		subdirPath := filepath.Join(node.Path, subdirName)
		if ls.isTargetPathTooDeep(subdirPath, "/src") {
			// If subdirectories would be too deep, copy this level as whole
			return node.TotalSize <= ls.maxLayerSizeBytes
		}
	}

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

	// For larger directories that are not at depth limit, prefer to split for better caching
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
			var copyCmd string
			var targetPath string

			if strings.HasSuffix(file, "/") {
				// Directory copy
				dirName := strings.TrimSuffix(file, "/")
				targetPath = filepath.Join(targetDir, dirName)

				// Check if the directory path would be too deep
				if ls.isTargetPathTooDeep(dirName, targetDir) {
					// Split the directory to avoid depth issues
					commands = append(commands, fmt.Sprintf("# ERROR: Directory %s would exceed max depth (%d), skipping",
						dirName, MaxDockerPathDepth))
					continue
				}

				copyCmd = fmt.Sprintf("COPY %s %s/", dirName, targetPath)
			} else {
				// File copy
				targetPath = filepath.Join(targetDir, file)

				// Check if the file path would be too deep
				if ls.isTargetPathTooDeep(file, targetDir) {
					// Try to copy to a shorter path in the target directory
					shortPath := ls.shortenPath(file, targetDir)
					if shortPath != "" {
						commands = append(commands, fmt.Sprintf("# WARNING: Path shortened to avoid max depth: %s -> %s",
							file, shortPath))
						targetPath = filepath.Join(targetDir, shortPath)
						copyCmd = fmt.Sprintf("COPY %s %s", file, targetPath)
					} else {
						// As absolute last resort, try flattening and copying to root of target
						flatPath := ls.flattenPath(file)
						if flatPath != "" && !ls.isTargetPathTooDeep(flatPath, targetDir) {
							commands = append(commands, fmt.Sprintf("# WARNING: Path flattened to avoid max depth: %s -> %s",
								file, flatPath))
							targetPath = filepath.Join(targetDir, flatPath)
							copyCmd = fmt.Sprintf("COPY %s %s", file, targetPath)
						} else {
							commands = append(commands, fmt.Sprintf("# ERROR: File %s exceeds max depth (%d), skipping",
								file, MaxDockerPathDepth))
							continue
						}
					}
				} else {
					copyCmd = fmt.Sprintf("COPY %s %s", file, targetPath)
				}
			}

			commands = append(commands, copyCmd)
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
