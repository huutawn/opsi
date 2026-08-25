package repositoryanalysis

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
)

type rankedFile struct {
	file File
	rank int
}

func scopeIncludes(filePath string, scope Scope) bool {
	for _, excluded := range scope.ExcludePaths {
		if filePath == excluded || strings.HasPrefix(filePath, excluded+"/") {
			return false
		}
	}
	if filePath == ".opsi/opsi-cd.yaml" || len(scope.ApplicationRoots) == 0 {
		return true
	}
	for _, root := range scope.ApplicationRoots {
		if underRoot(filePath, root) {
			return true
		}
	}
	return false
}

func selectAnalysisCandidates(files map[string]File) []File {
	ranked := make([]rankedFile, 0, len(files))
	for _, file := range files {
		if rank, ok := analysisCandidateRank(file.Path); ok {
			ranked = append(ranked, rankedFile{file: file, rank: rank})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].rank != ranked[j].rank {
			return ranked[i].rank < ranked[j].rank
		}
		return ranked[i].file.Path < ranked[j].file.Path
	})
	out := make([]File, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].file
	}
	return out
}

func analysisCandidateRank(value string) (int, bool) {
	lower, base := strings.ToLower(value), strings.ToLower(path.Base(value))
	segments := strings.Split(lower, "/")
	for _, segment := range segments[:len(segments)-1] {
		switch segment {
		case ".git", "node_modules", "vendor", ".next", "dist", "build", "bin", "obj", "coverage", "generated", "migrations":
			return 0, false
		}
	}
	if strings.HasSuffix(base, ".lock") || base == "package-lock.json" || base == "pnpm-lock.yaml" || base == "yarn.lock" || base == "go.sum" {
		return 0, false
	}
	switch {
	case lower == ".opsi/opsi-cd.yaml":
		return 0, true
	case base == "compose.yaml" || base == "compose.yml" || base == "docker-compose.yaml" || base == "docker-compose.yml":
		return 1, true
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile."):
		return 2, true
	case (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) && (strings.Contains(lower, "k8s/") || strings.Contains(lower, "kubernetes/") || strings.Contains(base, "deployment") || strings.Contains(base, "manifest")):
		return 3, true
	case base == "package.json" || base == "go.mod" || base == "requirements.txt" || base == "pyproject.toml" || strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".fsproj"):
		return 4, true
	case strings.HasPrefix(base, "appsettings") && strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".env.example") || base == ".env.example" || strings.Contains(base, "config") && supportedTextExtension(base):
		return 5, true
	case base == "readme" || strings.HasPrefix(base, "readme.") || strings.Contains(lower, "/docs/") && strings.HasSuffix(base, ".md"):
		return 6, true
	case (strings.Contains(base, "route") || strings.Contains(base, "client") || strings.Contains(base, "startup") || strings.Contains(base, "program")) && supportedSourceExtension(base):
		return 7, true
	case supportedSourceExtension(base) || strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".md"):
		return 8, true
	default:
		return 0, false
	}
}

func supportedTextExtension(base string) bool {
	return supportedSourceExtension(base) || strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".toml")
}
func supportedSourceExtension(base string) bool {
	for _, suffix := range []string{".cs", ".ts", ".tsx", ".js", ".jsx", ".go", ".py", ".rs", ".java", ".kt"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func (d Detector) readCandidates(ctx context.Context, request Request, files []File, maxBytes int64) (map[string][]byte, map[string]error) {
	contents, failures := make(map[string][]byte, len(files)), make(map[string]error)
	type outcome struct {
		path string
		data []byte
		err  error
	}
	jobs, outcomes := make(chan File), make(chan outcome, len(files))
	var workers sync.WaitGroup
	count := 8
	if len(files) < count {
		count = len(files)
	}
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for file := range jobs {
				data, err := d.Repository.ReadFile(ctx, request.InstallationID, request.Repository, request.CommitSHA, file.Path, maxBytes)
				outcomes <- outcome{path: file.Path, data: data, err: err}
			}
		}()
	}
	go func() {
		for _, file := range files {
			jobs <- file
		}
		close(jobs)
		workers.Wait()
		close(outcomes)
	}()
	for result := range outcomes {
		if result.err != nil {
			failures[result.path] = result.err
		} else if int64(len(result.data)) > maxBytes {
			failures[result.path] = errBlobSizeLimit
		} else {
			contents[result.path] = result.data
		}
	}
	return contents, failures
}
